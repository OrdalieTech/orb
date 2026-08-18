package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

const (
	childConcurrency    = 4
	forkMessageLimit    = 20
	externalOutputLimit = 1 << 20
	externalTimeout     = 10 * time.Minute
	// ponytail: one flat width cap, because `tasks` is model-controlled and each
	// entry costs a goroutine, a temp dir, a session, and a real provider call.
	// Uncapped, one tool call fans out as wide as the model asks. A per-session
	// or per-run budget is the upgrade path when 32 stops being enough.
	maxParallelTasks = 32
)

// ponytail: `mode` is the only unconditionally required field — task/tasks are
// required per branch, which plain JSON Schema cannot say without a oneOf that
// several providers reject. Execute rejects the wrong pairing with a clear error.
var subagentSchema = ai.JSONSchema(`{"type":"object","required":["mode"],"properties":{"mode":{"type":"string","enum":["single","parallel"],"description":"single runs one child from task/agent; parallel runs every entry of tasks concurrently."},"task":{"type":"string","description":"Self-contained instruction for the child, including any context it needs. Required when mode is single."},"agent":{"type":"string","enum":["scout","worker","reviewer"],"description":"Built-in role or configured external CLI. An external CLI receives only the task text on stdin (context and tools do not apply) and must finish within 10 minutes. Defaults to worker."},"context":{"type":"string","enum":["fresh","fork"],"description":"fresh starts with an empty conversation; fork prepends a transcript of the recent parent conversation. Defaults to fresh."},"tools":{"type":"array","items":{"type":"string"},"description":"Optional subset of the role's tools; names outside the role are dropped, and an empty array leaves the child with no tools."},"tasks":{"type":"array","maxItems":32,"description":"Children to run concurrently, at most 32 per call. Required when mode is parallel; ignored otherwise.","items":{"type":"object","required":["task"],"properties":{"task":{"type":"string","description":"Self-contained instruction for this child."},"agent":{"type":"string","enum":["scout","worker","reviewer"],"description":"Built-in role or configured external CLI. An external CLI receives only the task text on stdin (context and tools do not apply) and must finish within 10 minutes. Defaults to worker."},"context":{"type":"string","enum":["fresh","fork"],"description":"fresh or fork for this task. Defaults to fresh."},"tools":{"type":"array","items":{"type":"string"},"description":"Optional subset of this role's tools."}}}}}}`)

type archetype struct {
	prompt string
	tools  []string
}

// ponytail: Three fixed roles cover exploration, implementation, and review;
// add another only when a distinct tool boundary is actually needed.
var archetypes = map[string]archetype{
	"scout":    {prompt: "Explore quickly, report concrete evidence, and do not modify files.", tools: []string{"read", "grep", "find", "ls"}},
	"worker":   {prompt: "Complete the task directly and report the result.", tools: []string{"read", "bash", "edit", "write", "grep", "find", "ls"}},
	"reviewer": {prompt: "Review the evidence critically and report actionable findings without modifying files.", tools: []string{"read", "grep", "find", "ls"}},
}

type subagentInput struct {
	Task    string         `json:"task"`
	Agent   string         `json:"agent"`
	Mode    string         `json:"mode"`
	Tasks   []subagentTask `json:"tasks"`
	Context string         `json:"context"`
	Tools   []string       `json:"tools"`
}

type subagentTask struct {
	Task    string   `json:"task"`
	Agent   string   `json:"agent"`
	Context string   `json:"context"`
	Tools   []string `json:"tools"`
}

type childProgress struct{ name, status string }

// cappedOutput grows on demand up to externalOutputLimit; each stream has a
// single writer (the exec copier goroutine), so no lock is needed.
type cappedOutput struct {
	buffer   bytes.Buffer
	exceeded atomic.Bool
	cancel   context.CancelFunc
}

func newCappedOutput(cancel context.CancelFunc) *cappedOutput {
	return &cappedOutput{cancel: cancel}
}

func (output *cappedOutput) Write(data []byte) (int, error) {
	room := min(externalOutputLimit-output.buffer.Len(), len(data))
	if room > 0 {
		output.buffer.Write(data[:room])
	}
	if room < len(data) && output.exceeded.CompareAndSwap(false, true) {
		output.cancel()
	}
	return len(data), nil
}

func (output *cappedOutput) String() string { return output.buffer.String() }

