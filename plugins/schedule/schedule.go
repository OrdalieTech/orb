// Package schedule lets a model schedule its own turns: a prompt that runs once
// or on a recurrence, in the session that created it or in a new one. Tasks and
// their latest runs live in one document that every holder process (orb chat,
// the Bridge daemon) shares; claiming an occurrence there is what keeps two
// holders from firing it twice.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

const (
	maxTasks      = 20
	maxAgentTasks = 5
	maxAgentRuns  = 48 // in 24 h, every agent task together
	maxRuns       = 50
	minGap        = 5 * time.Minute
	busyFor       = time.Hour
	staleHolder   = 3 * time.Minute
	pruneHolder   = 24 * time.Hour
	maxFailures   = 5
)

const (
	running     = "running"
	done        = "done"
	quiet       = "quiet"
	failed      = "failed"
	skipped     = "skipped"
	interrupted = "interrupted"
)

// Options configure a Scheduler. Only Store is required.
type Options struct {
	Store document.Document
	// Run delivers a run's turn and returns once it settles or ctx ends, with the
	// session it ran in and its messages; Sessions is Run for AgentSession hosts.
	// Nil means this process fires nothing.
	Run  func(ctx context.Context, turn Turn) (session string, messages engine.AgentMessages, err error)
	Zone *time.Location // the owner's default zone; nil is UTC
	// ActiveHours is "08:00-22:00" in Zone; "" is always.
	ActiveHours string
	Timeout     time.Duration // a run's turn; 0 is 30 min
	Now         func() time.Time
	After       func(time.Duration) <-chan time.Time
	Name        string // how this holder shows in runs, e.g. "orb chat"
}

// Turn is a run as Run delivers it.
type Turn struct {
	Session string // the task's session; "" is a new session in Cwd
	Cwd     string
	Model   string // provider/id a new session runs with; "" is the default
	Prompt  string // the task's prompt, then the run's marker
}

// ErrBusy, wrapped in Run's error, says the task's session is held elsewhere:
// the run retries every minute, for an hour at most.
var ErrBusy = errors.New("busy")

// scheduled marks a scheduled run's context: the tasks its tool calls create are the agent's.
type scheduled struct{}

// Scheduler is one holder's view of the schedule document.
type Scheduler struct {
	o  Options
	id string // this holder's key in holders

	mu   sync.Mutex
	live map[string]*live // sessions open in this process, by ID
	// placed counts runs in place by session: a person's turn queued alongside one
	// carries no marker, so every tool call there counts as the run's.
	placed map[string]int
}

// live is a session open in this process, where runs for it are delivered.
type live struct {
	api   extensions.API
	ctx   extensions.Context
	waits map[string]chan engine.AgentMessages // by run marker
	ended map[string]engine.AgentMessages      // by run marker, its latest agent_end until the run settles
}

type doc struct {
	Version   int               `json:"version"`
	Tasks     []*task           `json:"tasks"`
	Runs      []*run            `json:"runs"`
	Holders   map[string]holder `json:"holders"`
	AgentRuns []time.Time       `json:"agentRuns,omitempty"`
}

type task struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Prompt   string    `json:"prompt"`
	When     string    `json:"when"`
	Zone     string    `json:"zone"`
	Target   string    `json:"target"`
	Session  string    `json:"session"`
	Cwd      string    `json:"cwd"`
	Model    string    `json:"model"`
	By       string    `json:"by"`
	Enabled  bool      `json:"enabled"`
	Created  time.Time `json:"created"`
	Next     time.Time `json:"next"`
	Failures int       `json:"failures"`
}

