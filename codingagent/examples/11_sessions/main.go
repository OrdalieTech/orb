// Session management: in-memory, persistent, continue.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello from session 1")})

	// In-memory session (no persistence)
	sm, err := sessionstore.InMemory(".")
	if err != nil {
		log.Fatal(err)
	}
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          provider.GetModel(),
		SessionManager: sm,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("In-memory session created")
	if err := result.Session.Prompt(context.Background(), "hello"); err != nil {
		log.Fatal(err)
	}
	result.Session.Dispose()

	// Persistent session
	tmpDir, _ := os.MkdirTemp("", "pi-example-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sm2, err := sessionstore.Create(tmpDir, tmpDir)
	if err != nil {
		log.Fatal(err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello from session 2")})
	result2, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          provider.GetModel(),
		SessionManager: sm2,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Persistent session created")
	result2.Session.Dispose()
}
