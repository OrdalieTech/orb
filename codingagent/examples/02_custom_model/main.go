// Custom model selection and thinking level.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("Hello! I'm running with a custom model and medium thinking.")})

	model := provider.GetModel()
	fmt.Printf("Model: %s/%s\n", model.Provider, model.ID)

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn:      provider.StreamSimple,
		Model:         model,
		ThinkingLevel: ai.ModelThinkingMedium,
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

	if err := result.Session.Prompt(context.Background(), "Say hello in one sentence."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
