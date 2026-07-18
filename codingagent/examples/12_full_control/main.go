// Full control: explicit model, session, resources, and tool selection.
//
// Mirrors upstream 12-full-control.ts: replace everything — custom model,
// custom system prompt, explicit tool allowlist, custom tools via extension
// registry, and an in-memory session.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"

	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("main.go  go.mod  go.sum")})

	model := provider.GetModel()
	prompt := "You are a minimal assistant.\nAvailable: read, bash. Be concise."

	sm, err := sessionstore.InMemory(".")
	if err != nil {
		log.Fatal(err)
	}

	// Register a custom tool via extensions.
	registry := extensions.NewRegistry(".")
	type customInfoInput struct {
		Topic string `json:"topic,omitempty" jsonschema:"description=Optional metadata topic"`
	}
	customInfoSchema, err := ai.JSONSchemaFrom[customInfoInput]()
	if err != nil {
		log.Fatal(err)
	}
	_ = registry.Register("<sdk:full-control>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name:        "custom_info",
			Label:       "Custom Info",
			Description: "Returns custom metadata",
			Parameters:  customInfoSchema,
			Execute: func(_ context.Context, _ string, _ any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					Content: ai.ToolResultContent{&ai.TextContent{Text: "custom_info_result"}},
				}, nil
			},
		})
		return nil
	})

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD:               ".",
		StreamFn:          provider.StreamSimple,
		Model:             model,
		ThinkingLevel:     ai.ModelThinkingOff,
		SessionManager:    sm,
		Resources:         &codingagent.Resources{SystemPrompt: &prompt},
		Tools:             []string{"read", "bash", "custom_info"},
		ExtensionRegistry: registry,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	// Verify tool setup.
	state := result.Session.State()
	fmt.Printf("Tools: %d\n", len(state.Tools))
	for _, t := range state.Tools {
		fmt.Printf("  - %s\n", t.Spec().Name)
	}

	result.Session.Subscribe(func(event any) {
		if end, ok := event.(codingagent.SessionAgentEndEvent); ok {
			for _, msg := range end.Messages {
				if a, ok := msg.(*ai.AssistantMessage); ok {
					for _, block := range a.Content {
						if text, ok := block.(*ai.TextContent); ok {
							fmt.Print(text.Text)
						}
					}
				}
			}
		}
	})

	if err := result.Session.Prompt(context.Background(), "List files in the current directory."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
