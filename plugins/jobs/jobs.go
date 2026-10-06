// Package jobs lets Orb's own models run shell commands in the background, as
// Claude Code does: bash gains run_in_background and monitor, a message reports
// each job's end (and, when monitored, the lines it prints), and stop_job ends
// one. Jobs run through the host's bash tool, so its sandbox, shell, command
// prefix and permission rules apply unchanged.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/activity"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

// Bash builds the host's bash tool for a working directory.
type Bash func(cwd string) (engine.AgentTool, error)

const (
	maxJobs  = 8
	maxLines = 20
)

// tick paces reports: a job's end or a monitored job's new lines, at most one message per tick.
var tick = time.Second

var stopSchema = ai.JSONSchema(`{"type":"object","required":["job"],"properties":{"job":{"type":"string","description":"The job ID bash returned when it started the job"}}}`)

type job struct {
	id, command, log, pid string
	monitor               bool
	started               time.Time
	tool                  engine.AgentTool
	stopped               atomic.Bool
	publish               func(activity.Record)
}

type plugin struct {
	api    extensions.API
	bash   Bash
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	dir    string
	next   int
	jobs   map[string]*job
}

// Extension adds background jobs to bash. A nil bash runs plain local bash.
func Extension(bash Bash) extensions.Factory {
	return func(api extensions.API) error {
		if bash == nil {
			bash = func(cwd string) (engine.AgentTool, error) { return tools.NewBashTool(cwd, nil), nil }
		}
		p := &plugin{api: api, bash: bash, jobs: map[string]*job{}}
		p.ctx, p.cancel = context.WithCancel(context.Background())
		base := tools.NewBashTool("", nil).Spec()
		var schema map[string]any
		if err := json.Unmarshal(base.Parameters, &schema); err != nil {
			return err
		}
		properties, _ := schema["properties"].(map[string]any)
		properties["run_in_background"] = map[string]any{"type": "boolean", "description": "Run as a background job and return at once with its ID and log file. When it ends, a message gives its exit code and last lines; read the log for the rest. Do not add '&'; timeout does not apply."}
		properties["monitor"] = map[string]any{"type": "boolean", "description": "Like run_in_background, and also report the lines the command prints, batched as they come. Each report can start a turn, so print only lines worth acting on, e.g. tail -f app.log | grep --line-buffered ERROR."}
		parameters, _ := json.Marshal(schema)
		api.RegisterTool(extensions.ToolDefinition{
			Name: "bash", Label: "bash", Description: base.Description + " A long command can run as a background job (run_in_background or monitor): the call returns at once, a message reports when the job ends, and stop_job stops it.",
			PromptSnippet: "Execute bash commands (ls, grep, find, etc.), in the background too",
			PromptGuidelines: []string{
				"You can inspect PI_* environment variables for current model and session details.",
				"Run commands that take long or never end (builds, test suites, dev servers, watchers) with run_in_background and keep working: a message reports when each job ends. Do not wait for a job with sleep or poll its log in a loop.",
				"Use monitor only for output you must react to while the command runs, filtered to the lines that matter (grep --line-buffered, as pipes buffer): each report can start a turn.",
				"Background jobs end with the session; stop the ones you no longer need with stop_job.",
			},
			Parameters: parameters,
			Execute:    p.execute,
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "stop_job", Label: "Stop job", PromptSnippet: "Stop a background job",
			Description: "Stop a background job started by bash: its whole process group gets TERM, then KILL after two seconds. A stopped job's end is not reported; its log stays readable.",
			Parameters:  stopSchema, Execute: p.stop,
		})
		// A job's report reads as one dim line, like Claude's task notices; expanded, it shows whole.
		api.RegisterMessageRenderer("job", func(message extensions.CustomMessage, options extensions.MessageRenderOptions, theme extensions.Theme) extensions.Component {
			text := fmt.Sprint(message.Content)
			if first, rest, more := strings.Cut(text, "\n"); more && !options.Expanded {
				text = first + " " + rest[strings.LastIndexByte(rest, '\n')+1:]
			}
			return notice{text, theme}
		})
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			p.shutdown()
			return nil, nil
		})
		return nil
	}
}

func (p *plugin) execute(ctx context.Context, id string, raw any, update engine.AgentToolUpdateCallback, session extensions.Context) (engine.AgentToolResult, error) {
	var input struct {
		Command    string `json:"command"`
		Background bool   `json:"run_in_background"`
		Monitor    bool   `json:"monitor"`
	}
	if err := toolutil.Decode(raw, &input); err != nil {
		return engine.AgentToolResult{}, err
	}
	tool, err := p.tool(session)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if !input.Background && !input.Monitor {
		return tool.Execute(ctx, id, raw, update)
	}
	bus := p.api.Events()
	publish := activity.Publisher(func() extensions.EventBus { return bus }, session.SessionManager().GetSessionID(), "Orb")
	return p.start(ctx, tool, input.Command, input.Monitor, publish)
}

