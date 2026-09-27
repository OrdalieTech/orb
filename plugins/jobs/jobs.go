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
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

// Bash builds the host's bash tool for a working directory.
type Bash func(cwd string) (engine.AgentTool, error)

const (
	maxJobs  = 8
	maxLines = 20
)

var stopSchema = ai.JSONSchema(`{"type":"object","required":["job"],"properties":{"job":{"type":"string","description":"ID of a background job started by bash"}}}`)

type job struct {
	id, command, log, pid string
	monitor               bool
	started               time.Time
	tool                  engine.AgentTool
	stopped               atomic.Bool
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
		properties["run_in_background"] = map[string]any{"type": "boolean", "description": "Run as a background job: return its ID and log file at once; a message reports when it ends."}
		properties["monitor"] = map[string]any{"type": "boolean", "description": "Run in the background and report each line the command prints, as it prints it."}
		parameters, _ := json.Marshal(schema)
		api.RegisterTool(extensions.ToolDefinition{
			Name: "bash", Label: "bash", Description: base.Description + " With run_in_background or monitor, the command runs as a background job; stop it with stop_job.",
			PromptSnippet: "Execute bash commands (ls, grep, find, etc.)",
			PromptGuidelines: []string{
				"You can inspect PI_* environment variables for current model and session details.",
				"Run long commands (builds, test suites, servers, deploys) with run_in_background instead of waiting or polling with sleep: a message reports when the job ends, and its log file can be read at any time. Use monitor to be told each line a command prints, such as a log to watch.",
			},
			Parameters: parameters,
			Execute:    p.execute,
		})
		api.RegisterTool(extensions.ToolDefinition{Name: "stop_job", Label: "Stop job", Description: "Stop a background job started by bash", Parameters: stopSchema, Execute: p.stop})
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
	return p.start(ctx, tool, input.Command, input.Monitor)
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

func (p *plugin) start(ctx context.Context, tool engine.AgentTool, command string, monitor bool) (engine.AgentToolResult, error) {
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
	j := &job{id: strconv.Itoa(p.next), command: command, monitor: monitor, started: time.Now(), tool: tool}
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
	wrapper := fmt.Sprintf(`set -m; ( "$0" %s > %s 2>&1 < /dev/null; echo $? > %s ) & echo $!`, quote(script), quote(j.log), quote(j.log+".exit"))
	result, err := tool.Execute(ctx, "", map[string]any{"command": wrapper}, nil)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	output := strings.TrimSpace(ai.ContentText(result.Content))
	if j.pid = output[strings.LastIndexByte(output, '\n')+1:]; !isPID(j.pid) {
		return engine.AgentToolResult{}, fmt.Errorf("background job did not start: %s", j.pid)
	}
	p.mu.Lock()
	p.jobs[j.id] = j
	p.mu.Unlock()
	go p.watch(j)
	report := "reports when it ends"
	if monitor {
		report = "reports each line it prints, and its end"
	}
	return toolutil.TextResult(fmt.Sprintf("Started background job %s (pid %s). Output: %s. A message %s; stop it with stop_job.", j.id, j.pid, j.log, report)), nil
}

// watch reports a monitored job's new lines and every job's end, at most once a second.
func (p *plugin) watch(j *job) {
	var offset int64
	ticker := time.NewTicker(time.Second)
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
		if j.monitor {
			if lines := newLines(j.log, &offset); len(lines) > 0 {
				p.notify(fmt.Sprintf("Job %s printed:\n%s", j.id, clip(lines)))
			}
		}
		code, err := os.ReadFile(j.log + ".exit")
		if err != nil {
			continue
		}
		p.mu.Lock()
		delete(p.jobs, j.id)
		p.mu.Unlock()
		text := fmt.Sprintf("Job %s (%s) exited with code %s after %s. Output: %s", j.id, oneLine(j.command), strings.TrimSpace(string(code)), time.Since(j.started).Round(time.Second), j.log)
		if !j.monitor {
			if tail := lastLines(j.log, 10); tail != "" {
				text += "\nLast lines:\n" + tail
			}
		}
		p.notify(text)
		return
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
	return toolutil.TextResult(fmt.Sprintf("Stopped background job %s. Output: %s", j.id, j.log)), nil
}

// shutdown ends every job with the session, as Claude Code does.
func (p *plugin) shutdown() {
	p.cancel()
	p.mu.Lock()
	running, dir := p.jobs, p.dir
	p.jobs = map[string]*job{}
	p.mu.Unlock()
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	for _, j := range running {
		kill(ctx, j)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// kill ends the job's process group through the same bash that started it.
func kill(ctx context.Context, j *job) {
	j.stopped.Store(true)
	_, _ = j.tool.Execute(ctx, "", map[string]any{"command": fmt.Sprintf("kill -TERM -%s 2>/dev/null || kill -TERM %s", j.pid, j.pid)}, nil)
}

// newLines returns the complete lines added to path since offset.
func newLines(path string, offset *int64) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 64<<10)
	count, _ := file.ReadAt(buffer, *offset)
	end := strings.LastIndexByte(string(buffer[:count]), '\n')
	if end < 0 && count == len(buffer) {
		*offset += int64(count)
		return []string{"(a line over 64 KB, see the log)"}
	}
	if end < 0 {
		return nil
	}
	*offset += int64(end + 1)
	return strings.Split(string(buffer[:end]), "\n")
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
	if cut || len(command) > 80 {
		return command[:min(len(command), 80)] + "…"
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
