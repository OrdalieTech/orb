package schedule_test

// The plugin's contract, tested from outside: only New, Extension, Tool, Sessions, Run and schedule.json. A scripted
// model drives real sessions, a hand-moved clock drives the timer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/plugins/schedule"
)

// clock is the timer's time: After waits until advance passes its deadline.
type clock struct {
	mu    sync.Mutex
	now   time.Time
	waits []wait
	calls int // After calls so far: a woken timer arms its next wait
}

type wait struct {
	at time.Time
	ch chan time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waits = append(c.waits, wait{c.now.Add(d), ch})
	return ch
}

// advance moves time on by d in one-minute steps, firing what falls due, as a minute's sleep would.
func (c *clock) advance(d time.Duration) {
	for d > 0 {
		step := min(d, time.Minute)
		d -= step
		c.mu.Lock()
		c.now = c.now.Add(step)
		var kept []wait
		fired := 0
		for _, w := range c.waits {
			if !w.at.After(c.now) {
				w.ch <- c.now
				fired++
			} else {
				kept = append(kept, w)
			}
		}
		c.waits = kept
		calls := c.calls
		c.mu.Unlock()
		// Time moves on only once what woke has armed its next wait, as it would in a minute of sleep.
		for deadline := time.Now().Add(2 * time.Second); fired > 0 && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			c.mu.Lock()
			armed := c.calls > calls
			c.mu.Unlock()
			if armed {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// file is schedule.json as the API announces it.
type file struct {
	Version   int                    `json:"version"`
	Tasks     []task                 `json:"tasks"`
	Runs      []run                  `json:"runs"`
	Holders   map[string]holder      `json:"holders"`
	AgentRuns []string               `json:"agentRuns"`
	Extra     map[string]interface{} `json:"-"`
}

type task struct {
	ID, Title, Prompt, When, Zone, Target, Session, Cwd, Model, By string
	Enabled                                                        bool
	Next                                                           time.Time
	Failures                                                       int
}

type run struct {
	Task, Holder, Status, Session, Detail string
	At                                    time.Time
}

type holder struct {
	Name string
	Seen time.Time
}

// env is one agent: its store, its sessions on a scripted model, its scheduler.
type env struct {
	t        *testing.T
	dir      string
	store    filelock.File
	clock    *clock
	provider *faux.Provider
	settings *config.SettingsManager
	skill    agent.Skill // the one skill sessions have

	mu      sync.Mutex
	prompts []string // what each model request's last user message said
	opens   []string // the sessions Open was asked for ("" = new)
	openErr error
}

func newEnv(t *testing.T, start time.Time) *env {
	t.Helper()
	dir := t.TempDir()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"retry":{"enabled":false},"compaction":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(dir, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, settings: settings, clock: &clock{now: start}, provider: faux.New()}
	e.skill = agent.Skill{Name: "brief", Description: "Brief the team", FilePath: filepath.Join(dir, "SKILL.md"), BaseDir: dir}
	if err := os.WriteFile(e.skill.FilePath, []byte("---\nname: brief\ndescription: Brief the team\n---\nSummarize the day for the team.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.store = filelock.File{Path: filepath.Join(agentDir, "schedule.json"), Perm: 0o600, Atomic: true}
	return e
}

// scheduler is a holder of the store: a process with Run, or (open nil) one that only has the tool.
func (e *env) scheduler(name string, zone *time.Location, mod func(*schedule.Options)) *schedule.Scheduler {
	o := schedule.Options{Store: e.store, Zone: zone, Now: e.clock.Now, After: e.clock.After, Name: name}
	var s *schedule.Scheduler
	o.Run = schedule.Sessions(func(ctx context.Context, session, cwd string) (*agent.AgentSession, func(), error) {
		e.mu.Lock()
		e.opens = append(e.opens, session)
		err := e.openErr
		e.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		rt := e.session(s)
		return rt, rt.Dispose, nil
	})
	if mod != nil {
		mod(&o)
	}
	s = schedule.New(o)
	return s
}

func (e *env) run(s *schedule.Scheduler) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	e.t.Cleanup(func() { cancel(); <-done })
	return cancel
}

// session is a conversation on the scripted model with the plugin loaded, as a host opens one.
func (e *env) session(s *schedule.Scheduler) *agent.AgentSession {
	e.t.Helper()
	manager, err := sessionstore.InMemory(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	registry := extensions.NewRegistry(e.dir)
	if err := registry.Register("schedule", s.Extension()); err != nil {
		e.t.Fatal(err)
	}
	stream := func(ctx context.Context, model *ai.Model, c ai.Context, o *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return e.provider.StreamSimple(ctx, model, c, o)
	}
	created := engine.NewAgent(stream, engine.WithInitialState(engine.AgentState{SystemPrompt: "test", Model: e.provider.GetModel()}), engine.WithConvertToLLM(agent.ConvertToLLM))
	rt, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: e.settings, StreamFn: stream, ExtensionRegistry: registry, SlashResolver: &agent.SlashResolver{Skills: []agent.Skill{e.skill}}})
	if err != nil {
		e.t.Fatal(err)
	}
	rt.RefreshContext()
	return rt
}

// answer queues what the model says next: each step is text, or a call of the schedule tool.
func (e *env) answer(steps ...any) {
	for _, s := range steps {
		switch v := s.(type) {
		case string:
			e.provider.AppendResponses([]faux.ResponseStep{e.capture(faux.AssistantMessage(v))})
		case map[string]any:
			e.provider.AppendResponses([]faux.ResponseStep{e.capture(faux.AssistantMessage(faux.ToolCall("schedule", v), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}))})
		}
	}
}

func (e *env) capture(m *ai.AssistantMessage) faux.ResponseStep {
	return faux.Factory(func(_ context.Context, c ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		e.mu.Lock()
		e.prompts = append(e.prompts, lastUser(c))
		e.mu.Unlock()
		return m, nil
	})
}

func lastUser(c ai.Context) string {
	b, _ := json.Marshal(c.Messages)
	s := string(b)
	i := strings.LastIndex(s, `"role":"user"`)
	if i < 0 {
		return ""
	}
	return s[i:]
}

// person has someone ask, in a session of its own, for the tool calls given; returns the tool's results.
func (e *env) person(s *schedule.Scheduler, calls ...map[string]any) []string {
	e.t.Helper()
	rt := e.session(s)
	defer rt.Dispose()
	for _, c := range calls {
		e.answer(c)
	}
	e.answer("ok")
	if err := rt.Prompt(context.Background(), "please"); err != nil {
		e.t.Fatal(err)
	}
	_ = rt.WaitForIdle(context.Background())
	var out []string
	for _, m := range rt.State().Messages {
		if r, ok := m.(*ai.ToolResultMessage); ok {
			b, _ := json.Marshal(r)
			out = append(out, string(b))
		}
	}
	return out
}

func (e *env) read() file {
	e.t.Helper()
	b, err := e.store.Read(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var f file
	if len(b) > 0 {
		if err := json.Unmarshal(b, &f); err != nil {
			e.t.Fatalf("schedule.json: %v\n%s", err, b)
		}
	}
	return f
}

// eventually waits (real time) for the store to satisfy ok.
func (e *env) eventually(what string, ok func(file) bool) file {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f := e.read()
		if ok(f) {
			return f
		}
		if time.Now().After(deadline) {
			b, _ := json.MarshalIndent(f, "", " ")
			e.t.Fatalf("%s: never\n%s", what, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runsOf(f file, id string) []run {
	var out []run
	for _, r := range f.Runs {
		if r.Task == id {
			out = append(out, r)
		}
	}
	return out
}

func taskID(t *testing.T, f file, title string) string {
	for _, k := range f.Tasks {
		if k.Title == title {
			return k.ID
		}
	}
	t.Fatalf("no task %q in %+v", title, f.Tasks)
	return ""
}

func refused(results []string) bool {
	return len(results) > 0 && strings.Contains(results[len(results)-1], `"isError":true`)
}

var paris, _ = time.LoadLocation("Europe/Paris")

// 2026-11-09 is a Monday; Paris is UTC+1 then.
var monday = time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)

func TestCreateByPersonIsStoredWithItsNextRun(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "Sales recap", "prompt": "/skill:sales-recap", "when": "cron 0 20 * * *"})
	f := e.read()
	if len(f.Tasks) != 1 {
		t.Fatalf("tasks %+v", f.Tasks)
	}
	k := f.Tasks[0]
	if k.By != "person" || !k.Enabled || k.Zone != "Europe/Paris" || k.Target != "new" {
		t.Fatalf("task %+v", k)
	}
	if want := time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC); !k.Next.Equal(want) {
		t.Fatalf("next %v, want %v", k.Next, want)
	}
}

func TestOneShotDefaultsToThisAndRecurringToNew(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s,
		map[string]any{"action": "create", "title": "once", "prompt": "check", "when": "in 20m"},
		map[string]any{"action": "create", "title": "often", "prompt": "check", "when": "every 2h"})
	f := e.read()
	for _, k := range f.Tasks {
		if want := map[string]string{"once": "this", "often": "new"}[k.Title]; k.Target != want {
			t.Fatalf("%s target %q, want %q", k.Title, k.Target, want)
		}
	}
	if k := f.Tasks[0]; k.Title == "once" && k.Session == "" {
		t.Fatalf("a one-shot on this names its session: %+v", k)
	}
}

func TestFiresAtItsTimeWithTheMarkerAfterThePrompt(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "brief", "prompt": "/skill:brief go", "when": "in 20m", "target": "new"})
	id := taskID(t, e.read(), "brief")
	e.run(s)
	e.answer("all good")
	e.clock.advance(19 * time.Minute)
	if rs := runsOf(e.read(), id); len(rs) != 0 {
		t.Fatalf("fired early: %+v", rs)
	}
	e.clock.advance(2 * time.Minute)
	f := e.eventually("run done", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "done" })
	for _, k := range f.Tasks {
		if k.ID == id {
			t.Fatalf("a one-shot that fired stays in tasks: %+v", k)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	last := e.prompts[len(e.prompts)-1]
	if !strings.Contains(last, "[scheduled · brief · "+id+"]") {
		t.Fatalf("the run's prompt lacks its marker: %s", last)
	}
	if i := strings.Index(last, "Summarize the day"); i < 0 || i > strings.Index(last, "[scheduled") {
		t.Fatalf("the skill is not expanded before the marker: %s", last)
	}
}

func TestABareSkillStillExpands(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "brief", "prompt": "/skill:brief", "when": "in 20m", "target": "new"})
	id := taskID(t, e.read(), "brief")
	e.run(s)
	e.answer("all good")
	e.clock.advance(21 * time.Minute)
	e.eventually("run done", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "done" })
	e.mu.Lock()
	defer e.mu.Unlock()
	if last := e.prompts[len(e.prompts)-1]; !strings.Contains(last, "Summarize the day") || !strings.Contains(last, "[scheduled · brief · "+id+"]") {
		t.Fatalf("a bare skill was not expanded with its marker: %s", last)
	}
}

