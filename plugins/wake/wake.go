// Package wake lets a model wake itself later: a one-shot or recurring prompt
// that comes back as a message in the conversation that scheduled it. It also
// holds the asks no one can answer now, until the user's next message decides.
// The host drives it: Advance delivers what is due and says when to call again,
// so it needs no goroutine or timer and runs wherever the host does. Tools
// serves a plain engine.Agent; Extension serves an Orb session, and adds the
// held approvals.
package wake

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

const (
	maxWakes  = 50
	minPeriod = 5 * time.Minute
	approval  = 30 * time.Minute // as Grok Bot's background approvals
)

// Wake is a due prompt for a session. Call marks a held tool call: its wake is
// the approval's expiry.
type Wake struct {
	ID       string    `json:"id"`
	Session  string    `json:"session"`
	Route    string    `json:"route,omitempty"`
	CWD      string    `json:"cwd,omitempty"`
	Prompt   string    `json:"prompt"`
	Due      time.Time `json:"due"`
	Cron     string    `json:"cron,omitempty"`
	Timezone string    `json:"timezone,omitempty"`
	Call     *Call     `json:"call,omitempty"`
}

type Call struct {
	Tool string `json:"tool"`
	Args any    `json:"args"`
}

// marker opens every wake's text: an input carrying it never settles a held
// call, so a wake (routed ones come back as prompts) cannot approve one.
const marker = "[wake "

// Text is the message a wake delivers.
func (w Wake) Text() string {
	return marker + w.ID + "] " + w.Prompt
}

// Store keeps one agent's wakes, one record each, shared by its replicas: a
// table indexed by Due (scoped to the agent), or a Redis sorted set scored by
// Due, serves as well as InDocument's file. Dues are whole seconds, so a store
// may keep them at that precision.
type Store interface {
	// Put adds w, whose ID is new.
	Put(ctx context.Context, w Wake) error
	// Claim removes w, or replaces it with next (moving it in the Due index),
	// only if the stored wake with w.ID still has w.Due (time.Time.Equal), and
	// reports whether it did. The check and the write are one atomic step
	// (UPDATE or DELETE ... WHERE id = ? AND due = ?, a Lua script), so of
	// concurrent claims one alone succeeds: a wake is delivered, and a held
	// call runs, at most once, across replicas too.
	Claim(ctx context.Context, w Wake, next *Wake) (bool, error)
	// List returns session's wakes, held calls (Call set) included.
	List(ctx context.Context, session string) ([]Wake, error)
	// Due returns the wakes due at or before t.
	Due(ctx context.Context, t time.Time) ([]Wake, error)
	// Next returns the earliest due time, zero when there are no wakes.
	Next(ctx context.Context) (time.Time, error)
}

// Document is one JSON document updated atomically, such as a file.
type Document interface {
	Read(ctx context.Context) ([]byte, error)
	Update(ctx context.Context, change func([]byte) ([]byte, error)) error
}

// InDocument keeps every wake in doc, which suits one agent's file.
func InDocument(doc Document) Store {
	return inDocument{doc}
}

type inDocument struct{ doc Document }

func (d inDocument) read(ctx context.Context, keep func(Wake) bool) ([]Wake, error) {
	data, err := d.doc.Read(ctx)
	var wakes []Wake
	if err == nil && len(data) > 0 {
		err = json.Unmarshal(data, &wakes)
	}
	return slices.DeleteFunc(wakes, func(w Wake) bool { return !keep(w) }), err
}

func (d inDocument) update(ctx context.Context, change func([]Wake) []Wake) error {
	return d.doc.Update(ctx, func(data []byte) ([]byte, error) {
		var wakes []Wake
		if len(data) > 0 {
			if err := json.Unmarshal(data, &wakes); err != nil {
				return nil, err
			}
		}
		return json.Marshal(change(wakes))
	})
}

func (d inDocument) Put(ctx context.Context, w Wake) error {
	return d.update(ctx, func(wakes []Wake) []Wake {
		return append(wakes, w)
	})
}

func (d inDocument) Claim(ctx context.Context, w Wake, next *Wake) (bool, error) {
	var claimed bool
	err := d.update(ctx, func(wakes []Wake) []Wake {
		at := slices.IndexFunc(wakes, func(o Wake) bool { return o.ID == w.ID && o.Due.Equal(w.Due) })
		switch claimed = at >= 0; {
		case !claimed:
		case next != nil:
			wakes[at] = *next
		default:
			wakes = slices.Delete(wakes, at, at+1)
		}
		return wakes
	})
	return claimed && err == nil, err
}

