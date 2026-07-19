// Session management: in-memory, persistent, continue, list, and open.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})

	inMemoryManager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	inMemory := newSession(provider, inMemoryManager)
	sessionFile := inMemory.Session.Manager().GetSessionFile()
	if sessionFile == "" {
		sessionFile = "(none)"
	}
	fmt.Println("In-memory session:", sessionFile)
	inMemory.Session.Dispose()

	root, err := os.MkdirTemp("", "pi-go-sdk-sessions-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			log.Printf("remove temporary session directory: %v", err)
		}
	}()
	sessionDir := filepath.Join(root, "sessions")
	persistentManager, err := sessionstore.Create(cwd, sessionDir)
	if err != nil {
		log.Fatal(err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello from the persistent session")})
	persistent := newSession(provider, persistentManager)
	if err := persistent.Session.Prompt(ctx, "hello persistent"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("New session file:", persistent.Session.Manager().GetSessionFile())
	persistent.Session.Dispose()

	continuedManager, err := sessionstore.ContinueRecent(cwd, sessionDir)
	if err != nil {
		log.Fatal(err)
	}
	continued := newSession(provider, continuedManager)
	if continued.ModelFallbackMessage != "" {
		fmt.Println("Note:", continued.ModelFallbackMessage)
	}
	fmt.Println("Continued session:", continued.Session.Manager().GetSessionFile())
	continued.Session.Dispose()

	sessions := sessionstore.List(cwd, sessionDir, nil)
	fmt.Printf("\nFound %d sessions:\n", len(sessions))
	for index, info := range sessions {
		if index == 3 {
			break
		}
		id := info.ID
		if len(id) > 8 {
			id = id[:8] + "..."
		}
		message := info.FirstMessage
		if len(message) > 30 {
			message = message[:30] + "..."
		}
		fmt.Printf("  %s - %q\n", id, message)
	}
	if len(sessions) > 0 {
		openedManager, err := sessionstore.Open(sessions[0].Path, sessionDir)
		if err != nil {
			log.Fatal(err)
		}
		opened := newSession(provider, openedManager)
		fmt.Println("\nOpened:", opened.Session.Manager().GetSessionID())
		opened.Session.Dispose()
	}
}

func newSession(provider *faux.Provider, manager *sessionstore.SessionManager) *codingagent.AgentSessionResult {
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: manager.GetCWD(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	return result
}