func TestNoReplyIsQuiet(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "watch", "prompt": "look, say NO_REPLY if nothing", "when": "every 1h"})
	id := taskID(t, e.read(), "watch")
	e.run(s)
	e.answer("  NO_REPLY \n")
	e.clock.advance(61 * time.Minute)
	e.eventually("quiet", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "quiet" })
}

func TestTwoHoldersFireAnOccurrenceOnce(t *testing.T) {
	e := newEnv(t, monday)
	a := e.scheduler("orb chat", paris, nil)
	b := e.scheduler("bridge", paris, nil)
	e.person(a, map[string]any{"action": "create", "title": "dup", "prompt": "x", "when": "every 1h"})
	id := taskID(t, e.read(), "dup")
	e.run(a)
	e.run(b)
	e.answer("one", "two")
	e.clock.advance(61 * time.Minute)
	time.Sleep(200 * time.Millisecond)
	f := e.eventually("one run", func(f file) bool { return len(runsOf(f, id)) >= 1 })
	if rs := runsOf(f, id); len(rs) != 1 {
		t.Fatalf("one occurrence fired %d times: %+v", len(rs), rs)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 1 {
		t.Fatalf("opened %d sessions for one occurrence", len(e.opens))
	}
}

func TestACrashedRunIsInterruptedNeverReplayed(t *testing.T) {
	e := newEnv(t, monday)
	seed := fmt.Sprintf(`{"version":1,"tasks":[{"id":"t1","title":"x","prompt":"x","when":"every 1h","zone":"Europe/Paris","target":"new","by":"person","enabled":true,"created":%q,"next":%q,"failures":0}],
 "runs":[{"task":"t1","at":%q,"holder":"orb chat#dead","status":"running","started":%q}],
 "holders":{"dead":{"name":"orb chat","seen":%q}}}`,
		monday.Add(-2*time.Hour).Format(time.RFC3339), monday.Add(30*time.Minute).Format(time.RFC3339),
		monday.Add(-30*time.Minute).Format(time.RFC3339), monday.Add(-30*time.Minute).Format(time.RFC3339), monday.Add(-4*time.Minute).Format(time.RFC3339))
	if err := filelock.File(e.store).Update(context.Background(), func([]byte) ([]byte, error) { return []byte(seed), nil }); err != nil {
		t.Fatal(err)
	}
	s := e.scheduler("orb chat", paris, nil)
	e.run(s)
	e.clock.advance(time.Minute)
	f := e.eventually("interrupted", func(f file) bool { rs := runsOf(f, "t1"); return len(rs) == 1 && rs[0].Status == "interrupted" })
	if rs := runsOf(f, "t1"); len(rs) != 1 {
		t.Fatalf("replayed: %+v", rs)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 0 {
		t.Fatalf("an interrupted run was replayed")
	}
}

func TestOnlyTheLatestMissedOccurrenceFires(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "daily", "prompt": "x", "when": "cron 30 8 * * *"})
	id := taskID(t, e.read(), "daily")
	// Three days down, then back.
	e.clock.advance(0)
	e.clock.mu.Lock()
	e.clock.now = e.clock.now.Add(72 * time.Hour)
	e.clock.mu.Unlock()
	e.answer("caught up")
	e.run(s)
	e.clock.advance(time.Minute)
	e.eventually("one run", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "done" })
	time.Sleep(100 * time.Millisecond)
	f := e.read()
	if rs := runsOf(f, id); len(rs) != 1 {
		t.Fatalf("backlog replayed: %+v", rs)
	}
	for _, k := range f.Tasks {
		if k.ID == id && !k.Next.After(e.clock.Now()) {
			t.Fatalf("next %v is not in the future", k.Next)
		}
	}
}

