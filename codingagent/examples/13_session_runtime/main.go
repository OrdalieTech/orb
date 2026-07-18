// Session runtime: direct SessionRuntime usage for session replacement.
//
// Use SessionRuntime directly when you need newSession/switchSession/fork
// flows. The pattern: create via NewSessionRuntime, rebind subscriptions
// after each session replacement.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("initial response")})

	model := provider.GetModel()
	sm, err := sessionstore.InMemory(".")
	if err != nil {
		log.Fatal(err)
	}

	a := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{
			Model:         model,
			ThinkingLevel: ai.ModelThinkingOff,
		}),
		agent.WithStreamFn(provider.StreamSimple),
	)

	settings, err := config.NewSettingsManager(".")
	if err != nil {
		log.Fatal(err)
	}

	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent:          a,
		SessionManager: sm,
		Settings:       settings,
		StreamFn:       provider.StreamSimple,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer runtime.Dispose()

	unsub := runtime.Subscribe(func(event any) {
		if _, ok := event.(codingagent.QueueUpdateEvent); ok {
			fmt.Println("Queue updated")
		}
	})

	if err := runtime.Prompt(context.Background(), "hello"); err != nil {
		log.Fatal(err)
	}
	unsub()

	fmt.Println("Initial session complete")
}
