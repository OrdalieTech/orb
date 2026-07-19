// API key and OAuth configuration through ModelRegistry and runtime callbacks.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	defaultRegistry, err := config.NewModelRegistry(codingagent.DefaultAgentDir())
	if err != nil {
		log.Fatal(err)
	}
	defaultAuthSession := newSession(provider, defaultRegistry, nil)
	fmt.Println("Session with default model registry")
	defaultAuthSession.Dispose()

	customAgentDir, err := os.MkdirTemp("", "pi-go-sdk-auth-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(customAgentDir); err != nil {
			log.Printf("remove temporary auth directory: %v", err)
		}
	}()
	customRegistry, err := config.NewModelRegistry(customAgentDir)
	if err != nil {
		log.Fatal(err)
	}
	customAuthSession := newSession(provider, customRegistry, nil)
	fmt.Println("Session with custom auth and models location")
	customAuthSession.Dispose()

	runtimeKey := agent.GetAPIKeyFunc(func(context.Context, ai.ProviderID) (*string, error) {
		key := "sk-faux-runtime-key"
		return &key, nil
	})
	runtimeKeySession := newSession(provider, defaultRegistry, runtimeKey)
	fmt.Println("Session with runtime API key override")
	runtimeKeySession.Dispose()
}

func newSession(provider *faux.Provider, registry *config.ModelRegistry, getAPIKey agent.GetAPIKeyFunc) *codingagent.AgentSession {
	manager, err := sessionstore.InMemory(".")
	if err != nil {
		log.Fatal(err)
	}
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ModelRegistry: registry, GetAPIKey: getAPIKey, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	return result.Session
}