func TestCronFollowsTheZoneAcrossDaylightSaving(t *testing.T) {
	// Paris leaves summer time on 2026-10-25: 08:30 is 06:30Z before, 07:30Z after.
	e := newEnv(t, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	s := e.scheduler("orb chat", paris, nil)
	e.person(s,
		map[string]any{"action": "create", "title": "cron", "prompt": "x", "when": "cron 30 8 * * *"},
		map[string]any{"action": "create", "title": "every", "prompt": "x", "when": "every 24h"})
	f := e.read()
	var cron, every task
	for _, k := range f.Tasks {
		if k.Title == "cron" {
			cron = k
		} else {
			every = k
		}
	}
	if want := time.Date(2026, 10, 25, 7, 30, 0, 0, time.UTC); !cron.Next.Equal(want) {
		t.Fatalf("cron next %v, want %v (08:30 Paris, winter time)", cron.Next, want)
	}
	if want := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC); !every.Next.Equal(want) {
		t.Fatalf("every next %v, want %v (24 h of elapsed time)", every.Next, want)
	}
}

func TestInputsRefusedWriteNothing(t *testing.T) {
	cases := map[string]map[string]any{
		"past at":        {"when": "at 2026-11-01 08:00"},
		"unknown zone":   {"when": "in 20m", "zone": "Mars/Olympus"},
		"every under 5m": {"when": "every 4m"},
		"invalid cron":   {"when": "cron 61 * * * *"},
		"never cron":     {"when": "cron 0 0 31 2 *"},
		"dense cron":     {"when": "cron */2 * * * *"},
		"empty prompt":   {"when": "in 20m", "prompt": ""},
		"empty title":    {"when": "in 20m", "title": ""},
		"model on this":  {"when": "in 20m", "target": "this", "model": "cheap"},
		"garbage when":   {"when": "tomorrow-ish"},
	}
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, monday)
			s := e.scheduler("orb chat", paris, nil)
			call := map[string]any{"action": "create", "title": "t", "prompt": "p"}
			for k, v := range over {
				call[k] = v
			}
			if !refused(e.person(s, call)) {
				t.Fatalf("%s accepted", name)
			}
			if f := e.read(); len(f.Tasks) != 0 {
				t.Fatalf("%s wrote %+v", name, f.Tasks)
			}
		})
	}
	t.Run("unknown id", func(t *testing.T) {
		e := newEnv(t, monday)
		s := e.scheduler("orb chat", paris, nil)
		if !refused(e.person(s, map[string]any{"action": "delete", "id": "nope"})) || !refused(e.person(s, map[string]any{"action": "update", "id": "nope", "enabled": false})) {
			t.Fatal("an unknown id is not refused")
		}
	})
}

