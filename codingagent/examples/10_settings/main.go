// Settings configuration via SettingsManager.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	cwd, _ := os.Getwd()
	settings, err := config.NewSettingsManager(cwd)
	if err != nil {
		log.Fatal(err)
	}

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Settings: settings,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	if errs := settings.DrainErrors(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Printf("Warning: %v\n", e)
		}
	}

	fmt.Println("Session created with explicit settings")
}
