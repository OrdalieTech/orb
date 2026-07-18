// Skills configuration: discover, filter, or add custom skills.
package main

import (
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	customSkill := codingagent.Skill{
		Name:        "my-skill",
		Description: "Custom project instructions",
	}

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Resources: &codingagent.Resources{
			Skills: []codingagent.Skill{customSkill},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	fmt.Printf("Session created with skill: %s\n", customSkill.Name)
}