func (d inDocument) List(ctx context.Context, session string) ([]Wake, error) {
	return d.read(ctx, func(w Wake) bool { return w.Session == session })
}

func (d inDocument) Due(ctx context.Context, t time.Time) ([]Wake, error) {
	return d.read(ctx, func(w Wake) bool { return !w.Due.After(t) })
}

func (d inDocument) Next(ctx context.Context) (time.Time, error) {
	wakes, err := d.read(ctx, func(Wake) bool { return true })
	var next time.Time
	for _, w := range wakes {
		if next.IsZero() || w.Due.Before(next) {
			next = w.Due
		}
	}
	return next, err
}

// Scheduler keeps wakes in a Store. Routes, set by the host, tag the wakes a
// session schedules (a chat conversation, say) for its deliver.
type Scheduler struct {
	store   Store
	now     func() time.Time
	deliver func(context.Context, Wake) error
	mu      sync.Mutex
	live    map[string]extensions.API
	routes  map[string]string
}

// New keeps wakes in store. A wake whose session is open in this process is
// delivered in it; deliver gets the others, and nil leaves them due until
// their session opens.
func New(store Store, now func() time.Time, deliver func(context.Context, Wake) error) *Scheduler {
	return &Scheduler{store: store, now: now, deliver: deliver, live: map[string]extensions.API{}, routes: map[string]string{}}
}

// Route tags the wakes session schedules from now on.
func (s *Scheduler) Route(session, route string) {
	s.mu.Lock()
	s.routes[session] = route
	s.mu.Unlock()
}

// due is t at the next whole second, the precision a Store keeps.
func due(t time.Time) time.Time {
	return t.Add(time.Second - 1).Truncate(time.Second)
}

// Advance delivers every due wake once, claiming it first, and returns the
// next due time (zero when none). A wake missed while the host was down fires
// once; a recurring one then moves to its next time after now.
func (s *Scheduler) Advance(ctx context.Context) (time.Time, error) {
	now := s.now()
	wakes, err := s.store.Due(ctx, now)
	if err != nil {
		return time.Time{}, err
	}
	var errs []error
	for _, w := range wakes {
		// A routed wake goes to deliver, which owns its conversation's replies.
		s.mu.Lock()
		api := s.live[w.Session]
		s.mu.Unlock()
		if w.Route != "" && s.deliver != nil {
			api = nil
		}
		if api == nil && s.deliver == nil {
			continue
		}
		var next *Wake
		if at, err := nextTime(w.Cron, w.Timezone, now); w.Cron != "" && err == nil {
			moved := w
			moved.Due = at
			next = &moved
		}
		if claimed, err := s.store.Claim(ctx, w, next); !claimed {
			errs = append(errs, err)
			continue
		}
		if api != nil {
			errs = append(errs, send(ctx, api, w.Text(), true))
		} else {
			errs = append(errs, s.deliver(ctx, w))
		}
	}
	next, err := s.store.Next(ctx)
	return next, errors.Join(append(errs, err)...)
}

func send(ctx context.Context, api extensions.API, text string, trigger bool) error {
	return api.SendMessage(ctx, extensions.CustomMessage{CustomType: "wake", Content: text, Display: true},
		&extensions.SendMessageOptions{TriggerTurn: &trigger, DeliverAs: extensions.DeliverFollowUp})
}

// Defer holds a tool call that needs an approval no one can give now, and
// returns what the model is told: the user's next message decides.
func (s *Scheduler) Defer(ctx context.Context, session, cwd, tool string, args any) string {
	s.mu.Lock()
	route := s.routes[session]
	s.mu.Unlock()
	w := Wake{ID: newID(), Session: session, Route: route, CWD: cwd, Due: due(s.now().Add(approval)), Call: &Call{tool, args},
		Prompt: "The approval request for " + tool + " got no answer within 30 minutes, so the call did not run."}
	held, _ := json.Marshal(w.Call)
	wakes, err := s.store.List(ctx, session)
	switch {
	case err != nil:
	case slices.ContainsFunc(wakes, func(o Wake) bool {
		same, _ := json.Marshal(o.Call)
		return string(same) == string(held)
	}): // one held call per identical retry (calls made at once may hold twice, and run twice on a yes)
	case len(wakes) >= maxWakes:
		err = fmt.Errorf("%d wakes are already scheduled", maxWakes)
	default:
		err = s.store.Put(ctx, w)
	}
	if err != nil {
		return "This call needs the user's approval, which could not be requested: " + err.Error()
	}
	return fmt.Sprintf("This %s call needs the user's approval and is held. Ask the user in this conversation to approve or deny it, saying exactly what it does, then end your turn. "+
		"If their next message approves it (yes, oui, ok), it runs and you get its result; otherwise, or without an answer within 30 minutes, it does not run.", tool)
}