func TestTwentyEnabledTasksAtMost(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	var calls []map[string]any
	for i := range 21 {
		calls = append(calls, map[string]any{"action": "create", "title": fmt.Sprint("t", i), "prompt": "p", "when": "every 1h"})
	}
	results := e.person(s, calls...)
	if !refused(results) {
		t.Fatal("the 21st task was accepted")
	}
	if f := e.read(); len(f.Tasks) != 20 {
		t.Fatalf("%d tasks", len(f.Tasks))
	}
}

// agentRun has a scheduled run create tasks: the plugin, not the model, marks them by:"agent".
func agentRun(t *testing.T, e *env, s *schedule.Scheduler, calls ...map[string]any) file {
	e.person(s, map[string]any{"action": "create", "title": "self", "prompt": "plan", "when": "in 10m", "target": "new"})
	id := taskID(t, e.read(), "self")
	e.run(s)
	steps := []any{}
	for _, c := range calls {
		steps = append(steps, c)
	}
	e.answer(append(steps, "planned")...)
	e.clock.advance(11 * time.Minute)
	return e.eventually("the scheduled run ends", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status != "running" })
}

func TestTasksCreatedInAScheduledRunAreTheAgents(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	f := agentRun(t, e, s, map[string]any{"action": "create", "title": "follow up", "prompt": "check", "when": "in 30m", "by": "person"})
	for _, k := range f.Tasks {
		if k.Title == "follow up" {
			if k.By != "agent" {
				t.Fatalf("a task created in a scheduled run is %q, and the model chose it", k.By)
			}
			return
		}
	}
	t.Fatalf("the task the scheduled run created is missing: %+v", f.Tasks)
}