// tool is the host's bash with the session metadata the runtime gives its own.
func (p *plugin) tool(session extensions.Context) (engine.AgentTool, error) {
	tool, err := p.bash(session.CWD())
	if binder, ok := tool.(tools.BashSessionEnvironmentBinder); ok && err == nil {
		binder.BindSessionEnvironment(func() *tools.BashSessionEnvironment {
			manager := session.SessionManager()
			info := &tools.BashSessionEnvironment{SessionID: manager.GetSessionID(), SessionFile: manager.GetSessionFile()}
			if model := session.Model(); model != nil {
				info.Provider, info.Model = string(model.Provider), model.ID
			}
			if level, err := p.api.GetThinkingLevel(); err == nil {
				info.ReasoningLevel = string(level)
			}
			return info
		})
	}
	return tool, err
}

func (p *plugin) start(ctx context.Context, tool engine.AgentTool, command string, monitor bool, publish func(activity.Record)) (engine.AgentToolResult, error) {
	p.mu.Lock()
	if len(p.jobs) >= maxJobs {
		p.mu.Unlock()
		return engine.AgentToolResult{}, fmt.Errorf("%d background jobs already run; stop one with stop_job", maxJobs)
	}
	var err error
	if p.dir == "" {
		p.dir, err = os.MkdirTemp("", "orb-jobs-")
	}
	p.next++
	j := &job{id: strconv.Itoa(p.next), command: command, monitor: monitor, started: time.Now(), tool: tool, publish: publish}
	j.log = filepath.Join(p.dir, j.id+".log")
	p.mu.Unlock()
	script := filepath.Join(p.dir, j.id+".sh")
	if err == nil {
		err = os.WriteFile(script, []byte(command+"\n"), 0o600)
	}
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	// The job runs through the same bash call as any command. set -m gives it a
	// process group of its own, which stop_job ends whole; "$0" is that shell.
	// Its PID is marked: a job that ends at once makes bash print a notice too.
	wrapper := fmt.Sprintf(`set -m; ( "$0" %s; echo $? > %s ) > %s 2>&1 < /dev/null & echo "orb-job $!"`, quote(script), quote(j.log+".exit"), quote(j.log))
	result, err := tool.Execute(ctx, "", map[string]any{"command": wrapper}, nil)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	output := ai.ContentText(result.Content)
	_, pid, _ := strings.Cut(output, "orb-job ")
	if j.pid, _, _ = strings.Cut(pid, "\n"); !isPID(j.pid) {
		return engine.AgentToolResult{}, fmt.Errorf("background job did not start: %s", strings.TrimSpace(output))
	}
	p.mu.Lock()
	p.jobs[j.id] = j
	p.mu.Unlock()
	j.activity(activity.Running)
	go p.watch(j)
	report := "reports when it ends"
	if monitor {
		report = "reports each line it prints, and its end"
	}
	return toolutil.TextResult(fmt.Sprintf("Started background job %s (pid %s). Output: %s. A message %s, so keep working rather than wait or poll; stop it with stop_job.", j.id, j.pid, j.log, report)), nil
}

func (j *job) activity(state activity.State) {
	if j.publish == nil {
		return
	}
	title := "Bash #" + j.id
	// Only the executable, never raw shell arguments or assignments, enters the bar.
	if words := strings.Fields(j.command); len(words) > 0 && !strings.Contains(words[0], "=") {
		title += " · " + filepath.Base(words[0])
	}
	j.publish(activity.Record{ID: j.id, Kind: activity.Process, Title: title, State: state, Started: j.started})
}

// watch reports a monitored job's new lines and every job's end, at most once a tick.
func (p *plugin) watch(j *job) {
	var at cursor
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
		}
		if j.stopped.Load() {
			return
		}
		code, done := exitCode(j.log)
		if !done && alive(j.pid) {
			p.report(j, &at, false)
			continue
		}
		// The group ended without its exit code (killed outside Orb), unless
		// the code landed between the two reads.
		if !done {
			if code, done = exitCode(j.log); !done {
				code = "none (the job was killed)"
			}
		}
		// stop_job may have taken the job meanwhile; then it is not reported.
		p.mu.Lock()
		_, mine := p.jobs[j.id]
		delete(p.jobs, j.id)
		p.mu.Unlock()
		if !mine {
			return
		}
		state := activity.Failed
		if code == "0" {
			state = activity.Completed
		} else if !done {
			state = activity.Unknown
		}
		j.activity(state)
		p.report(j, &at, true)
		text := fmt.Sprintf("Job %s (%s) exited with code %s after %s. Output: %s", j.id, oneLine(j.command), code, time.Since(j.started).Round(time.Second), j.log)
		if !j.monitor {
			if tail := lastLines(j.log, 10); tail != "" {
				text += "\nLast lines:\n" + tail
			}
		}
		p.notify(text)
		return
	}
}

