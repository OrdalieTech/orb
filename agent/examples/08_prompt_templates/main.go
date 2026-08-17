// Prompt templates discovered and extended through DefaultResourceLoader.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/orb/agent"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	deployTemplate := agent.PromptTemplate{
		Name: "deploy", Description: "Deploy the application",
		FilePath: "/virtual/prompts/deploy.md",
		SourceInfo: agent.SourceInfo{
			Path: "/virtual/prompts/deploy.md", Source: "sdk", Scope: "temporary", Origin: "top-level",
		},
		Content: `# Deploy Instructions

1. Build: go build ./...
2. Test: go test ./...
3. Deploy: run the release workflow`,
	}
	loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agent.DefaultAgentDir(),
		PromptsOverride: func(current agent.ResourcePromptsResult) agent.ResourcePromptsResult {
			current.Prompts = append(current.Prompts, deployTemplate)
			return current
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}

	discovered := loader.GetPrompts().Prompts
	fmt.Println("Discovered prompt templates:")
	for _, template := range discovered {
		fmt.Printf("  /%s: %s\n", template.Name, template.Description)
	}

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Session created with %d prompt templates\n", len(discovered)+1)
	result.Session.Dispose()
}
