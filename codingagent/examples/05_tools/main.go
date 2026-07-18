// Tools configuration: tool allowlists and denylists.
//
// Mirrors upstream 05-tools.ts: restrict built-in tools via allowlist,
// disable all tools, and combine with custom CWD.
package main

import (
	"fmt"
	"log"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func main() {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	// Read-only mode: only allow read, grep, find, ls.
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Tools:    []string{"read", "grep", "find", "ls"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Read-only session: %d tools\n", len(result.Session.State().Tools))
	result.Session.Dispose()

	// Custom tool selection.
	result2, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Tools:    []string{"read", "bash", "grep"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Custom tools session: %d tools\n", len(result2.Session.State().Tools))
	result2.Session.Dispose()

	// Exclude specific tools (denylist).
	result3, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn:     provider.StreamSimple,
		Model:        provider.GetModel(),
		ExcludeTools: []string{"write", "edit"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Excluded write/edit: %v\n", names(result3.Session.State().Tools))
	result3.Session.Dispose()

	// No tools at all.
	result4, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		NoTools:  "all",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("No tools: %d tools\n", len(result4.Session.State().Tools))
	result4.Session.Dispose()

	// Custom CWD with tools.
	result5, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD:      "/tmp",
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Tools:    []string{"read", "bash"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Custom CWD /tmp: %d tools\n", len(result5.Session.State().Tools))
	result5.Session.Dispose()
}

func names(tools []agent.AgentTool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Spec().Name
	}
	return out
}