// externalEntry is one configured external CLI: its command and whether it is
// currently offered to the model.
type externalEntry struct {
	Command string
	Enabled bool
}

// externalSubagents returns the enabled name→command map the subagent tool
// exposes. Disabled entries keep their configuration but disappear from the
// tool surface.
func externalSubagents(settings *config.SettingsManager) (map[string]string, error) {
	entries, err := externalSubagentEntries(settings)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	enabled := make(map[string]string, len(entries))
	for name, entry := range entries {
		if entry.Enabled {
			enabled[name] = entry.Command
		}
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	return enabled, nil
}

// externalSubagentEntries parses plugins.subagents.external, where each value
// is either a command string or {"command": string, "enabled": bool} — the
// object form is how a CLI is switched off without losing its command.
func externalSubagentEntries(settings *config.SettingsManager) (map[string]externalEntry, error) {
	if settings == nil {
		return nil, nil
	}
	return parseExternalEntries(settings.GetPluginSettings("subagents"))
}

// parseExternalEntries parses one scope's raw subagents configuration.
func parseExternalEntries(configured map[string]any) (map[string]externalEntry, error) {
	encoded, err := json.Marshal(configured)
	parsed := struct {
		Enabled  *bool                      `json:"enabled"`
		External map[string]json.RawMessage `json:"external"`
	}{}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err == nil {
		err = decoder.Decode(&parsed)
	}
	if err != nil {
		return nil, fmt.Errorf("plugins: invalid subagents settings: %w", err)
	}
	if value, exists := configured["enabled"]; exists && value == nil {
		return nil, fmt.Errorf("plugins: subagents.enabled must be true or false")
	}
	if value, exists := configured["external"]; !exists {
		return nil, nil
	} else if value == nil {
		return nil, fmt.Errorf("plugins: subagents.external must map names to commands")
	}
	entries := make(map[string]externalEntry, len(parsed.External))
	for name, raw := range parsed.External {
		if strings.TrimSpace(name) == "" || strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("plugins: subagents.external names must be non-empty without whitespace or control characters")
		}
		if _, exists := archetypes[name]; exists {
			return nil, fmt.Errorf("plugins: subagents.external cannot replace built-in agent %q", name)
		}
		entry := externalEntry{Enabled: true}
		if stringErr := json.Unmarshal(raw, &entry.Command); stringErr != nil {
			object := struct {
				Command string `json:"command"`
				Enabled *bool  `json:"enabled"`
			}{}
			objectDecoder := json.NewDecoder(bytes.NewReader(raw))
			objectDecoder.DisallowUnknownFields()
			if objectErr := objectDecoder.Decode(&object); objectErr != nil {
				return nil, fmt.Errorf("plugins: invalid subagents settings: subagents.external.%s must be a command or {command, enabled}", name)
			}
			entry.Command = object.Command
			if object.Enabled != nil {
				entry.Enabled = *object.Enabled
			}
		}
		if strings.TrimSpace(entry.Command) == "" {
			return nil, fmt.Errorf("plugins: subagents.external.%s command must not be empty", name)
		}
		entries[name] = entry
	}
	return entries, nil
}

func schemaWithExternal(external map[string]string) ai.JSONSchema {
	names := []string{"scout", "worker", "reviewer"}
	for name := range external {
		names = append(names, name)
	}
	slices.Sort(names[3:])
	encoded, _ := json.Marshal(names)
	return ai.JSONSchema(strings.ReplaceAll(string(subagentSchema), `["scout","worker","reviewer"]`, string(encoded)))
}