func TestTheAgentHasFiveTasksAtMost(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	var calls []map[string]any
	for i := range 6 {
		calls = append(calls, map[string]any{"action": "create", "title": fmt.Sprint("a", i), "prompt": "p", "when": "every 1h"})
	}
	f := agentRun(t, e, s, calls...)
	n := 0
	for _, k := range f.Tasks {
		if k.By == "agent" {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("the agent has %d tasks", n)
	}
}

func TestTheAgentCannotWakeItselfWithinFiveMinutes(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	f := agentRun(t, e, s, map[string]any{"action": "create", "title": "soon", "prompt": "again", "when": "in 1m"})
	for _, k := range f.Tasks {
		if k.Title == "soon" {
			t.Fatalf("the agent woke itself in 1 min: %+v", k)
		}
	}
}

func TestFiveFailuresInARowPause(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "flaky", "prompt": "x", "when": "every 10m"})
	id := taskID(t, e.read(), "flaky")
	e.mu.Lock()
	e.openErr = errors.New("no session")
	e.mu.Unlock()
	e.run(s)
	for range 6 {
		e.clock.advance(10 * time.Minute)
	}
	f := e.eventually("paused", func(f file) bool {
		for _, k := range f.Tasks {
			if k.ID == id {
				return !k.Enabled
			}
		}
		return false
	})
	if rs := runsOf(f, id); len(rs) != 5 {
		t.Fatalf("%d runs before the pause, want 5: %+v", len(rs), rs)
	}
}