type approvalKey struct{}

// Approved reports whether ctx runs the held tool call the user approved; it
// answers true once.
func (s *Scheduler) Approved(ctx context.Context, tool string) bool {
	held, _ := ctx.Value(approvalKey{}).(*heldCall)
	return held != nil && held.tool == tool && held.used.CompareAndSwap(false, true)
}

type heldCall struct {
	tool string
	used atomic.Bool
}

// decide settles session's held calls with the user's message: a yes runs them,
// anything else denies them. It returns their outcomes, which the input hook
// puts before the message: a native session (claudesessions) only reads its turn's prompt.
func (s *Scheduler) decide(ctx context.Context, session, text string, runner extensions.Context) string {
	now := s.now()
	wakes, err := s.store.List(ctx, session)
	if err != nil {
		return ""
	}
	var reports []string
	var taken []Wake
	for _, w := range wakes {
		// An expired call is past deciding: Advance delivers its expiry.
		if w.Call == nil || !w.Due.After(now) {
			continue
		}
		// Every call is claimed before any runs: one runs at most once, and a
		// long one does not let the next expire.
		if claimed, err := s.store.Claim(ctx, w, nil); claimed {
			taken = append(taken, w)
		} else if err != nil {
			reports = append(reports, fmt.Sprintf("The %s call could not be settled, so it did not run: %v", w.Call.Tool, err))
		}
	}
	yes := approves(text)
	for _, w := range taken {
		report := fmt.Sprintf("The user denied the %s call, so it did not run.", w.Call.Tool)
		if tools, ok := runner.(extensions.ToolContext); yes && ok {
			outcome := tools.ExecuteTool(context.WithValue(ctx, approvalKey{}, &heldCall{tool: w.Call.Tool}), w.Call.Tool, w.Call.Args, nil)
			state := "ran"
			if outcome.IsError {
				state = "failed"
			}
			var out strings.Builder
			for _, block := range outcome.Result.Content {
				if text, ok := block.(*ai.TextContent); ok {
					out.WriteString(text.Text)
				}
			}
			report = fmt.Sprintf("The user approved the %s call, which %s:\n%s", w.Call.Tool, state, out.String())
		}
		reports = append(reports, report)
	}
	return strings.Join(reports, "\n\n")
}

// approves reads a yes from the user's message: a Buzz event (buzz-acp)
// carries it in its last Content field, after the agent's @mention, and a
// group chat's after its sender's "Name: ".
func approves(text string) bool {
	if at := strings.LastIndex(text, "\nContent: "); at >= 0 {
		text, _, _ = strings.Cut(text[at+len("\nContent: "):], "\n")
	}
	candidates := []string{text}
	if name, rest, ok := strings.Cut(text, ": "); ok && len(strings.Fields(name)) <= 3 {
		candidates = append(candidates, rest)
	}
	for _, candidate := range candidates {
		for _, word := range strings.Fields(strings.ToLower(candidate)) {
			if !strings.HasPrefix(word, "@") {
				if slices.Contains([]string{"yes", "oui", "ok", "okay", "y", "approve", "approved"}, strings.Trim(word, ".,!:;")) {
					return true
				}
				break
			}
		}
	}
	return false
}