func subagentsExtension(injected engine.StreamFn, policy *Policy, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		external, err := externalSubagents(settings)
		if err != nil {
			return err
		}
		var progressMu sync.Mutex
		api.RegisterTool(extensions.ToolDefinition{
			Name: "subagent", Label: "Subagent", Description: "Run a child agent", Parameters: schemaWithExternal(external),
			Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, extensionContext extensions.Context) (engine.AgentToolResult, error) {
				var input subagentInput
				if err := decode(raw, &input); err != nil {
					return engine.AgentToolResult{}, err
				}
				mode := input.Mode
				if mode == "" {
					mode = "single"
				}
				var tasks []subagentTask
				switch mode {
				case "single":
					tasks = []subagentTask{{Task: input.Task, Agent: input.Agent, Context: input.Context, Tools: input.Tools}}
				case "parallel":
					tasks = append([]subagentTask(nil), input.Tasks...)
					if len(tasks) == 0 {
						return engine.AgentToolResult{}, fmt.Errorf("subagent: parallel mode requires tasks")
					}
					if len(tasks) > maxParallelTasks {
						return engine.AgentToolResult{}, fmt.Errorf(
							"subagent: parallel mode accepts at most %d tasks, got %d; split the work across calls",
							maxParallelTasks, len(tasks))
					}
				default:
					return engine.AgentToolResult{}, fmt.Errorf("subagent: mode must be single or parallel")
				}
				for index := range tasks {
					if strings.TrimSpace(tasks[index].Task) == "" {
						return engine.AgentToolResult{}, fmt.Errorf("subagent: task is required")
					}
					if tasks[index].Agent == "" {
						tasks[index].Agent = "worker"
					}
					if tasks[index].Context == "" {
						tasks[index].Context = "fresh"
					}
					_, builtIn := archetypes[tasks[index].Agent]
					if _, configured := external[tasks[index].Agent]; !builtIn && !configured {
						return engine.AgentToolResult{}, fmt.Errorf("subagent: unknown agent %q", tasks[index].Agent)
					}
					if tasks[index].Context != "fresh" && tasks[index].Context != "fork" {
						return engine.AgentToolResult{}, fmt.Errorf("subagent: context must be fresh or fork")
					}
				}

				progress := make([]childProgress, len(tasks))
				for index, task := range tasks {
					progress[index] = childProgress{name: fmt.Sprintf("%s-%d", task.Agent, index+1), status: "queued"}
				}
				// The run owns the widget: every child is joined below, so no
				// update can outlive this clear.
				// extensions.Context has no staleness predicate and every accessor
				// panics once the session is disposed or reloaded, so the UI is
				// captured here while the context is provably live. A fan-out
				// routinely outlives its session: Dispose during the run would
				// otherwise panic a child goroutine and take the host down.
				ui := extensionContext.UI()
				defer ui.SetWidget("subagents", nil, nil)
				updateProgress := func(index int, status string) {
					// SetWidget stays under the lock, otherwise a stale line set
					// can reach the UI after a newer one.
					progressMu.Lock()
					defer progressMu.Unlock()
					progress[index].status = status
					lines := make([]string, len(progress))
					for childIndex, child := range progress {
						lines[childIndex] = child.name + ": " + child.status
					}
					ui.SetWidget("subagents", &extensions.Widget{Lines: lines}, nil)
				}
				for index := range progress {
					updateProgress(index, "queued")
				}

				results := make([]string, len(tasks))
				errorsByChild := make([]error, len(tasks))
				// ponytail: Runs stay foreground-only with no detached supervisor,
				// watchdog, or profiles; add lifecycle machinery when callers need it.
				semaphore := make(chan struct{}, childConcurrency)
				var group sync.WaitGroup
				for index, task := range tasks {
					group.Add(1)
					go func() {
						defer group.Done()
						select {
						case semaphore <- struct{}{}:
						case <-ctx.Done():
							errorsByChild[index] = ctx.Err()
							updateProgress(index, "cancelled")
							return
						}
						defer func() { <-semaphore }()
						updateProgress(index, "running")
						results[index], errorsByChild[index] = runChildGuarded(ctx, extensionContext, injected, policy, external, task)
						if errorsByChild[index] != nil {
							updateProgress(index, "error")
						} else {
							updateProgress(index, "done")
						}
					}()
				}
				group.Wait()
				if mode == "single" {
					if errorsByChild[0] != nil {
						return engine.AgentToolResult{}, errorsByChild[0]
					}
					return textResult(results[0]), nil
				}
				sections := make([]string, len(tasks))
				failures := 0
				for index, task := range tasks {
					body := results[index]
					if errorsByChild[index] != nil {
						body = "error: " + errorsByChild[index].Error()
						failures++
					}
					sections[index] = fmt.Sprintf("[%d] %s\n%s", index+1, task.Agent, body)
				}
				report := strings.Join(sections, "\n\n")
				if failures > 0 {
					// Same signal as single mode: a failed child makes the call an
					// error. The report is the message so surviving output survives.
					return engine.AgentToolResult{}, fmt.Errorf("subagent: %d of %d children failed\n\n%s", failures, len(tasks), report)
				}
				return textResult(report), nil
			},
		})
		return nil
	}
}

