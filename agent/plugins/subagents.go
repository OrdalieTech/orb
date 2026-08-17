package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

const (
	childConcurrency = 4
	forkMessageLimit = 20
	// ponytail: one flat width cap, because `tasks` is model-controlled and each
	// entry costs a goroutine, a temp dir, a session, and a real provider call.
	// Uncapped, one tool call fans out as wide as the model asks. A per-session
	// or per-run budget is the upgrade path when 32 stops being enough.
	maxParallelTasks = 32
)

// ponytail: `mode` is the only unconditionally required field — task/tasks are
// required per branch, which plain JSON Schema cannot say without a oneOf that
// several providers reject. Execute rejects the wrong pairing with a clear error.
var subagentSchema = ai.JSONSchema(`{"type":"object","required":["mode"],"properties":{"mode":{"type":"string","enum":["single","parallel"],"description":"single runs one child from task/agent; parallel runs every entry of tasks concurrently."},"task":{"type":"string","description":"Self-contained instruction for the child, including any context it needs. Required when mode is single."},"agent":{"type":"string","enum":["scout","worker","reviewer"],"description":"Child role: scout and reviewer are read-only, worker may edit and run commands. Defaults to worker."},"context":{"type":"string","enum":["fresh","fork"],"description":"fresh starts with an empty conversation; fork prepends a transcript of the recent parent conversation. Defaults to fresh."},"tools":{"type":"array","items":{"type":"string"},"description":"Optional subset of the role's tools; names outside the role are dropped, and an empty array leaves the child with no tools."},"tasks":{"type":"array","maxItems":32,"description":"Children to run concurrently, at most 32 per call. Required when mode is parallel; ignored otherwise.","items":{"type":"object","required":["task"],"properties":{"task":{"type":"string","description":"Self-contained instruction for this child."},"agent":{"type":"string","enum":["scout","worker","reviewer"],"description":"Child role for this task. Defaults to worker."},"context":{"type":"string","enum":["fresh","fork"],"description":"fresh or fork for this task. Defaults to fresh."},"tools":{"type":"array","items":{"type":"string"},"description":"Optional subset of this role's tools."}}}}}}`)

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

func subagentsExtension(injected engine.StreamFn, policy *Policy) extensions.Factory {
	return func(api extensions.API) error {
		var progressMu sync.Mutex
		api.RegisterTool(extensions.ToolDefinition{
			Name: "subagent", Label: "Subagent", Description: "Run an in-process child agent", Parameters: subagentSchema,
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
					if _, ok := archetypes[tasks[index].Agent]; !ok {
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
						results[index], errorsByChild[index] = runChildGuarded(ctx, extensionContext, injected, policy, task)
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
	ctx context.Context, parent extensions.Context, injected engine.StreamFn, policy *Policy, task subagentTask,
) (result string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = "", fmt.Errorf("subagent: child stopped: %v", recovered)
		}
	}()
	return runChild(ctx, parent, injected, policy, task)
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
