// Minimal SDK usage with all defaults.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("Here are the files in the current directory: main.go")})

	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	result.Session.Subscribe(func(event any) {
		if end, ok := event.(agent.SessionAgentEndEvent); ok {
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

	if err := result.Session.Prompt(context.Background(), "What files are in the current directory?"); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	for _, message := range result.Session.Agent().State().Messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(encoded))
	}
	fmt.Println()
}