func childOptions(parentRegistry extensions.ModelRegistry, injected engine.StreamFn, options agent.AgentSessionOptions) (agent.AgentSessionOptions, error) {
	if injected != nil {
		options.StreamFn = injected
		return options, nil
	}
	if parentRegistry == nil {
		return options, fmt.Errorf("subagent: parent has no model registry")
	}
	registry, ok := parentRegistry.(*config.ModelRegistry)
	if !ok {
		return options, fmt.Errorf("subagent: unsupported parent model registry %T", parentRegistry)
	}
	options.ModelRegistry = registry
	return options, nil
}

// runChildGuarded keeps one child's failure inside that child. A child runs in
// its own goroutine and reads the parent extensions.Context, whose accessors
// panic once the session is disposed or reloaded — an unrecovered panic there
// would kill the embedding process instead of failing the tool call. The panic
// text is reported, never swallowed.
// ponytail: recover because extensions.Context exposes no way to ask whether it
// is still live; drop it once the interface can be checked before the call.
func runChildGuarded(
	ctx context.Context, parent extensions.Context, injected engine.StreamFn, policy *Policy, external map[string]string, task subagentTask,
) (result string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = "", fmt.Errorf("subagent: child stopped: %v", recovered)
		}
	}()
	if command, ok := external[task.Agent]; ok {
		// Configured external CLIs are trusted host executables. Unlike integrated
		// children, they do not inherit tool filtering or the permissions policy.
		return runExternalChild(ctx, parent.CWD(), task.Agent, command, task.Task)
	}
	return runChild(ctx, parent, injected, policy, task)
}

func runExternalChild(ctx context.Context, cwd, name, command, task string) (string, error) {
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, externalTimeout)
	defer cancelTimeout()
	processCtx, cancelProcess := context.WithCancel(timeoutCtx)
	defer cancelProcess()
	process := exec.CommandContext(processCtx, "/bin/sh", "-c", `/bin/sh -c "$1" 3>&-; status=$?; printf '%d\n' "$status" >&3; while :; do sleep 3600; done`, "orb-subagent", command)
	// WaitDelay only fires when an escaped descendant still holds the pipes
	// after the group kill; keep it generous so a loaded host never truncates
	// a successful child's final output burst.
	process.Dir, process.Stdin, process.WaitDelay = cwd, strings.NewReader(task), 5*time.Second
	killGroup, err := isolateExternalProcess(process)
	if err != nil {
		return "", fmt.Errorf("subagent: external agent %q unavailable: %w", name, err)
	}
	stdout, stderr := newCappedOutput(cancelProcess), newCappedOutput(cancelProcess)
	process.Stdout, process.Stderr = stdout, stderr
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer func() { _ = statusReader.Close() }()
	process.ExtraFiles = []*os.File{statusWriter}
	if err = process.Start(); err != nil {
		_ = statusWriter.Close()
		return "", err
	}
	_ = statusWriter.Close()
	var status int
	_, statusErr := fmt.Fscan(statusReader, &status)
	cleanupErr := killGroup()
	waitErr := process.Wait()
	if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		return "", fmt.Errorf("subagent: external agent %q cleanup failed: %w", name, cleanupErr)
	}
	if stdout.exceeded.Load() {
		return "", fmt.Errorf("subagent: external agent %q stdout exceeded the %d-byte limit", name, externalOutputLimit)
	}
	if stderr.exceeded.Load() {
		return "", fmt.Errorf("subagent: external agent %q stderr exceeded the %d-byte limit", name, externalOutputLimit)
	}
	if statusErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("subagent: external agent %q stopped: %w", name, ctx.Err())
		}
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("subagent: external agent %q did not finish within %s", name, externalTimeout)
		}
		return "", fmt.Errorf("subagent: external agent %q lost its exit status: %w", name, errors.Join(statusErr, waitErr))
	}
	if status != 0 {
		// CLIs like claude -p print their failure explanation on stdout.
		detail := excerpt(stderr.String())
		if detail == "" {
			detail = excerpt(stdout.String())
		}
		return "", fmt.Errorf("subagent: external agent %q failed with status %d: %s", name, status, detail)
	}
	return stdout.String(), nil
}

// excerpt bounds a child's stderr so a crashing CLI cannot flood the model
// context with a megabyte-scale traceback.
func excerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[:4096], "") + " [truncated]"
	}
	return text
}