// exitCode reads the code a job's shell wrote as it ended. The file exists
// empty for a moment first, as the shell opens it before writing.
func exitCode(log string) (string, bool) {
	data, _ := os.ReadFile(log + ".exit")
	code := strings.TrimSpace(string(data))
	return code, code != ""
}

// report sends a monitored job's new lines; other jobs' logs are left unread.
func (p *plugin) report(j *job, at *cursor, final bool) {
	if !j.monitor {
		return
	}
	if lines := at.read(j.log, final); len(lines) > 0 {
		p.notify(fmt.Sprintf("Job %s printed:\n%s", j.id, clip(lines)))
	}
}

// notify tells the model, starting a turn when it is idle.
func (p *plugin) notify(text string) {
	// ponytail: a message racing the session's shutdown is dropped, as the job is.
	defer func() { _ = recover() }()
	if p.ctx.Err() != nil {
		return
	}
	trigger := true
	_ = p.api.SendMessage(context.Background(), extensions.CustomMessage{CustomType: "job", Content: text, Display: true},
		&extensions.SendMessageOptions{TriggerTurn: &trigger, DeliverAs: extensions.DeliverSteer})
}

func (p *plugin) stop(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
	var input struct {
		Job string `json:"job"`
	}
	if err := toolutil.Decode(raw, &input); err != nil {
		return engine.AgentToolResult{}, err
	}
	p.mu.Lock()
	j := p.jobs[input.Job]
	delete(p.jobs, input.Job)
	p.mu.Unlock()
	if j == nil {
		return engine.AgentToolResult{}, fmt.Errorf("no background job %q is running", input.Job)
	}
	kill(ctx, j)
	state := activity.Cancelled
	if ctx.Err() != nil || alive(j.pid) {
		state = activity.Unknown
	}
	j.activity(state)
	return toolutil.TextResult(fmt.Sprintf("Stopped background job %s. Output: %s", j.id, j.log)), nil
}

// shutdown ends every job with the session, as Claude Code does.
func (p *plugin) shutdown() {
	p.cancel()
	p.mu.Lock()
	running, dir := p.jobs, p.dir
	p.jobs = map[string]*job{}
	p.mu.Unlock()
	// At once: a job ignoring TERM holds its kill for two seconds.
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	var wg sync.WaitGroup
	for _, j := range running {
		wg.Go(func() { kill(ctx, j) })
	}
	wg.Wait()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// kill ends the job's process group through the same bash that started it.
func kill(ctx context.Context, j *job) {
	j.stopped.Store(true)
	// TERM first for a clean exit, KILL after two seconds for a job that ignores it.
	stop := fmt.Sprintf(`kill -TERM -%[1]s 2>/dev/null || kill -TERM %[1]s 2>/dev/null; i=0; while [ $i -lt 20 ] && kill -0 -%[1]s 2>/dev/null; do sleep 0.1; i=$((i+1)); done; kill -KILL -%[1]s 2>/dev/null; true`, j.pid)
	_, _ = j.tool.Execute(ctx, "", map[string]any{"command": stop}, nil)
}

// cursor is how far a monitored job's log has been reported.
type cursor struct {
	offset int64
	long   bool // inside a line over the read buffer, already reported
}

// read returns the lines added to path since the last read: complete ones, and
// with final the last one even without its newline. A line over 64 KB is
// reported once, as a placeholder.
func (c *cursor) read(path string, final bool) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var lines []string
	buffer := make([]byte, 64<<10)
	for {
		count, _ := file.ReadAt(buffer, c.offset)
		chunk := string(buffer[:count])
		end := strings.LastIndexByte(chunk, '\n')
		if end < 0 {
			if count == len(buffer) || final && count > 0 {
				c.offset += int64(count)
				if !c.long {
					lines = append(lines, chunk)
				}
			}
			if count < len(buffer) {
				return lines
			}
			if !c.long {
				lines[len(lines)-1] = "(a line over 64 KB, see the log)"
			}
			c.long = true
			continue
		}
		c.offset += int64(end + 1)
		complete := strings.Split(chunk[:end], "\n")
		if c.long {
			complete, c.long = complete[1:], false
		}
		lines = append(lines, complete...)
		if count < len(buffer) && (!final || end == count-1) {
			return lines
		}
	}
}

func clip(lines []string) string {
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… %d more lines in the log", len(lines)-maxLines)
}

func lastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func oneLine(command string) string {
	command, _, cut := strings.Cut(strings.TrimSpace(command), "\n")
	if runes := []rune(command); cut || len(runes) > 80 {
		return string(runes[:min(len(runes), 80)]) + "…"
	}
	return command
}

func isPID(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

// notice is a job report in the transcript: dim, indented, cut to the width.
type notice struct {
	text  string
	theme extensions.Theme
}

func (n notice) Render(width int) []string {
	lines := strings.Split(n.text, "\n")
	for i, line := range lines {
		if runes := []rune(line); width > 4 && len(runes) > width-3 {
			line = string(runes[:width-4]) + "…"
		}
		lines[i] = "   " + n.theme.FG("dim", line)
	}
	return lines
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }
