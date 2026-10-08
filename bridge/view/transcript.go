package view

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/OrdalieTech/orb/agent/session/exporthtml"
)

// Row is what an app draws of a conversation, one per line of it: what someone said (you), a
// block of what Orb wrote (md), a run of what it did (run: thoughts and tool calls, folded into
// one line past a single action), or a note.
type Row struct {
	Key     string   `json:"k"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text,omitempty"`
	Via     string   `json:"via,omitempty"` // "bridge": a peer sent it, not this app
	Alarm   bool     `json:"alarm,omitempty"`
	Images  []string `json:"images,omitempty"` // references an app fetches with "image"
	Block   *Block   `json:"block,omitempty"`
	Live    bool     `json:"live,omitempty"`
	What    string   `json:"what,omitempty"` // a run in words: "2 thoughts · 3 commands"
	Now     string   `json:"now,omitempty"`  // what a run is doing now
	Failed  int      `json:"failed,omitempty"`
	Actions []Action `json:"actions,omitempty"`
}

// Action is one line of a run: a thought or a tool call, its target and what came of it. What
// it was given and returned in full comes with "detail".
type Action struct {
	Key    string `json:"k"`
	Verb   string `json:"verb"`
	Target string `json:"target"`
	Result string `json:"result,omitempty"`
	Live   bool   `json:"live,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// item is one thing a conversation holds: what someone said ('y'), what Orb said and thought
// ('s'), a tool it ran ('t'), or a note ('n').
type item struct {
	key, kind                          string
	text, via, thinking                string
	images                             []string
	live, failed, alarm                bool
	verb, target, args, result, output string
	blocks                             []Block
	parsed                             string // the text and state blocks were parsed for
}

// transcript folds Orb's agent events, live or replayed from a snapshot, into items. Keys restart
// with it, so a reload of the same messages keeps its rows.
type transcript struct {
	items               []*item
	said, retry         *item
	tools               map[string]*item
	n                   int
	sent                []string // texts this side sent: a user message not among them came by Bridge
	userOpen, replaying bool
	index, last         int
}

const outputKept = 8000

var (
	verbs   = map[string]string{"ask_user_question": "ask", "todo_write": "tasks", "web_search": "search", "web_fetch": "fetch"}
	kinds   = map[string]string{"bash": "command", "read": "read", "grep": "search", "find": "search", "glob": "search", "ls": "listing", "edit": "edit", "write": "edit"}
	tagged  = regexp.MustCompile(`^<([a-z][\w-]*)[ >]`)
	markup  = regexp.MustCompile(`<[^>]+>`)
	spaces  = regexp.MustCompile(`\s+`)
	status  = regexp.MustCompile(`^\d{3}`)
	message = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

func (t *transcript) clear() { *t = transcript{sent: t.sent} }

func (t *transcript) add(it *item) *item {
	t.items = append(t.items, t.keyed(it))
	return it
}

func (t *transcript) keyed(it *item) *item {
	it.key = fmt.Sprintf("i%d", t.n)
	t.n++
	return it
}

func (t *transcript) note(text string, alarm bool) *item {
	return t.add(&item{kind: "n", text: text, alarm: alarm})
}

// waiting says that a message sent during a run waits: steering lands after the current step,
// a follow-up after the run.
func (t *transcript) waiting(steer bool) {
	t.note(map[bool]string{true: "steering · lands after the current step", false: "queued · sends when this run ends"}[steer], false)
}

// shell is a `!command` this side ran: live until the conversation, reloaded, holds its output.
func (t *transcript) shell(command string) {
	t.add(&item{kind: "t", verb: "bash", target: firstLine(command, 120), args: command, live: true})
}

type wireEvent struct {
	Type          string          `json:"type"`
	Message       json.RawMessage `json:"message"`
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	PartialResult json.RawMessage `json:"partialResult"`
	Result        json.RawMessage `json:"result"`
	IsError       bool            `json:"isError"`
	ErrorMessage  string          `json:"errorMessage"`
	Aborted       bool            `json:"aborted"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"maxAttempts"`
	Success       bool            `json:"success"`
}

type wireMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	ToolCallID   string          `json:"toolCallId"`
	IsError      bool            `json:"isError"`
	Command      string          `json:"command"`
	Output       string          `json:"output"`
	ExitCode     int             `json:"exitCode"`
	Display      bool            `json:"display"`
	TokensBefore int             `json:"tokensBefore"`
	Summary      string          `json:"summary"`
}

// content is a message's or tool result's text, thinking, image references and tool calls.
type content struct {
	text, thinking string
	images         []string
	calls          []struct {
		ID, Name  string
		Arguments json.RawMessage
	}
}

func parse(raw json.RawMessage) (c content) {
	if json.Unmarshal(raw, &c.text) == nil {
		return c
	}
	var parts []struct {
		Type, Text, Thinking, Ref, ID, Name string
		Arguments                           json.RawMessage
	}
	_ = json.Unmarshal(raw, &parts)
	for _, p := range parts {
		switch p.Type {
		case "text":
			c.text += p.Text
		case "thinking":
			c.thinking += p.Thinking
		case "image":
			if p.Ref != "" {
				c.images = append(c.images, p.Ref)
			}
		case "toolCall":
			c.calls = append(c.calls, struct {
				ID, Name  string
				Arguments json.RawMessage
			}{p.ID, p.Name, p.Arguments})
		}
	}
	return c
}

func result(raw json.RawMessage) content {
	var r struct{ Content json.RawMessage }
	_ = json.Unmarshal(raw, &r)
	return parse(r.Content)
}

// apply folds one agent event in; false when it changes nothing drawn.
func (t *transcript) apply(raw json.RawMessage) bool {
	var e wireEvent
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	switch e.Type {
	case "message_start", "message_update", "message_end":
		var m wireMessage
		if json.Unmarshal(e.Message, &m) != nil {
			return false
		}
		t.message(m, e.Type == "message_end")
	case "tool_execution_start":
		t.tool(e.ToolCallID, e.ToolName, e.Args)
	case "tool_execution_update":
		if it := t.tools[e.ToolCallID]; it != nil {
			out := result(e.PartialResult).text
			it.result, it.output = firstLine(lastLine(out), 120), kept(out)
		}
	case "tool_execution_end":
		if it := t.tools[e.ToolCallID]; it != nil {
			r := result(e.Result)
			it.finish(r.text, e.IsError, r.images)
		}
	case "agent_end":
		if t.said != nil {
			t.said.live, t.said = false, nil
		}
		for _, it := range t.tools {
			it.live = false
		}
	case "compaction_start":
		t.note("compacting context…", false)
	case "compaction_end":
		var r struct{ TokensBefore int }
		_ = json.Unmarshal(e.Result, &r)
		switch {
		case e.ErrorMessage != "":
			t.note("compaction failed · "+errorText(e.ErrorMessage), true)
		case e.Aborted:
			t.note("compaction cancelled", false)
		default:
			t.note("context compacted"+tokensBefore(r.TokensBefore), false)
		}
	// Retries read as one line that updates, not a failure per attempt: the failed attempt's error
	// gives way to it, and it disappears once a retry gets through.
	case "auto_retry_start":
		if n := len(t.items); n > 0 && t.items[n-1].kind == "n" && t.items[n-1].alarm {
			t.items = t.items[:n-1]
		}
		t.retrying(fmt.Sprintf("retrying %d / %d · %s", e.Attempt, e.MaxAttempts, errorText(e.ErrorMessage)), false)
	case "auto_retry_end":
		if e.Success {
			t.retrying("", false)
		} else {
			t.retrying(fmt.Sprintf("gave up after %d retries", e.Attempt), true)
		}
	default:
		return false
	}
	return true
}

func (t *transcript) retrying(text string, alarm bool) {
	at := slices.Index(t.items, t.retry)
	t.retry = nil
	if at >= 0 {
		t.items = slices.Delete(t.items, at, at+1)
	}
	if text != "" {
		if it := t.note(text, alarm); !alarm {
			t.retry = it
		}
	}
}

// load replays whole messages: a snapshot's, or a page of history.
func (t *transcript) load(messages []json.RawMessage) {
	t.replaying, t.last = true, len(messages)-1
	for i, raw := range messages {
		var m wireMessage
		if json.Unmarshal(raw, &m) == nil {
			t.index = i
			t.message(m, true)
		}
	}
	t.replaying = false
}

func (t *transcript) message(m wireMessage, final bool) {
	switch m.Role {
	// Live user messages come as start and end; snapshots and history carry only the end.
	case "user":
		if final && t.userOpen {
			t.userOpen = false
			return
		}
		c := parse(m.Content)
		text := invocation(c.text)
		if tag := tagged.FindStringSubmatch(strings.TrimLeft(text, " \t\n")); tag != nil {
			// Machine-written turns (<task-notification>…) are events, not the person speaking.
			t.note(strings.ReplaceAll(tag[1], "-", " ")+" · "+clip(spaces.ReplaceAllString(strings.TrimSpace(markup.ReplaceAllString(text, " ")), " "), 160), false)
		} else {
			via := "bridge"
			if i := slices.Index(t.sent, text); i >= 0 || t.replaying {
				via = ""
				if i >= 0 {
					t.sent = slices.Delete(t.sent, i, i+1)
				}
			}
			t.add(&item{kind: "y", text: text, via: via, images: c.images})
		}
		t.userOpen = !final
	case "assistant":
		s := t.said
		if s == nil || !s.live {
			s = t.add(&item{kind: "s", live: true})
			t.said = s
		}
		c := parse(m.Content)
		s.text, s.thinking = c.text, c.thinking
		if !final {
			return
		}
		for _, call := range c.calls {
			t.tool(call.ID, call.Name, call.Arguments)
		}
		// In history, a failed attempt that a later message followed was retried: only a final failure shows.
		if m.StopReason == "error" && (!t.replaying || t.index == t.last) {
			t.note(errorText(cmp.Or(m.ErrorMessage, "error")), true)
		}
		s.live, t.said = false, nil
		if strings.TrimSpace(s.text) == "" && strings.TrimSpace(s.thinking) == "" {
			t.items = slices.DeleteFunc(t.items, func(it *item) bool { return it == s })
		}
	case "compactionSummary":
		t.note("context compacted"+tokensBefore(m.TokensBefore), false)
	case "branchSummary":
		t.note("branch summary · "+firstLine(m.Summary, 160), false)
	case "bashExecution":
		it := t.add(&item{kind: "t", verb: "bash", target: firstLine(m.Command, 120), args: m.Command})
		it.finish(m.Output, m.ExitCode != 0, nil)
	case "custom":
		if m.Display {
			t.note(clip(parse(m.Content).text, 400), false)
		}
	case "toolResult":
		if it := t.tools[m.ToolCallID]; it != nil {
			c := parse(m.Content)
			it.finish(c.text, m.IsError, c.images)
		}
	}
}

func (it *item) finish(out string, failed bool, images []string) {
	it.result, it.output, it.failed, it.live = summary(it.verb, out), kept(out), failed, false
	if images != nil {
		it.images = images
	}
}

func (t *transcript) tool(id, name string, raw json.RawMessage) {
	if t.tools == nil {
		t.tools = map[string]*item{}
	}
	if t.tools[id] != nil {
		return
	}
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	target := ""
	if qs, _ := args["questions"].([]any); len(qs) > 0 {
		q, _ := qs[0].(map[string]any)
		target, _ = q["question"].(string)
	}
	for _, k := range []string{"path", "file_path", "command", "pattern", "url", "query"} {
		if s, _ := args[k].(string); target == "" && strings.TrimSpace(s) != "" {
			target = s
		}
	}
	if target == "" && len(args) > 0 {
		keys := make([]string, 0, len(args))
		for k := range args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		target = fmt.Sprint(args[keys[0]])
	}
	pretty, _ := json.MarshalIndent(args, "", "  ")
	if len(args) == 0 {
		pretty = nil
	}
	verb := cmp.Or(verbs[name], name)
	if strings.HasPrefix(name, "mcp__") {
		verb = name[strings.LastIndex(name, "__")+2:] // mcp__server__tool: the tool says what it did
	}
	it := &item{kind: "t", verb: verb, target: firstLine(target, 120), args: clip(string(pretty), 2000), live: !t.replaying}
	t.tools[id] = t.keyed(it)
	// A call lands right after the message that made it, ahead of what a live stream added since.
	if at := slices.Index(t.items, t.said); t.said != nil && at >= 0 {
		t.items = slices.Insert(t.items, at+1, it)
	} else {
		t.items = append(t.items, it)
	}
}

// rows is the transcript as an app draws it: consecutive thoughts and tool calls make one run,
// prose a row per markdown block, notes and what someone said a row each.
func (t *transcript) rows() []Row {
	var rows []Row
	var run []*item
	flush := func() {
		if len(run) > 0 {
			rows = append(rows, runRow(run))
			run = nil
		}
	}
	for _, it := range t.items {
		switch it.kind {
		case "y":
			flush()
			rows = append(rows, Row{Key: it.key, Kind: "you", Text: it.text, Via: it.via, Images: it.images})
		case "s":
			if strings.TrimSpace(it.thinking) != "" {
				run = append(run, it)
			}
			if strings.TrimSpace(it.text) != "" {
				flush()
				for i, b := range it.markdown() {
					rows = append(rows, Row{Key: fmt.Sprintf("%s.%d", it.key, i), Kind: "md", Block: &b})
				}
			}
		case "t":
			run = append(run, it)
		case "n":
			flush()
			rows = append(rows, Row{Key: it.key, Kind: "note", Text: it.text, Alarm: it.alarm})
		}
	}
	flush()
	return rows
}

func (it *item) markdown() []Block {
	if state := fmt.Sprint(it.live, it.text); it.parsed != state {
		it.blocks, it.parsed = Blocks(it.text, !it.live), state
	}
	return it.blocks
}

// runRow folds a run of actions into one row, as the TUI folds exploration: what they were,
// counted by kind, what runs now, and how many failed.
func runRow(run []*item) Row {
	row := Row{Key: run[0].key, Kind: "run"}
	var order []string
	count := map[string]int{}
	for _, it := range run {
		a := Action{Key: it.key, Verb: it.verb, Target: it.target, Result: it.result, Live: it.live, Failed: it.failed}
		kind := cmp.Or(kinds[strings.ToLower(it.verb)], it.verb) // Claude Code names its tools Bash, Read…
		if it.kind == "s" {
			thought := strings.TrimSpace(it.thinking)
			title, _, _ := strings.Cut(strings.TrimPrefix(firstLine(thought, 200), "**"), "**")
			a = Action{Key: it.key + ":t", Verb: "thought", Target: title, Result: fmt.Sprintf("%d tok", len(thought)/4), Live: it.live && strings.TrimSpace(it.text) == ""}
			kind = "thought"
		} else {
			row.Images = append(row.Images, it.images...)
		}
		if count[kind]++; count[kind] == 1 {
			order = append(order, kind)
		}
		row.Live = row.Live || a.Live
		if a.Failed {
			row.Failed++
		}
		if a.Live && it.kind == "t" {
			row.Now = a.Target
		}
		row.Actions = append(row.Actions, a)
	}
	what := make([]string, len(order))
	for i, k := range order {
		n := count[k]
		switch {
		case n == 1:
			what[i] = "1 " + k
		case k == "search":
			what[i] = fmt.Sprintf("%d searches", n)
		default:
			what[i] = fmt.Sprintf("%d %ss", n, k)
		}
	}
	row.What = strings.Join(what, " · ")
	return row
}

// detail is what an action was given and returned in full, or a thought's whole text.
func (t *transcript) detail(key string) map[string]string {
	for _, it := range t.items {
		switch {
		case it.kind == "t" && it.key == key:
			out := it.output
			if out == "" {
				out = map[bool]string{true: "running…", false: "no output"}[it.live]
			}
			return map[string]string{"args": it.args, "output": out}
		case it.kind == "s" && it.key+":t" == key:
			return map[string]string{"text": strings.TrimSpace(it.thinking)}
		}
	}
	return nil
}

// lastText is the last thing Orb said.
func (t *transcript) lastText() string {
	for i := len(t.items) - 1; i >= 0; i-- {
		if t.items[i].kind == "s" && strings.TrimSpace(t.items[i].text) != "" {
			return t.items[i].text
		}
	}
	return ""
}

// invocation is a message that invoked skills as it was typed, their tokens in place; a stored
// preview, clipped before its block closed, keeps the first skill's token.
func invocation(raw string) string {
	if skill, ok := exporthtml.ParseSkillBlock(raw); ok {
		return skill.InvocationText()
	}
	if rest, ok := strings.CutPrefix(raw, `<skill name="`); ok {
		if name, _, found := strings.Cut(rest, `"`); found {
			return exporthtml.SkillTokenPrefix + name
		}
	}
	return raw
}