func runChild(ctx context.Context, parent extensions.Context, injected engine.StreamFn, policy *Policy, task subagentTask) (string, error) {
	role := archetypes[task.Agent]
	options, err := childOptions(parent.ModelRegistry(), injected, agent.AgentSessionOptions{})
	if err != nil {
		return "", err
	}
	model := parent.Model()
	if model == nil {
		return "", fmt.Errorf("subagent: parent has no model")
	}
	settingsDir, err := os.MkdirTemp("", "orb-subagent-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(settingsDir) }()
	settings, err := config.NewSettingsManager(parent.CWD(), config.WithAgentDir(settingsDir), config.WithProjectTrusted(false))
	if err != nil {
		return "", err
	}
	manager, err := sessionstore.InMemory(parent.CWD())
	if err != nil {
		return "", err
	}
	prompt := "You are the " + task.Agent + " subagent. " + role.prompt
	tools := restrictTools(role.tools, task.Tools)
	var extensionRegistry *extensions.Registry
	// ponytail: children inherit permissions only, never memory; add explicit
	// child plugin selection when callers need cross-agent memory access.
	if policy != nil {
		extensionRegistry = extensions.NewRegistry(parent.CWD())
		if err := extensionRegistry.Register("<inline:permissions>", permissionsExtension(policy, nil, parent)); err != nil {
			return "", err
		}
	}
	options.CWD, options.AgentDir, options.Model = parent.CWD(), settingsDir, model
	options.ThinkingLevel, options.Tools = ai.ModelThinkingOff, tools
	options.SessionManager, options.Settings = manager, settings
	options.Resources = &agent.Resources{SystemPrompt: &prompt}
	options.ExtensionRegistry = extensionRegistry
	result, err := agent.NewAgentSession(options)
	if err != nil {
		return "", err
	}
	defer result.Session.Dispose()
	if task.Context == "fork" {
		// ponytail: Fork sends a short text transcript, avoiding an unresolved
		// parent tool call; use a real session branch when ancestry must persist.
		messages := parent.SessionManager().BuildSessionContext().Messages
		if len(messages) > forkMessageLimit {
			messages = messages[len(messages)-forkMessageLimit:]
		}
		if transcript := forkTranscript(messages); transcript != "" {
			if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("Parent conversation:\n" + transcript)}); err != nil {
				return "", err
			}
		}
		result.Session.SyncMessagesFromSession()
	}
	if err := result.Session.PromptSync(ctx, strings.TrimSpace(task.Task)); err != nil {
		return "", err
	}
	state := result.Session.State()
	if len(state.Messages) > 0 {
		if assistant, ok := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage); ok &&
			(assistant.StopReason == ai.StopReasonError || assistant.StopReason == ai.StopReasonAborted) {
			failure := string(assistant.StopReason)
			if assistant.ErrorMessage != nil && strings.TrimSpace(*assistant.ErrorMessage) != "" {
				failure = *assistant.ErrorMessage
			}
			return "", fmt.Errorf("subagent: child failed: %s", failure)
		}
	}
	text := result.Session.GetLastAssistantText()
	if text == nil {
		return "", fmt.Errorf("subagent: child returned no final text")
	}
	return *text, nil
}

func forkTranscript(messages []json.RawMessage) string {
	lines := make([]string, 0, len(messages))
	for _, raw := range messages {
		message, err := ai.UnmarshalMessage(raw)
		if err != nil {
			continue
		}
		var role, content string
		switch typed := message.(type) {
		case *ai.UserMessage:
			role = "User"
			if typed.Content.Text != nil {
				content = *typed.Content.Text
			} else {
				content = ai.ContentText(typed.Content.Blocks)
			}
		case *ai.AssistantMessage:
			role, content = "Assistant", ai.ContentText(typed.Content)
		case *ai.ToolResultMessage:
			role, content = "Tool "+typed.ToolName, ai.ContentText(typed.Content)
		}
		if content = strings.TrimSpace(content); content != "" {
			lines = append(lines, role+": "+content)
		}
	}
	return strings.Join(lines, "\n")
}

func restrictTools(allowed, requested []string) []string {
	if requested == nil {
		return append([]string(nil), allowed...)
	}
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	result := make([]string, 0, len(requested))
	for _, name := range requested {
		if _, ok := set[name]; ok {
			result = append(result, name)
		}
	}
	return result
}
