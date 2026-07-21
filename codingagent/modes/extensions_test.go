package modes

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/permissiongate"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/pirate"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/statusline"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestCompiledDemosBehaveInHeadlessPrintAndJSON(t *testing.T) {
	for _, outputMode := range []PrintOutputMode{PrintOutputText, PrintOutputJSON} {
		t.Run(string(outputMode), func(t *testing.T) {
			cwd := t.TempDir()
			manager, err := session.InMemory(cwd)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			registry := extensions.NewRegistry(cwd)
			for _, entry := range []struct {
				path    string
				factory extensions.Factory
			}{
				{"<inline:permission-gate>", permissiongate.Extension},
				{"<inline:pirate>", pirate.Extension},
				{"<inline:status-line>", statusline.Extension},
			} {
				if err := registry.Register(entry.path, entry.factory); err != nil {
					t.Fatal(err)
				}
			}
			provider := faux.New()
			provider.SetResponses([]faux.ResponseStep{
				faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "sudo true"}, faux.ToolCallOptions{ID: "danger"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
				faux.AssistantMessage("safe"),
			})
			var executions atomic.Int64
			bash := agent.AgentToolFunc{
				AgentToolSpec: agent.AgentToolSpec{Name: "bash", Label: "bash", Description: "bash", Parameters: jsonschema.Schema(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`)},
				Run: func(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
					executions.Add(1)
					return agent.AgentToolResult{}, nil
				},
			}
			var seenPrompt string
			stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
				if request.SystemPrompt != nil {
					seenPrompt = *request.SystemPrompt
				}
				return provider.StreamSimple(ctx, model, request, options)
			}
			promptOptions := codingagent.SystemPromptOptions{CWD: cwd, SelectedTools: []string{"bash"}, ToolSnippets: map[string]string{"bash": "bash"}}
			created := agent.NewAgent(agent.WithInitialState(agent.AgentState{SystemPrompt: "base", Model: provider.GetModel(), Tools: []agent.AgentTool{bash}}), agent.WithStreamFn(stream), agent.WithConvertToLLM(codingagent.ConvertToLLM))
			extensionMode := extensions.ModePrint
			if outputMode == PrintOutputJSON {
				extensionMode = extensions.ModeJSON
			}
			runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
				Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
				ExtensionMode: extensionMode, BaseTools: []agent.AgentTool{bash},
				InitialActiveToolNames: []string{"bash"}, SystemPromptOptions: &promptOptions,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Dispose()
			var stdout, stderr bytes.Buffer
			code := RunPrintMode(context.Background(), runtime, PrintModeOptions{
				Mode: outputMode, Messages: []string{"/pirate", "go"}, Stdout: &stdout, Stderr: &stderr,
			})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if executions.Load() != 0 {
				t.Fatalf("dangerous command executed %d times", executions.Load())
			}
			if !strings.Contains(seenPrompt, pirate.PromptSuffix) {
				t.Fatalf("pirate prompt missing: %q", seenPrompt)
			}
			if outputMode == PrintOutputText {
				if stdout.String() != "safe\n" {
					t.Fatalf("text output = %q", stdout.String())
				}
			} else if !strings.Contains(stdout.String(), `"type":"agent_settled"`) || !strings.Contains(stdout.String(), `"text":"safe"`) {
				t.Fatalf("json output = %q", stdout.String())
			}
		})
	}
}
