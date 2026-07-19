// Custom system prompt replacement and extension through DefaultResourceLoader.
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
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	agentDir := codingagent.DefaultAgentDir()
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	piratePrompt := "You are a helpful assistant that speaks like a pirate.\nAlways end responses with \"Arrr!\""
	loader1, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD:      cwd,
		AgentDir: agentDir,
		SystemPromptOverride: func(*string) *string {
			return &piratePrompt
		},
		AppendSystemPromptOverride: func([]string) []string { return []string{} },
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader1.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("4, matey! Arrr!")})
	session1 := newSession(cwd, provider, loader1)
	fmt.Println("=== Replace prompt ===")
	if err := session1.Prompt(ctx, "What is 2 + 2?"); err != nil {
		log.Fatal(err)
	}
	fmt.Print("\n\n")
	session1.Dispose()

	loader2, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD:      cwd,
		AgentDir: agentDir,
		AppendSystemPromptOverride: func(base []string) []string {
			return append(base, "## Additional Instructions\n- Always be concise\n- Use bullet points when listing things")
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader2.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("- Safer refactoring\n- Better tooling\n- Clearer contracts")})
	session2 := newSession(cwd, provider, loader2)
	fmt.Println("=== Modify prompt ===")
	if err := session2.Prompt(ctx, "List 3 benefits of TypeScript."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	session2.Dispose()
}

func newSession(cwd string, provider *faux.Provider, loader codingagent.ResourceLoader) *codingagent.AgentSession {
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	result.Session.Subscribe(func(event any) {
		if end, ok := event.(codingagent.SessionAgentEndEvent); ok {
			printAssistantText(end.Messages)
		}
	})
	return result.Session
}

func printAssistantText(messages agent.AgentMessages) {
	for _, message := range messages {
		assistant, ok := message.(*ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			if text, ok := block.(*ai.TextContent); ok {
				fmt.Print(text.Text)
			}
		}
	}
}
