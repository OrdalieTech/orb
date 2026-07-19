// Context files discovered and extended through DefaultResourceLoader.
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
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: codingagent.DefaultAgentDir(),
		AgentsFilesOverride: func(current codingagent.ResourceAgentsFilesResult) codingagent.ResourceAgentsFilesResult {
			current.AgentsFiles = append(current.AgentsFiles, codingagent.ContextFile{
				Path: "/virtual/AGENTS.md",
				Content: `# Project Guidelines

## Code Style
- Format Go with gofmt
- Keep imports used
- Prefer const when values do not change`,
			})
			return current
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}

	discovered := loader.GetAgentsFiles().AgentsFiles
	fmt.Println("Discovered context files:")
	for _, file := range discovered {
		fmt.Printf("  - %s (%d chars)\n", file.Path, len(file.Content))
	}

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Session created with %d context files\n", len(discovered)+1)
	result.Session.Dispose()
}