var (
	scheduleSchema = ai.JSONSchema(`{"type":"object","required":["prompt"],"properties":{
"prompt":{"type":"string","description":"What to do when it fires, written for yourself: it comes back as a message in this conversation"},
"at":{"type":"string","description":"One-shot time, RFC 3339 (2026-11-15T20:00:00+01:00) or local 2026-11-15T20:00"},
"delay":{"type":"string","description":"One-shot delay from now: 90s, 30m, 2h, 1h30m"},
"cron":{"type":"string","description":"Recurring schedule, 5 cron fields (minute hour day month weekday), e.g. 0 9 * * 1-5"},
"timezone":{"type":"string","description":"IANA zone for at and cron, e.g. Europe/Paris; default the host's"}}}`)
	idSchema = ai.JSONSchema(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"The wake ID schedule_wake returned"}}}`)
)

// tools are wake's tools, run for a session and its working directory.
var tools = []struct {
	name, label, snippet, description string
	parameters                        ai.JSONSchema
	run                               func(s *Scheduler, ctx context.Context, session, cwd string, raw any) (engine.AgentToolResult, error)
}{
	{"schedule_wake", "Schedule wake", "Wake yourself later, once or on a schedule",
		"Schedule a prompt to come back to you in this conversation, as a message that starts a turn: once (at or delay) or recurring (cron). " +
			"Give exactly one of at, delay, cron. Use it for follow-ups, reminders and recurring work instead of waiting; a recurring wake runs at most every 5 minutes.",
		scheduleSchema, (*Scheduler).schedule},
	{"list_wakes", "List wakes", "List this conversation's scheduled wakes",
		"List the wakes scheduled in this conversation: ID, next time, schedule and prompt.",
		ai.JSONSchema(`{"type":"object","properties":{}}`), (*Scheduler).list},
	{"cancel_wake", "Cancel wake", "Cancel a scheduled wake",
		"Cancel a wake scheduled in this conversation, one-shot or recurring.", idSchema, (*Scheduler).cancel},
}

// Tools are schedule_wake, list_wakes and cancel_wake for session, whose wakes
// go to the host's deliver: an SDK host hands them to its engine.Agent.
func (s *Scheduler) Tools(session, cwd string) []engine.AgentTool {
	var out []engine.AgentTool
	for _, t := range tools {
		out = append(out, engine.AgentToolFunc{AgentToolSpec: engine.AgentToolSpec{Name: t.name, Label: t.label, Description: t.description, Parameters: t.parameters},
			Run: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
				return t.run(s, ctx, session, cwd, raw)
			}})
	}
	return out
}

// Extension adds schedule_wake, list_wakes and cancel_wake, and delivers due
// wakes inside the session while it is open.
func (s *Scheduler) Extension() extensions.Factory {
	return func(api extensions.API) error {
		var session string
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			s.mu.Lock()
			session = ctx.SessionManager().GetSessionID()
			s.live[session] = api
			s.mu.Unlock()
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			s.mu.Lock()
			if s.live[session] == api {
				delete(s.live, session)
			}
			s.mu.Unlock()
			return nil, nil
		})
		api.On(extensions.EventInput, func(ctx context.Context, event extensions.Event, runner extensions.Context) (any, error) {
			if input, ok := event.(extensions.InputEvent); ok && input.Source != extensions.InputExtension && !strings.Contains(input.Text, marker) {
				if report := s.decide(ctx, runner.SessionManager().GetSessionID(), input.Text, runner); report != "" {
					return extensions.InputResult{Action: extensions.InputTransform, Text: report + "\n\n" + input.Text, Images: input.Images}, nil
				}
			}
			return nil, nil
		})
		for _, t := range tools {
			api.RegisterTool(extensions.ToolDefinition{
				Name: t.name, Label: t.label, Description: t.description, PromptSnippet: t.snippet, Parameters: t.parameters,
				Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, session extensions.Context) (engine.AgentToolResult, error) {
					return t.run(s, ctx, session.SessionManager().GetSessionID(), session.CWD(), raw)
				},
			})
		}
		return nil
	}
}

func (s *Scheduler) schedule(ctx context.Context, session, cwd string, raw any) (engine.AgentToolResult, error) {
	var in struct{ Prompt, At, Delay, Cron, Timezone string }
	if err := toolutil.Decode(raw, &in); err != nil {
		return engine.AgentToolResult{}, err
	}
	now := s.now()
	w := Wake{ID: newID(), Session: session, CWD: cwd, Prompt: strings.TrimSpace(in.Prompt), Cron: strings.TrimSpace(in.Cron), Timezone: in.Timezone}
	location, err := zone(in.Timezone)
	switch {
	case err != nil:
	case w.Prompt == "":
		err = errors.New("prompt is required")
	case len(slices.DeleteFunc([]string{in.At, in.Delay, w.Cron}, func(v string) bool { return v == "" })) != 1:
		err = errors.New("give exactly one of at, delay, cron")
	case in.At != "":
		if w.Due, err = time.Parse(time.RFC3339, in.At); err != nil {
			w.Due, err = time.ParseInLocation("2006-01-02T15:04", in.At, location)
		}
	case in.Delay != "":
		var delay time.Duration
		delay, err = time.ParseDuration(in.Delay)
		w.Due = now.Add(delay)
	default:
		if w.Due, err = nextTime(w.Cron, in.Timezone, now); err == nil {
			// The minute and hour fields alone set the shortest gap, and two
			// days of them, every day matching, show all of their gaps.
			daily, day := strings.Join(strings.Fields(w.Cron)[:2], " ")+" * * *", time.Time{}
			for at, _ := nextTime(daily, "UTC", day); err == nil && at.Before(day.Add(48*time.Hour)); {
				after, _ := nextTime(daily, "UTC", at)
				if after.Sub(at) < minPeriod {
					err = fmt.Errorf("a recurring wake runs at most every %s", minPeriod)
				}
				at = after
			}
		}
	}
	if w.Due = due(w.Due); err == nil && !w.Due.After(now) {
		err = errors.New("that time has passed")
	}
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	s.mu.Lock()
	w.Route = s.routes[w.Session]
	s.mu.Unlock()
	if wakes, err := s.store.List(ctx, session); err != nil {
		return engine.AgentToolResult{}, err
	} else if len(wakes) >= maxWakes {
		return engine.AgentToolResult{}, fmt.Errorf("%d wakes are already scheduled in this conversation; cancel one first", maxWakes)
	}
	if err := s.store.Put(ctx, w); err != nil {
		return engine.AgentToolResult{}, err
	}
	return toolutil.TextResult(fmt.Sprintf("Scheduled wake %s, next at %s.", w.ID, w.Due.In(location).Format(time.RFC3339))), nil
}

func (s *Scheduler) list(ctx context.Context, session, _ string, _ any) (engine.AgentToolResult, error) {
	wakes, err := s.store.List(ctx, session)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	var out strings.Builder
	for _, w := range wakes {
		if w.Call == nil {
			location, _ := zone(w.Timezone)
			fmt.Fprintf(&out, "%s  %s  %s  %s\n", w.ID, w.Due.In(location).Format(time.RFC3339), cmp.Or(w.Cron, "once"), w.Prompt)
		}
	}
	return toolutil.TextResult(cmp.Or(out.String(), "No wakes are scheduled in this conversation.")), nil
}

func (s *Scheduler) cancel(ctx context.Context, session, _ string, raw any) (engine.AgentToolResult, error) {
	var in struct{ ID string }
	if err := toolutil.Decode(raw, &in); err != nil {
		return engine.AgentToolResult{}, err
	}
	// A wake that fires meanwhile moves on: cancel the one it has become.
	for range 3 {
		wakes, err := s.store.List(ctx, session)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		index := slices.IndexFunc(wakes, func(w Wake) bool { return w.ID == in.ID && w.Call == nil })
		if index < 0 {
			return engine.AgentToolResult{}, fmt.Errorf("no wake %q in this conversation", in.ID)
		}
		if claimed, err := s.store.Claim(ctx, wakes[index], nil); err != nil {
			return engine.AgentToolResult{}, err
		} else if claimed {
			return toolutil.TextResult("Cancelled wake " + in.ID + "."), nil
		}
	}
	return engine.AgentToolResult{}, fmt.Errorf("wake %q keeps firing; try again", in.ID)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func zone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// nextTime is the first minute after t that cron spec matches in timezone.
// Fields take *, lists, ranges and steps; day of month and weekday match
// either when both are restricted, as in cron.
func nextTime(spec, timezone string, t time.Time) (time.Time, error) {
	location, err := zone(timezone)
	if err != nil {
		return time.Time{}, err
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return time.Time{}, errors.New("cron takes 5 fields: minute hour day month weekday")
	}
	var sets [5]uint64
	for i, bounds := range [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}} {
		for _, part := range strings.Split(fields[i], ",") {
			values, step, stepped := strings.Cut(part, "/")
			low, high := bounds[0], bounds[1]
			if values != "*" {
				first, last, ranged := strings.Cut(values, "-")
				if low, err = strconv.Atoi(first); err == nil && ranged {
					high, err = strconv.Atoi(last)
				} else if !stepped {
					high = low
				}
			}
			every := 1
			if stepped && err == nil {
				every, err = strconv.Atoi(step)
			}
			if err != nil || low < bounds[0] || high > bounds[1] || low > high || every < 1 {
				return time.Time{}, fmt.Errorf("cron field %q is invalid", fields[i])
			}
			for v := low; v <= high; v += every {
				sets[i] |= 1 << v
			}
		}
	}
	if sets[4]&(1<<7) != 0 {
		sets[4] |= 1
	}
	in := func(set uint64, v int) bool { return set&(1<<v) != 0 }
	for m, end := t.In(location).Truncate(time.Minute).Add(time.Minute), t.AddDate(5, 0, 0); m.Before(end); m = m.Add(time.Minute) {
		day, weekday := in(sets[2], m.Day()), in(sets[4], int(m.Weekday()))
		if strings.HasPrefix(fields[2], "*") || strings.HasPrefix(fields[4], "*") {
			day = day && weekday
		} else {
			day = day || weekday
		}
		if day && in(sets[0], m.Minute()) && in(sets[1], m.Hour()) && in(sets[3], int(m.Month())) {
			return m, nil
		}
	}
	return time.Time{}, errors.New("cron never matches")
}
