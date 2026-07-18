// API key and auth configuration via GetAPIKey callback.
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

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		GetAPIKey: func(_ context.Context, _ ai.ProviderID) (*string, error) {
			key := "sk-faux-key"
			return &key, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	fmt.Println("Session created with custom API key provider")
}