// errorText keeps what a turn needs of a provider's error: its status and its sentence.
func errorText(raw string) string {
	var parts []string
	if s := status.FindString(strings.TrimSpace(raw)); s != "" {
		parts = append(parts, s)
	}
	if m := message.FindStringSubmatch(raw); m != nil {
		parts = append(parts, strings.ReplaceAll(m[1], `\"`, `"`))
	}
	if len(parts) == 0 {
		return firstLine(raw, 200)
	}
	return strings.Join(parts, " · ")
}

// summary is the result an action line shows: an answer to questions, a command's last line,
// or how many lines came back.
func summary(verb, out string) string {
	if verb == "ask" {
		var r struct {
			Cancelled bool
			Answers   []struct {
				Selected []string
				Custom   string
			}
		}
		if json.Unmarshal([]byte(out), &r) == nil {
			if r.Answers == nil {
				return map[bool]string{true: "dismissed", false: "done"}[r.Cancelled]
			}
			picked := make([]string, len(r.Answers))
			for i, a := range r.Answers {
				picked[i] = a.Custom
				if len(a.Selected) > 0 && a.Selected[0] != "" {
					picked[i] = a.Selected[0]
				}
			}
			return strings.Join(picked, " · ")
		}
	}
	lines := slices.DeleteFunc(strings.Split(strings.TrimRight(out, " \t\n"), "\n"), func(l string) bool { return strings.TrimSpace(l) == "" })
	switch {
	case len(lines) == 0:
		return "done"
	case verb == "bash" || len(lines) == 1:
		return clip(lines[len(lines)-1], 120)
	}
	return fmt.Sprintf("%d lines", len(lines))
}

func tokensBefore(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" · from %dk tokens", n/1000)
}

func firstLine(s string, n int) string {
	line, _, _ := strings.Cut(s, "\n")
	return clip(line, n)
}

func lastLine(s string) string {
	s = strings.TrimRight(s, " \t\n")
	return s[strings.LastIndex(s, "\n")+1:]
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func kept(s string) string {
	if r := []rune(s); len(r) > outputKept {
		return string(r[len(r)-outputKept:])
	}
	return s
}
