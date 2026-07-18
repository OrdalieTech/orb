// Minimal SDK usage with all defaults.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("Here are the files in the current directory: main.go")})

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
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

	if err := result.Session.Prompt(context.Background(), "What files are in the current directory?"); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