func TestOutsideActiveHoursIsSkipped(t *testing.T) {
	e := newEnv(t, time.Date(2026, 11, 9, 22, 0, 0, 0, time.UTC)) // 23:00 Paris
	s := e.scheduler("orb chat", paris, func(o *schedule.Options) { o.ActiveHours = "08:00-22:00" })
	e.person(s, map[string]any{"action": "create", "title": "night", "prompt": "x", "when": "every 1h"})
	id := taskID(t, e.read(), "night")
	e.run(s)
	e.clock.advance(61 * time.Minute)
	e.eventually("skipped", func(f file) bool {
		rs := runsOf(f, id)
		return len(rs) == 1 && rs[0].Status == "skipped" && strings.Contains(rs[0].Detail, "active hours")
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 0 {
		t.Fatal("a skipped run opened a session")
	}
}

func TestATurnPastTheTimeoutFails(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, func(o *schedule.Options) { o.Timeout = 2 * time.Minute })
	e.person(s, map[string]any{"action": "create", "title": "slow", "prompt": "x", "when": "in 10m", "target": "new"})
	id := taskID(t, e.read(), "slow")
	e.run(s)
	// The model never answers until its turn is aborted.
	e.provider.AppendResponses([]faux.ResponseStep{faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	e.clock.advance(11 * time.Minute)
	e.eventually("running", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "running" })
	time.Sleep(100 * time.Millisecond) // the turn has begun and armed its timeout
	e.clock.advance(3 * time.Minute)
	e.eventually("timeout", func(f file) bool {
		rs := runsOf(f, id)
		return len(rs) == 1 && rs[0].Status == "failed" && strings.Contains(rs[0].Detail, "timeout")
	})
}

func TestBusyThisIsSkippedAfterAnHour(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "later", "prompt": "x", "when": "in 10m"}) // target this, its session gone
	id := taskID(t, e.read(), "later")
	e.mu.Lock()
	e.openErr = errors.New("held by another process")
	e.mu.Unlock()
	e.run(s)
	e.clock.advance(11 * time.Minute)
	if rs := runsOf(e.read(), id); len(rs) == 1 && rs[0].Status == "failed" {
		t.Fatalf("a busy this failed at once instead of waiting: %+v", rs)
	}
	e.clock.advance(61 * time.Minute)
	e.eventually("skipped busy", func(f file) bool {
		rs := runsOf(f, id)
		return len(rs) == 1 && rs[0].Status == "skipped" && strings.Contains(rs[0].Detail, "busy")
	})
}

func TestToolWithoutAHolderSaysNothingFires(t *testing.T) {
	e := newEnv(t, monday)
	s := schedule.New(schedule.Options{Store: e.store, Zone: paris, Now: e.clock.Now})
	results := e.person(s, map[string]any{"action": "create", "title": "t", "prompt": "p", "when": "in 20m"})
	if len(results) == 0 || !strings.Contains(strings.ToLower(results[0]), "fire") {
		t.Fatalf("a tool with no holder does not say nothing fires here: %v", results)
	}
}

func TestTheAgentCannotLoosenAPersonsTask(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "daily", "prompt": "x", "when": "cron 0 9 * * *"})
	id := taskID(t, e.read(), "daily")
	// A scheduled run tries to make the person's daily task fire every 5 min, outside the agent's caps.
	f := agentRun(t, e, s, map[string]any{"action": "update", "id": id, "when": "every 5m"})
	for _, k := range f.Tasks {
		if k.ID == id && k.When != "cron 0 9 * * *" {
			t.Fatalf("a scheduled run rewrote a person's task: %+v", k)
		}
	}
}

func TestTheAgentMayPauseAPersonsTask(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	e.person(s, map[string]any{"action": "create", "title": "daily", "prompt": "x", "when": "cron 0 9 * * *"})
	id := taskID(t, e.read(), "daily")
	f := agentRun(t, e, s, map[string]any{"action": "update", "id": id, "enabled": false})
	for _, k := range f.Tasks {
		if k.ID == id && k.Enabled {
			t.Fatalf("a scheduled run could not pause a task: %+v", k)
		}
	}
}

