// Context files (AGENTS.md) loaded into the system prompt.
package main

import (
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Resources: &codingagent.Resources{
			ContextFiles: []codingagent.ContextFile{
				{
					Path:    "/virtual/AGENTS.md",
					Content: "# Project Guidelines\n\n## Code Style\n- Use Go strict formatting\n- No unused imports",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	fmt.Println("Session created with 1 context file")
}