type run struct {
	Task    string    `json:"task"`
	At      time.Time `json:"at"`
	Holder  string    `json:"holder"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	Session string    `json:"session"`
	Detail  string    `json:"detail"`
}

type holder struct {
	Name string    `json:"name"`
	Seen time.Time `json:"seen"`
}

func New(o Options) *Scheduler {
	if o.Zone == nil {
		o.Zone = time.UTC
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Minute
	}
	return &Scheduler{o: o, id: randomID(4), live: map[string]*live{}, placed: map[string]int{}}
}

func (s *Scheduler) now() time.Time { return s.o.Now().UTC().Truncate(time.Second) }

func (s *Scheduler) holder() string { return s.o.Name + "#" + s.id }

var toolSchema = ai.JSONSchema(`{"type":"object","required":["action"],"properties":{
"action":{"type":"string","enum":["create","list","update","delete"]},
"id":{"type":"string","description":"The task to update or delete, as list or create gave it"},
"title":{"type":"string","description":"A short name for the task"},
"prompt":{"type":"string","description":"What each run asks, as a user message; it may start with /skill:name"},
"when":{"type":"string","description":"'in 20m', 'at 2026-11-08 08:30' (local time in zone, or RFC 3339), 'every 2h' (at least 5m), or 'cron 30 8 * * 1-5' (5 fields, in zone)"},
"zone":{"type":"string","description":"IANA zone for at and cron; defaults to the owner's"},
"target":{"type":"string","enum":["this","new"],"description":"Run in this session, or in a new session in the same directory each time; defaults to this for a one-shot, new for a recurring task"},
"model":{"type":"string","description":"provider/id to run a new-session task with; defaults to the default model"},
"enabled":{"type":"boolean","description":"false pauses the task, true resumes it"}}}`)

// Extension adds the schedule tool, and delivers runs in place to the sessions
// this process has open.
func (s *Scheduler) Extension() extensions.Factory {
	return func(api extensions.API) error {
		var l *live
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, c extensions.Context) (any, error) {
			s.mu.Lock()
			l = &live{api: api, ctx: c, waits: map[string]chan engine.AgentMessages{}, ended: map[string]engine.AgentMessages{}}
			s.live[c.SessionManager().GetSessionID()] = l
			s.mu.Unlock()
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(_ context.Context, _ extensions.Event, c extensions.Context) (any, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if l != nil {
				for _, wait := range l.waits {
					close(wait)
				}
				l.waits = nil
			}
			delete(s.live, c.SessionManager().GetSessionID())
			return nil, nil
		})
		api.On(extensions.EventAgentEnd, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
			end, _ := event.(extensions.AgentEndEvent)
			s.mu.Lock()
			defer s.mu.Unlock()
			if l == nil {
				return nil, nil
			}
			// A retry's agent_end holds only what it added, so a run's latest one stands until it settles.
			for mark := range l.waits {
				if _, ok := l.ended[mark]; ok || slices.ContainsFunc(end.Messages, func(m engine.AgentMessage) bool {
					u, ok := m.(*ai.UserMessage)
					return ok && strings.Contains(userText(u.Content), mark)
				}) {
					l.ended[mark] = end.Messages
				}
			}
			return nil, nil
		})
		// A run ends once its session settles, after any retry or compaction recovery.
		api.On(extensions.EventAgentSettled, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if l == nil {
				return nil, nil
			}
			for mark, messages := range l.ended {
				if wait, ok := l.waits[mark]; ok {
					wait <- messages
					delete(l.waits, mark)
				}
				delete(l.ended, mark)
			}
			return nil, nil
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "schedule", Label: "Schedule", PromptSnippet: "Schedule a prompt to run later or on a recurrence",
			Description: toolDescription, PromptGuidelines: toolGuidelines, Parameters: toolSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, c extensions.Context) (engine.AgentToolResult, error) {
				models := func(model string) bool {
					provider, id, _ := strings.Cut(model, "/")
					if registry := c.ModelRegistry(); registry != nil {
						_, ok := registry.Find(provider, id)
						return ok
					}
					return false
				}
				return s.execute(ctx, raw, c.SessionManager().GetSessionID(), c.CWD(), models)
			},
		})
		return nil
	}
}

// Tool is the schedule tool for a host without extensions, such as a bare
// engine.Agent: session and cwd are the conversation it serves, and models
// says whether a provider/id may run a task (nil allows any).
func (s *Scheduler) Tool(session, cwd string, models func(string) bool) engine.AgentTool {
	if models == nil {
		models = func(string) bool { return true }
	}
	return engine.AgentToolFunc{
		AgentToolSpec: engine.AgentToolSpec{Name: "schedule", Label: "Schedule", Description: toolDescription + " " + strings.Join(toolGuidelines, " "), Parameters: toolSchema},
		Run: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
			return s.execute(ctx, raw, session, cwd, models)
		},
	}
}

const toolDescription = "Create, list, update or delete scheduled tasks: a prompt that runs once or on a recurrence, in this session or in a new one. A run receives the prompt followed by its marker [scheduled · title · id]; it reaches people only through what the run itself does or says."

var toolGuidelines = []string{
	"Use schedule for reminders, follow-ups and recurring checks instead of waiting or polling.",
	"A run whose final answer is exactly NO_REPLY is recorded as quiet: tell a recurring check to answer NO_REPLY when there is nothing to report.",
}

func userText(content ai.UserContent) string {
	if content.Text != nil {
		return *content.Text
	}
	return ai.ContentText(content.Blocks)
}

type input struct {
	Action  string  `json:"action"`
	ID      string  `json:"id"`
	Title   *string `json:"title"`
	Prompt  *string `json:"prompt"`
	When    *string `json:"when"`
	Zone    *string `json:"zone"`
	Target  *string `json:"target"`
	Model   *string `json:"model"`
	Enabled *bool   `json:"enabled"`
}

func (s *Scheduler) execute(ctx context.Context, raw any, sid, cwd string, models func(string) bool) (engine.AgentToolResult, error) {
	var in input
	if err := toolutil.Decode(raw, &in); err != nil {
		return engine.AgentToolResult{}, err
	}
	now := s.now()
	if in.Action == "list" {
		b, err := s.o.Store.Read(ctx)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		d, err := decode(b)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		out, _ := json.MarshalIndent(map[string]any{"tasks": d.Tasks, "runs": d.Runs[max(0, len(d.Runs)-10):]}, "", " ")
		return toolutil.TextResult(string(out)), nil
	}
	s.mu.Lock()
	agentTurn := ctx.Value(scheduled{}) != nil || s.placed[sid] > 0
	s.mu.Unlock()
	var text string
	err := s.o.Store.Update(ctx, func(b []byte) ([]byte, error) {
		d, err := decode(b)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(d.Tasks, func(k *task) bool { return k.ID == in.ID })
		if in.Action != "create" && i < 0 {
			return nil, fmt.Errorf("no task %q; list gives the tasks", in.ID)
		}
		switch in.Action {
		case "create":
			k := &task{ID: "s" + randomID(4), Zone: s.o.Zone.String(), By: "person", Cwd: cwd, Enabled: true, Created: now}
			for slices.ContainsFunc(d.Tasks, func(o *task) bool { return o.ID == k.ID }) {
				k.ID = "s" + randomID(4)
			}
			if agentTurn {
				k.By = "agent"
			}
			in.set(k, sid)
			if k.Target == "" {
				k.Target = "new"
				if oneShot(k.When) {
					k.Target, k.Session = "this", sid
				}
			}
			if err := check(d, k, now, true, agentTurn, models); err != nil {
				return nil, err
			}
			d.Tasks = append(d.Tasks, k)
			text = fmt.Sprintf("Scheduled %s (%s), next run %s.", k.ID, k.Title, at(k))
		case "update":
			k := *d.Tasks[i]
			in.set(&k, sid)
			if paused := *d.Tasks[i]; agentTurn && paused.By == "person" {
				if paused.Enabled = false; k != paused {
					return nil, fmt.Errorf("a scheduled run can only pause (enabled:false) or delete a task a person created")
				}
			}
			reschedule := in.When != nil || in.Zone != nil || in.Enabled != nil && *in.Enabled && !d.Tasks[i].Enabled
			if err := check(d, &k, now, reschedule, agentTurn, models); err != nil {
				return nil, err
			}
			*d.Tasks[i] = k
			text = fmt.Sprintf("Updated %s (%s), next run %s.", k.ID, k.Title, at(&k))
			if !k.Enabled {
				text = fmt.Sprintf("Paused %s (%s).", k.ID, k.Title)
			}
		case "delete":
			text = fmt.Sprintf("Deleted %s (%s).", d.Tasks[i].ID, d.Tasks[i].Title)
			d.Tasks = slices.Delete(d.Tasks, i, i+1)
		default:
			return nil, fmt.Errorf("unknown action %q", in.Action)
		}
		if !slices.ContainsFunc(holders(d), func(h holder) bool { return now.Sub(h.Seen) <= staleHolder }) {
			text += " Nothing fires on this machine right now: no Orb process that runs schedules (orb chat, the Bridge) is up."
		}
		return encode(d)
	})
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	return toolutil.TextResult(text), nil
}

func (in input) set(k *task, sid string) {
	for field, value := range map[*string]*string{&k.Title: in.Title, &k.Prompt: in.Prompt, &k.When: in.When, &k.Zone: in.Zone, &k.Model: in.Model} {
		if value != nil {
			*field = strings.TrimSpace(*value)
		}
	}
	if in.Target != nil {
		k.Target, k.Session = *in.Target, ""
		if k.Target == "this" {
			k.Session = sid
		}
	}
	if in.Enabled != nil {
		k.Enabled = *in.Enabled
	}
}

func holders(d *doc) []holder {
	out := make([]holder, 0, len(d.Holders))
	for _, h := range d.Holders {
		out = append(out, h)
	}
	return out
}

func at(k *task) string {
	loc, _ := time.LoadLocation(k.Zone)
	return k.Next.In(loc).Format("Mon 2 Jan 15:04 MST")
}

// check validates k as it would be stored in d, setting its next run when reschedule.
func check(d *doc, k *task, now time.Time, reschedule, agentTurn bool, models func(string) bool) error {
	switch {
	case k.Title == "":
		return errors.New("title is empty")
	case k.Prompt == "":
		return errors.New("prompt is empty")
	case k.Target != "this" && k.Target != "new":
		return fmt.Errorf("target is this or new, not %q", k.Target)
	case k.Model != "" && k.Target == "this":
		return errors.New("model applies only to target new; this keeps the session's model")
	case k.Model != "" && !models(k.Model):
		return fmt.Errorf("model %q is not available; give provider/id", k.Model)
	}
	loc, err := time.LoadLocation(k.Zone)
	if err != nil || k.Zone == "" {
		return fmt.Errorf("unknown zone %q; give an IANA zone such as Europe/Paris", k.Zone)
	}
	if reschedule {
		r, err := parse(k.When, loc, now)
		if err != nil {
			return err
		}
		if k.Next = r.first(now); agentTurn && k.Next.Before(now.Add(minGap)) {
			return errors.New("a task created during a scheduled run fires 5 min out at the earliest")
		}
	}
	others := slices.DeleteFunc(slices.Clone(d.Tasks), func(o *task) bool { return o.ID == k.ID })
	if k.Enabled && countTasks(others, func(o *task) bool { return o.Enabled }) >= maxTasks {
		return fmt.Errorf("%d tasks are enabled already; delete or pause one", maxTasks)
	}
	if k.By == "agent" && countTasks(others, func(o *task) bool { return o.By == "agent" }) >= maxAgentTasks {
		return fmt.Errorf("scheduled runs have created %d tasks already; delete one first", maxAgentTasks)
	}
	return nil
}

func countTasks(tasks []*task, ok func(*task) bool) int {
	n := 0
	for _, k := range tasks {
		if ok(k) {
			n++
		}
	}
	return n
}

// Run fires due occurrences until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	if s.o.Run == nil {
		return errors.New("schedule: Run needs Options.Run")
	}
	if _, _, err := hours(s.o.ActiveHours); err != nil {
		return err
	}
	for {
		wait := s.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-s.o.After(wait):
		}
	}
}

// tick beats this holder's heartbeat, interrupts the runs of holders gone
// silent and claims the occurrences due; it returns how long to wait for the next.
func (s *Scheduler) tick(ctx context.Context) time.Duration {
	now := s.now()
	wait := time.Minute
	var claims []task
	var runs []run
	err := s.o.Store.Update(ctx, func(b []byte) ([]byte, error) {
		d, err := decode(b)
		if err != nil {
			return nil, err
		}
		claims, runs, wait = nil, nil, time.Minute
		d.Holders[s.id] = holder{s.o.Name, now}
		for id, h := range d.Holders {
			if now.Sub(h.Seen) > pruneHolder {
				delete(d.Holders, id)
			}
		}
		for _, r := range d.Runs {
			id := r.Holder[strings.LastIndexByte(r.Holder, '#')+1:]
			if h, ok := d.Holders[id]; r.Status == running && (!ok || now.Sub(h.Seen) > staleHolder) {
				r.Status, r.Ended, r.Detail = interrupted, now, "its holder stopped"
			}
		}
		d.AgentRuns = slices.DeleteFunc(d.AgentRuns, func(t time.Time) bool { return now.Sub(t) > 24*time.Hour })
		for _, k := range slices.Clone(d.Tasks) {
			if k.Enabled && !k.Next.After(now) {
				if r := s.claim(d, k, now); r != nil {
					claims, runs = append(claims, *k), append(runs, *r)
				}
			}
			if k.Enabled && slices.Contains(d.Tasks, k) {
				wait = min(wait, k.Next.Sub(now))
			}
		}
		// The oldest finished runs go first; a running one is how claim merges its next occurrence.
		drop := len(d.Runs) - maxRuns
		d.Runs = slices.DeleteFunc(d.Runs, func(r *run) bool {
			if drop > 0 && r.Status != running {
				drop--
				return true
			}
			return false
		})
		return encode(d)
	})
	if err != nil {
		return time.Minute
	}
	for i := range claims {
		go s.fire(ctx, claims[i], runs[i])
	}
	return wait
}

// claim records the latest due occurrence of k as a run and moves k past now;
// it returns the run when this holder is to fire it. An occurrence due while a
// run of k is still going merges into that run.
func (s *Scheduler) claim(d *doc, k *task, now time.Time) *run {
	due, next, recurring := k.Next, time.Time{}, false
	if loc, err := time.LoadLocation(k.Zone); err == nil {
		if r, err := parse(k.When, loc, now); err == nil {
			due, next, recurring = r.due(k.Next, now)
		}
	}
	if recurring {
		k.Next = next
	} else {
		d.Tasks = slices.DeleteFunc(d.Tasks, func(o *task) bool { return o == k })
	}
	if slices.ContainsFunc(d.Runs, func(r *run) bool { return r.Task == k.ID && (r.At.Equal(due) || r.Status == running) }) {
		return nil
	}
	r := &run{Task: k.ID, At: due, Holder: s.holder(), Status: running, Started: now}
	switch {
	case !s.active(now):
		r.Status, r.Ended, r.Detail = skipped, now, "outside active hours"
	case k.By == "agent" && len(d.AgentRuns) >= maxAgentRuns:
		r.Status, r.Ended, r.Detail = skipped, now, "agent daily limit"
	case k.By == "agent":
		d.AgentRuns = append(d.AgentRuns, now)
	}
	d.Runs = append(d.Runs, r)
	if r.Status != running {
		return nil
	}
	return r
}

func (s *Scheduler) active(t time.Time) bool {
	from, to, _ := hours(s.o.ActiveHours)
	if from == to {
		return true
	}
	local := t.In(s.o.Zone)
	m := local.Hour()*60 + local.Minute()
	if from < to {
		return from <= m && m < to
	}
	return m >= from || m < to
}

// hours parses "08:00-22:00" into minutes of the day; "" is all day.
func hours(spec string) (from, to int, err error) {
	if spec == "" {
		return 0, 0, nil
	}
	a, b, _ := strings.Cut(spec, "-")
	x, errA := time.Parse("15:04", strings.TrimSpace(a))
	y, errB := time.Parse("15:04", strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return 0, 0, fmt.Errorf("schedule: active hours %q are not HH:MM-HH:MM", spec)
	}
	return x.Hour()*60 + x.Minute(), y.Hour()*60 + y.Minute(), nil
}

// fire delivers one claimed run and records how it ended.
func (s *Scheduler) fire(ctx context.Context, k task, r run) {
	status, detail, session := s.deliver(ctx, k, r.Started)
	_ = s.o.Store.Update(context.WithoutCancel(ctx), func(b []byte) ([]byte, error) {
		d, err := decode(b)
		if err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(d.Tasks, func(o *task) bool { return o.ID == k.ID }); i >= 0 {
			switch t := d.Tasks[i]; status {
			case done, quiet:
				t.Failures = 0
			case failed:
				if t.Failures++; t.Failures >= maxFailures {
					t.Enabled, detail = false, detail+"; paused after 5 failures"
				}
			}
		}
		for _, o := range d.Runs {
			if o.Task == r.Task && o.At.Equal(r.At) && o.Holder == r.Holder {
				o.Status, o.Detail, o.Session, o.Ended = status, detail, session, s.now()
			}
		}
		return encode(d)
	})
}

func (s *Scheduler) deliver(ctx context.Context, k task, started time.Time) (status, detail, session string) {
	mark := fmt.Sprintf("[scheduled · %s · %s]", k.Title, k.ID)
	text := k.Prompt + "\n\n" + mark
	if strings.HasPrefix(k.Prompt, "/skill:") && !strings.Contains(k.Prompt, " ") {
		text = k.Prompt + " \n\n" + mark // a skill's name ends at the first space
	}
	for {
		if !s.active(s.now()) {
			return skipped, "outside active hours", ""
		}
		if k.Target == "this" {
			s.mu.Lock()
			l := s.live[k.Session]
			s.mu.Unlock()
			if l != nil {
				status, detail := s.inPlace(ctx, l, k.Session, mark, text, started)
				return status, detail, k.Session
			}
		}
		turn := Turn{Cwd: k.Cwd, Model: k.Model, Prompt: text}
		if k.Target == "this" {
			turn.Session = k.Session
		}
		status, detail, session, err := s.turn(ctx, turn)
		if !errors.Is(err, ErrBusy) {
			return status, detail, session
		}
		if s.now().Sub(started) >= busyFor {
			return skipped, err.Error(), k.Session
		}
		select {
		case <-ctx.Done():
			return interrupted, "its holder stopped", ""
		case <-s.o.After(time.Minute):
		}
	}
}

// turn delivers t through Run, whose context ends at the timeout.
func (s *Scheduler) turn(ctx context.Context, t Turn) (status, detail, session string, err error) {
	runCtx, cancel := context.WithCancel(context.WithValue(ctx, scheduled{}, true))
	defer cancel()
	timedOut := make(chan struct{})
	go func() {
		select {
		case <-s.o.After(s.o.Timeout):
			close(timedOut)
			cancel()
		case <-runCtx.Done():
		}
	}()
	session, messages, err := s.o.Run(runCtx, t)
	select {
	case <-timedOut:
		return failed, "timeout", session, nil
	default:
	}
	switch {
	case ctx.Err() != nil:
		return interrupted, "its holder stopped", session, nil
	case err != nil:
		return failed, err.Error(), session, err
	}
	status, detail = outcome(messages)
	return status, detail, session, nil
}

// Sessions is Run for hosts that run AgentSessions, such as orb chat: open gives
// a stored session (session != "") or a new one in cwd, and close disposes it.
// An error opening a stored session counts as ErrBusy.
func Sessions(open func(ctx context.Context, session, cwd string) (*agent.AgentSession, func(), error)) func(context.Context, Turn) (string, engine.AgentMessages, error) {
	return func(ctx context.Context, t Turn) (string, engine.AgentMessages, error) {
		rt, closeSession, err := open(ctx, t.Session, t.Cwd)
		if err != nil {
			if t.Session != "" {
				err = fmt.Errorf("%w: %w", ErrBusy, err)
			}
			return t.Session, nil, err
		}
		defer closeSession()
		sid := rt.Manager().GetSessionID()
		if t.Model != "" {
			provider, id, _ := strings.Cut(t.Model, "/")
			models := rt.AvailableModels()
			i := slices.IndexFunc(models, func(m ai.Model) bool { return string(m.Provider) == provider && m.ID == id })
			if i < 0 {
				return sid, nil, fmt.Errorf("model %q is not available", t.Model)
			}
			if err := rt.SetModel(ctx, models[i]); err != nil {
				return sid, nil, err
			}
		}
		ended := make(chan error, 1)
		go func() {
			err := rt.Prompt(ctx, t.Prompt)
			if err == nil {
				err = rt.WaitForIdle(ctx)
			}
			ended <- err
		}()
		select {
		case err = <-ended:
		case <-ctx.Done():
			rt.Abort()
			err = <-ended
		}
		return sid, rt.State().Messages, err
	}
}

// inPlace runs text in a session open in this process once it is idle, as a
// person's next message would.
func (s *Scheduler) inPlace(ctx context.Context, l *live, sid, mark, text string, started time.Time) (string, string) {
	for !l.ctx.IsIdle() {
		if s.now().Sub(started) >= busyFor {
			return skipped, "busy"
		}
		select {
		case <-ctx.Done():
			return interrupted, "its holder stopped"
		case <-s.o.After(time.Minute):
		}
	}
	if !s.active(s.now()) {
		return skipped, "outside active hours"
	}
	wait := make(chan engine.AgentMessages, 1)
	s.mu.Lock()
	if l.waits == nil {
		s.mu.Unlock()
		return interrupted, "the session closed"
	}
	l.waits[mark] = wait
	s.placed[sid]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(l.waits, mark)
		delete(l.ended, mark)
		if s.placed[sid]--; s.placed[sid] == 0 {
			delete(s.placed, sid)
		}
		s.mu.Unlock()
	}()
	// An idle session runs the turn inside SendUserMessage, so the timeout runs alongside it.
	sent := make(chan error, 1)
	go func() {
		sent <- l.api.SendUserMessage(ctx, ai.NewUserText(text), &extensions.SendUserMessageOptions{DeliverAs: extensions.DeliverFollowUp, ExpandPromptTemplates: true})
	}()
	timeout := s.o.After(s.o.Timeout)
	for {
		select {
		case err := <-sent:
			if err != nil {
				return failed, err.Error()
			}
			sent = nil
		case messages, ok := <-wait:
			if !ok {
				return interrupted, "the session closed"
			}
			return outcome(messages)
		case <-timeout:
			l.ctx.Abort()
			if sent != nil {
				<-sent
			}
			return failed, "timeout"
		case <-ctx.Done():
			return interrupted, "its holder stopped"
		}
	}
}

// outcome reads how a run's turn ended from its last assistant message.
func outcome(messages engine.AgentMessages) (string, string) {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(*ai.AssistantMessage)
		if !ok {
			continue
		}
		if m.StopReason == ai.StopReasonError || m.StopReason == ai.StopReasonAborted {
			if m.ErrorMessage != nil {
				return failed, *m.ErrorMessage
			}
			return failed, string(m.StopReason)
		}
		if strings.TrimSpace(ai.ContentText(m.Content)) == "NO_REPLY" {
			return quiet, ""
		}
		break
	}
	return done, ""
}

func decode(b []byte) (*doc, error) {
	d := &doc{Version: 1}
	if len(b) > 0 {
		if err := json.Unmarshal(b, d); err != nil {
			return nil, fmt.Errorf("schedule.json: %w", err)
		}
	}
	if d.Holders == nil {
		d.Holders = map[string]holder{}
	}
	return d, nil
}

func encode(d *doc) ([]byte, error) { return json.MarshalIndent(d, "", " ") }

func randomID(n int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

// rule is a parsed when: one instant, an interval, or a cron spec.
type rule struct {
	at    time.Time
	every time.Duration
	cron  *cron
	loc   *time.Location
}

func oneShot(when string) bool {
	return strings.HasPrefix(when, "in ") || strings.HasPrefix(when, "at ")
}

func parse(when string, loc *time.Location, now time.Time) (rule, error) {
	kind, rest, _ := strings.Cut(strings.TrimSpace(when), " ")
	rest = strings.TrimSpace(rest)
	r := rule{loc: loc}
	switch kind {
	case "in":
		d, err := time.ParseDuration(rest)
		if err != nil || d <= 0 {
			return r, fmt.Errorf("'in' takes a duration such as 20m or 2h, not %q", rest)
		}
		r.at = now.Add(d)
	case "at":
		t, err := time.ParseInLocation("2006-01-02 15:04", rest, loc)
		if err != nil {
			if t, err = time.Parse(time.RFC3339, rest); err != nil {
				return r, fmt.Errorf("'at' takes 2006-01-02 15:04 or RFC 3339, not %q", rest)
			}
		}
		if !t.After(now) {
			return r, fmt.Errorf("%s is past", rest)
		}
		r.at = t.UTC()
	case "every":
		d, err := time.ParseDuration(rest)
		if err != nil || d < minGap {
			return r, fmt.Errorf("'every' takes a duration of 5m or more, not %q", rest)
		}
		r.every = d
	case "cron":
		c, err := parseCron(rest)
		if err != nil {
			return r, err
		}
		if _, ok := c.after(now, loc); !ok {
			return r, fmt.Errorf("cron %q never fires", rest)
		}
		r.cron = c
	default:
		return r, fmt.Errorf("when is 'in 20m', 'at 2026-11-08 08:30', 'every 2h' or 'cron 30 8 * * 1-5', not %q", when)
	}
	return r, nil
}

func (r rule) first(now time.Time) time.Time {
	switch {
	case r.every > 0:
		return now.Add(r.every)
	case r.cron != nil:
		t, _ := r.cron.after(now, r.loc)
		return t
	}
	return r.at
}

// due returns the latest occurrence from next up to now and the one after it;
// recurring is false for a one-shot, whose only occurrence is next.
func (r rule) due(next, now time.Time) (latest, after time.Time, recurring bool) {
	switch {
	case r.every > 0:
		latest = next.Add(now.Sub(next) / r.every * r.every)
		return latest, latest.Add(r.every), true
	case r.cron != nil:
		latest = next
		for {
			t, ok := r.cron.after(latest, r.loc)
			if !ok || t.After(now) {
				return latest, t, ok
			}
			latest = t
		}
	}
	return next, time.Time{}, false
}

// cron is a 5-field spec as bit sets; a day matches as in Vixie cron.
type cron struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

func parseCron(spec string) (*cron, error) {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q needs 5 fields: minute hour day-of-month month day-of-week", spec)
	}
	c := &cron{domStar: strings.HasPrefix(f[2], "*"), dowStar: strings.HasPrefix(f[4], "*")}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	sets := [5]*uint64{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, field := range f {
		bits, err := parseField(field, bounds[i][0], bounds[i][1])
		if err != nil {
			return nil, fmt.Errorf("cron %q: %w", spec, err)
		}
		*sets[i] = bits
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	// Two minutes of one hour, or across consecutive hours, under 5 min apart.
	var minutes []int
	for m := range 60 {
		if c.minute&(1<<m) != 0 {
			minutes = append(minutes, m)
		}
	}
	dense := false
	for i := 1; i < len(minutes); i++ {
		dense = dense || minutes[i]-minutes[i-1] < 5
	}
	for h := range 24 {
		if c.hour&(1<<h) != 0 && c.hour&(1<<((h+1)%24)) != 0 {
			dense = dense || minutes[0]+60-minutes[len(minutes)-1] < 5
		}
	}
	if dense {
		return nil, fmt.Errorf("cron %q fires less than 5 min apart", spec)
	}
	return c, nil
}

func parseField(field string, lo, hi int) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(field, ",") {
		span, stepText, stepped := strings.Cut(part, "/")
		step := 1
		if stepped {
			var err error
			if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
				return 0, fmt.Errorf("bad step %q", part)
			}
		}
		a, b := lo, hi
		if span != "*" {
			first, last, ranged := strings.Cut(span, "-")
			var errA, errB error
			a, errA = strconv.Atoi(first)
			b, errB = a, nil
			if ranged {
				b, errB = strconv.Atoi(last)
			} else if stepped {
				b = hi
			}
			if errA != nil || errB != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func (c *cron) day(t time.Time) bool {
	dom, dow := c.dom&(1<<t.Day()) != 0, c.dow&(1<<int(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}

// after returns the first occurrence after t, within 8 years (a 29 February
// on a given weekday comes back at least that often); wall times a
// daylight-saving jump skips never occur.
func (c *cron) after(t time.Time, loc *time.Location) (time.Time, bool) {
	t = t.In(loc)
	for i := range 8 * 366 {
		date := time.Date(t.Year(), t.Month(), t.Day()+i, 0, 0, 0, 0, loc)
		if c.month&(1<<int(date.Month())) == 0 || !c.day(date) {
			continue
		}
		for h := range 24 {
			if c.hour&(1<<h) == 0 {
				continue
			}
			for m := range 60 {
				if c.minute&(1<<m) == 0 {
					continue
				}
				at := time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, loc)
				if at.Hour() == h && at.Minute() == m && at.After(t) {
					return at.UTC(), true
				}
			}
		}
	}
	return time.Time{}, false
}
