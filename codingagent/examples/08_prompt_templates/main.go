// Prompt templates discovered and extended through DefaultResourceLoader.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	deployTemplate := codingagent.PromptTemplate{
		Name: "deploy", Description: "Deploy the application",
		FilePath: "/virtual/prompts/deploy.md",
		SourceInfo: codingagent.SourceInfo{
			Path: "/virtual/prompts/deploy.md", Source: "sdk", Scope: "temporary", Origin: "top-level",
		},
		Content: `# Deploy Instructions

1. Build: go build ./...
2. Test: go test ./...
3. Deploy: run the release workflow`,
	}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: codingagent.DefaultAgentDir(),
		PromptsOverride: func(current codingagent.ResourcePromptsResult) codingagent.ResourcePromptsResult {
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
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Session created with %d prompt templates\n", len(discovered)+1)
	result.Session.Dispose()
}
