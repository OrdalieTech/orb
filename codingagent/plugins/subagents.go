package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

const (
	childConcurrency = 4
	forkMessageLimit = 20
)

var subagentSchema = ai.JSONSchema(`{"type":"object","properties":{"task":{"type":"string"},"agent":{"type":"string","enum":["scout","worker","reviewer"]},"mode":{"type":"string","enum":["single","parallel"]},"tasks":{"type":"array","items":{"type":"object","required":["task"],"properties":{"task":{"type":"string"},"agent":{"type":"string","enum":["scout","worker","reviewer"]},"context":{"type":"string","enum":["fresh","fork"]},"tools":{"type":"array","items":{"type":"string"}}}}},"context":{"type":"string","enum":["fresh","fork"]},"tools":{"type":"array","items":{"type":"string"}}}}`)

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

func subagentsExtension(injected agent.StreamFn) extensions.Factory {
	return func(api extensions.API) error {
		var progressMu sync.Mutex
		api.RegisterTool(extensions.ToolDefinition{
			Name: "subagent", Label: "Subagent", Description: "Run an in-process child agent", Parameters: subagentSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, extensionContext extensions.Context) (agent.AgentToolResult, error) {
				var input subagentInput
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
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
						return agent.AgentToolResult{}, fmt.Errorf("subagent: parallel mode requires tasks")
					}
				default:
					return agent.AgentToolResult{}, fmt.Errorf("subagent: mode must be single or parallel")
				}
				for index := range tasks {
					if strings.TrimSpace(tasks[index].Task) == "" {
						return agent.AgentToolResult{}, fmt.Errorf("subagent: task is required")
					}
					if tasks[index].Agent == "" {
						tasks[index].Agent = "worker"
					}
					if tasks[index].Context == "" {
						tasks[index].Context = "fresh"
					}
					if _, ok := archetypes[tasks[index].Agent]; !ok {
						return agent.AgentToolResult{}, fmt.Errorf("subagent: unknown agent %q", tasks[index].Agent)
					}
					if tasks[index].Context != "fresh" && tasks[index].Context != "fork" {
						return agent.AgentToolResult{}, fmt.Errorf("subagent: context must be fresh or fork")
					}
				}

				progress := make([]childProgress, len(tasks))
				for index, task := range tasks {
					progress[index] = childProgress{name: fmt.Sprintf("%s-%d", task.Agent, index+1), status: "queued"}
				}
				updateProgress := func(index int, status string) {
					progressMu.Lock()
					progress[index].status = status
					lines := make([]string, len(progress))
					for childIndex, child := range progress {
						lines[childIndex] = child.name + ": " + child.status
					}
					progressMu.Unlock()
					extensionContext.UI().SetWidget("subagents", &extensions.Widget{Lines: lines}, nil)
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
						results[index], errorsByChild[index] = runChild(ctx, extensionContext, injected, task)
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
						return agent.AgentToolResult{}, errorsByChild[0]
					}
					return textResult(results[0]), nil
				}
				sections := make([]string, len(tasks))
				for index, task := range tasks {
					body := results[index]
					if errorsByChild[index] != nil {
						body = "error: " + errorsByChild[index].Error()
					}
					sections[index] = fmt.Sprintf("[%d] %s\n%s", index+1, task.Agent, body)
				}
				return textResult(strings.Join(sections, "\n\n")), nil
			},
		})
		return nil
	}
}

func runChild(ctx context.Context, parent extensions.Context, injected agent.StreamFn, task subagentTask) (string, error) {
	role := archetypes[task.Agent]
	stream := injected
	if stream == nil {
		registry := parent.ModelRegistry()
		if registry == nil {
			return "", fmt.Errorf("subagent: no stream function or model registry")
		}
		stream = registry.StreamSimple
	}
	model := parent.Model()
	if model == nil {
		return "", fmt.Errorf("subagent: parent has no model")
	}
	settingsDir, err := os.MkdirTemp("", "pigo-subagent-")
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
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: parent.CWD(), AgentDir: settingsDir, Model: model, StreamFn: stream,
		ThinkingLevel: ai.ModelThinkingOff, Tools: tools, SessionManager: manager, Settings: settings,
		Resources: &codingagent.Resources{SystemPrompt: &prompt},
	})
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
			role, content = "User", ai.ContentText(typed.Content.Blocks)
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
