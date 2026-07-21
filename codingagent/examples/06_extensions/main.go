// Extensions configuration through DefaultResourceLoader inline factories.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: codingagent.DefaultAgentDir(),
		ExtensionFactories: []extensions.Factory{func(api extensions.API) error {
			api.On(extensions.EventAgentStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				fmt.Println("[Inline Extension] Agent starting")
				return nil, nil
			})
			api.RegisterTool(extensions.ToolDefinition{
				Name: "my_tool", Label: "My Tool", Description: "Does something useful",
				Parameters: []byte(`{"type":"object","properties":{"input":{"type":"string"}}}`),
				Execute: func(_ context.Context, _ string, params any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
					input := ""
					if values, ok := params.(map[string]any); ok {
						input, _ = values["input"].(string)
					}
					return agent.AgentToolResult{
						Content: ai.ToolResultContent{&ai.TextContent{Text: "Processed: " + input}},
					}, nil
				},
			})
			return nil
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("main.go")})
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()
	result.Session.Subscribe(func(event any) {
		if end, ok := event.(codingagent.SessionAgentEndEvent); ok {
			for _, message := range end.Messages {
				assistant, ok := message.(*ai.AssistantMessage)
				if !ok {
					continue
				}
				for _, block := range assistant.Content {
					if text, ok := block.(*ai.TextContent); ok {
						fmt.Print(text.Text)
					}
				}
			}
		}
	})

	if err := result.Session.Prompt(ctx, "List files in the current directory."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
