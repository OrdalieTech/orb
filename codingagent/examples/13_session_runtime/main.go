// Session runtime demonstrates recreating cwd-bound services when the active
// AgentSession is replaced and rebinding session-local host state.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func main() {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "pigo-sdk-runtime-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			log.Printf("remove temporary runtime directory: %v", err)
		}
	}()

	agentDir := filepath.Join(root, "agent")
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	manager, err := sessionstore.Create(root, filepath.Join(root, "sessions"))
	if err != nil {
		log.Fatal(err)
	}
	createRuntime := codingagent.CreateAgentSessionRuntimeFactory(func(_ context.Context, options codingagent.AgentSessionOptions) (*codingagent.AgentSessionResult, error) {
		services, err := codingagent.CreateAgentSessionServices(codingagent.CreateAgentSessionServicesOptions{
			CWD: options.CWD, AgentDir: options.AgentDir,
		})
		if err != nil {
			return nil, err
		}
		return codingagent.CreateAgentSessionFromServices(codingagent.CreateAgentSessionFromServicesOptions{
			Services: services, SessionManager: options.SessionManager,
			SessionStartEvent: options.SessionStartEvent, Model: provider.GetModel(),
			ThinkingLevel: options.ThinkingLevel, ScopedModels: options.ScopedModels,
			Tools: options.Tools, ExcludeTools: options.ExcludeTools,
			NoTools: options.NoTools, CustomTools: options.CustomTools,
		})
	})
	runtime, err := codingagent.NewAgentSessionRuntime(ctx, codingagent.AgentSessionOptions{
		CWD: root, AgentDir: agentDir, SessionManager: manager,
	}, createRuntime)
	if err != nil {
		log.Fatal(err)
	}
	defer runtime.Dispose(ctx)

	var unsubscribe func()
	bindSession := func(session *codingagent.AgentSession) error {
		if unsubscribe != nil {
			unsubscribe()
		}
		if err := session.BindExtensions(ctx); err != nil {
			return err
		}
		unsubscribe = session.Subscribe(func(event any) {
			if queue, ok := event.(codingagent.QueueUpdateEvent); ok {
				fmt.Println("Queued:", len(queue.Steering)+len(queue.FollowUp))
			}
		})
		return nil
	}
	runtime.SetRebindSession(bindSession)
	if err := bindSession(runtime.Session()); err != nil {
		log.Fatal(err)
	}

	originalSessionFile := runtime.Session().Manager().GetSessionFile()
	fmt.Println("Initial session:", originalSessionFile)
	if _, err := runtime.NewSession(ctx, nil); err != nil {
		log.Fatal(err)
	}
	fmt.Println("After NewSession():", runtime.Session().Manager().GetSessionFile())
	if originalSessionFile != "" {
		if _, err := runtime.SwitchSession(ctx, originalSessionFile, nil); err != nil {
			log.Fatal(err)
		}
		fmt.Println("After SwitchSession():", runtime.Session().Manager().GetSessionFile())
	}
	if unsubscribe != nil {
		unsubscribe()
	}
}
