// Prompt templates: file-based templates invoked with /templatename.
package main

import (
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	deployTemplate := codingagent.PromptTemplate{
		Name:        "deploy",
		Description: "Deploy the application",
	}

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Resources: &codingagent.Resources{
			PromptTemplates: []codingagent.PromptTemplate{deployTemplate},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	fmt.Printf("Session created with template: /%s\n", deployTemplate.Name)
}
