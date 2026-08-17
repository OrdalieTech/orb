// Settings configuration through SettingsManager.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	agentDir := agent.DefaultAgentDir()
	settingsFromDisk, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		log.Fatal(err)
	}
	current, err := json.MarshalIndent(settingsFromDisk.GetGlobalSettings(), "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Current settings:", string(current))

	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		log.Fatal(err)
	}
	settings.SetCompactionEnabled(false)
	settings.SetRetryEnabled(true)

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		Settings: settings, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Session created with custom settings")
	result.Session.Dispose()

	settings.SetDefaultThinkingLevel(ai.ModelThinkingLow)
	for _, settingsErr := range settings.DrainErrors() {
		fmt.Printf("Warning (%s settings): %v\n", settingsErr.Scope, settingsErr.Err)
	}
}