func TestATurnPastTheTimeoutFailsInPlace(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, func(o *schedule.Options) { o.Timeout = 2 * time.Minute })
	// The person's session stays open, so the run is delivered in place.
	rt := e.session(s)
	t.Cleanup(rt.Dispose)
	e.answer(map[string]any{"action": "create", "title": "slow", "prompt": "x", "when": "in 10m"}, "ok")
	if err := rt.Prompt(context.Background(), "please"); err != nil {
		t.Fatal(err)
	}
	_ = rt.WaitForIdle(context.Background())
	id := taskID(t, e.read(), "slow")
	e.run(s)
	e.provider.AppendResponses([]faux.ResponseStep{faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	e.clock.advance(11 * time.Minute)
	e.eventually("running", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "running" })
	time.Sleep(100 * time.Millisecond)
	e.clock.advance(3 * time.Minute)
	e.eventually("timeout", func(f file) bool {
		rs := runsOf(f, id)
		return len(rs) == 1 && rs[0].Status == "failed" && strings.Contains(rs[0].Detail, "timeout")
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 0 {
		t.Fatalf("the run opened a session instead of running in place: %v", e.opens)
	}
}

func TestACatchUpOutsideActiveHoursIsSkipped(t *testing.T) {
	e := newEnv(t, monday) // 08:00 Paris
	s := e.scheduler("orb chat", paris, func(o *schedule.Options) { o.ActiveHours = "08:00-22:00" })
	e.person(s, map[string]any{"action": "create", "title": "morning", "prompt": "x", "when": "in 30m", "target": "new"})
	id := taskID(t, e.read(), "morning")
	// The holder was down all day and restarts at 23:00 Paris.
	e.clock.mu.Lock()
	e.clock.now = e.clock.now.Add(15 * time.Hour)
	e.clock.mu.Unlock()
	e.run(s)
	e.eventually("skipped", func(f file) bool {
		rs := runsOf(f, id)
		return len(rs) == 1 && rs[0].Status == "skipped" && strings.Contains(rs[0].Detail, "active hours")
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 0 {
		t.Fatal("a run caught up outside active hours opened a session")
	}
}

func TestTrimmingRunsKeepsTheRunning(t *testing.T) {
	e := newEnv(t, monday)
	runs := []map[string]any{{"task": "long", "at": monday, "holder": "other#h", "status": "running", "started": monday}}
	for i := range 60 {
		at := monday.Add(time.Duration(i) * time.Second)
		runs = append(runs, map[string]any{"task": "short", "at": at, "holder": "other#h", "status": "done", "started": at, "ended": at})
	}
	b, err := json.Marshal(map[string]any{"version": 1, "runs": runs, "holders": map[string]any{"h": map[string]any{"name": "other", "seen": monday}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.store.Path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	e.run(e.scheduler("orb chat", paris, nil))
	f := e.eventually("trimmed", func(f file) bool { return len(f.Runs) < len(runs) })
	if rs := runsOf(f, "long"); len(rs) != 1 || rs[0].Status != "running" {
		t.Fatalf("trimming dropped a running run: %+v", rs)
	}
	if len(f.Runs) != 50 {
		t.Fatalf("%d runs kept, want 50", len(f.Runs))
	}
}

func TestARetriedTurnInPlaceEndsWithItsRetry(t *testing.T) {
	e := newEnv(t, monday)
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.store.Path), "settings.json"), []byte(`{"retry":{"enabled":true,"baseDelayMs":1},"compaction":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e.settings.Reload()
	s := e.scheduler("orb chat", paris, nil)
	rt := e.session(s)
	t.Cleanup(rt.Dispose)
	e.answer(map[string]any{"action": "create", "title": "check", "prompt": "x", "when": "in 10m"}, "ok")
	if err := rt.Prompt(context.Background(), "please"); err != nil {
		t.Fatal(err)
	}
	_ = rt.WaitForIdle(context.Background())
	id := taskID(t, e.read(), "check")
	e.run(s)
	overloaded := "overloaded"
	e.provider.AppendResponses([]faux.ResponseStep{faux.AssistantMessage("", faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &overloaded})})
	// The retry is still the scheduled run, so the task it creates is the agent's.
	e.answer(map[string]any{"action": "create", "title": "again", "prompt": "y", "when": "in 1h"}, "done")
	e.clock.advance(11 * time.Minute)
	f := e.eventually("done", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status != "running" })
	if rs := runsOf(f, id); rs[0].Status != "done" {
		t.Fatalf("a run that recovered on retry ended %s: %+v", rs[0].Status, rs[0])
	}
	if i := slices.IndexFunc(f.Tasks, func(k task) bool { return k.Title == "again" }); i < 0 || f.Tasks[i].By != "agent" {
		t.Fatalf("the retry did not create its task as the agent's: %+v", f.Tasks)
	}
}

func TestTasksCreatedInAnInPlaceRunAreTheAgents(t *testing.T) {
	e := newEnv(t, monday)
	s := e.scheduler("orb chat", paris, nil)
	rt := e.session(s)
	t.Cleanup(rt.Dispose)
	e.answer(map[string]any{"action": "create", "title": "self", "prompt": "plan", "when": "in 10m"}, "ok")
	if err := rt.Prompt(context.Background(), "please"); err != nil {
		t.Fatal(err)
	}
	_ = rt.WaitForIdle(context.Background())
	id := taskID(t, e.read(), "self")
	e.run(s)
	e.answer(map[string]any{"action": "create", "title": "follow up", "prompt": "check", "when": "in 30m"}, "planned")
	e.clock.advance(11 * time.Minute)
	f := e.eventually("the run ends", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "done" })
	if k := f.Tasks[slices.IndexFunc(f.Tasks, func(k task) bool { return k.Title == "follow up" })]; k.By != "agent" {
		t.Fatalf("a task created in a run in place is %q", k.By)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.opens) != 0 {
		t.Fatalf("the run opened a session instead of running in place: %v", e.opens)
	}
}

// A host without AgentSessions, such as a product on engine.Agent, has the tool and fires through Run alone.
func TestABareEngineAgentSchedulesAndFires(t *testing.T) {
	e := newEnv(t, monday)
	var s *schedule.Scheduler
	stream := func(ctx context.Context, model *ai.Model, c ai.Context, o *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return e.provider.StreamSimple(ctx, model, c, o)
	}
	var conversation *engine.Agent
	s = schedule.New(schedule.Options{Store: e.store, Zone: paris, Now: e.clock.Now, After: e.clock.After, Name: "product",
		Run: func(ctx context.Context, turn schedule.Turn) (string, engine.AgentMessages, error) {
			if turn.Session != "conv-1" {
				return "", nil, fmt.Errorf("run for session %q", turn.Session)
			}
			err := conversation.Prompt(ctx, turn.Prompt)
			if err == nil {
				err = conversation.WaitForIdle(ctx)
			}
			return turn.Session, conversation.State().Messages, err
		}})
	conversation = engine.NewAgent(stream, engine.WithInitialState(engine.AgentState{SystemPrompt: "test", Model: e.provider.GetModel(), Tools: []engine.AgentTool{s.Tool("conv-1", e.dir, nil)}}))
	e.answer(map[string]any{"action": "create", "title": "nudge", "prompt": "anything new?", "when": "in 20m"}, "ok")
	if err := conversation.Prompt(context.Background(), "nudge me in 20 minutes"); err != nil {
		t.Fatal(err)
	}
	_ = conversation.WaitForIdle(context.Background())
	f := e.read()
	if len(f.Tasks) != 1 || f.Tasks[0].Session != "conv-1" || f.Tasks[0].Target != "this" || f.Tasks[0].Cwd != e.dir || f.Tasks[0].By != "person" {
		t.Fatalf("tasks %+v", f.Tasks)
	}
	id := f.Tasks[0].ID
	e.run(s)
	e.answer(map[string]any{"action": "create", "title": "again", "prompt": "anything new?", "when": "in 1h"}, "NO_REPLY")
	e.clock.advance(21 * time.Minute)
	f = e.eventually("the run is quiet", func(f file) bool { rs := runsOf(f, id); return len(rs) == 1 && rs[0].Status == "quiet" })
	if rs := runsOf(f, id); rs[0].Session != "conv-1" {
		t.Fatalf("run %+v", rs[0])
	}
	if len(f.Tasks) != 1 || f.Tasks[0].Title != "again" || f.Tasks[0].By != "agent" {
		t.Fatalf("the task the run created: %+v", f.Tasks)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if last := e.prompts[len(e.prompts)-1]; !strings.Contains(last, "[scheduled · nudge · "+id+"]") {
		t.Fatalf("the run's prompt lacks its marker: %s", last)
	}
}
