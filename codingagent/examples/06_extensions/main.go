// Extensions configuration: event interception and custom tools.
//
// Mirrors upstream 06-extensions.ts: register an inline extension that
// intercepts agent_start events and registers a custom tool.
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
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("main.go")})

	// Create an extension registry and register an inline extension.
	registry := extensions.NewRegistry(".")
	if err := registry.Register("<inline:logger>", func(api extensions.API) error {
		// Intercept agent_start events.
		api.On(extensions.EventAgentStart, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			fmt.Println("[Extension] Agent starting")
			return nil, nil
		})

		// Register a custom tool.
		api.RegisterTool(extensions.ToolDefinition{
			Name:        "my_tool",
			Label:       "My Tool",
			Description: "Does something useful",
			Parameters:  []byte(`{"type":"object","properties":{"input":{"type":"string"}}}`),
			Execute: func(_ context.Context, _ string, params any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				m, _ := params.(map[string]any)
				return agent.AgentToolResult{
					Content: ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf("Processed: %v", m["input"])}},
				}, nil
			},
		})
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn:          provider.StreamSimple,
		Model:             provider.GetModel(),
		ExtensionRegistry: registry,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

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
