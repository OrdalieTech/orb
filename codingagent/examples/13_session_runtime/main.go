// Session runtime demonstrates replacing the active AgentSession while a host
// keeps session-local subscriptions and extension bindings current.
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
	root, err := os.MkdirTemp("", "pi-go-sdk-runtime-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			log.Printf("remove temporary runtime directory: %v", err)
		}
	}()

	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("initial response")})
	manager, err := sessionstore.Create(root, filepath.Join(root, "sessions"))
	if err != nil {
		log.Fatal(err)
	}
	host, err := codingagent.NewAgentSessionRuntime(ctx, codingagent.AgentSessionOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), SessionManager: manager,
		StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer host.Dispose(ctx)

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
	host.SetRebindSession(bindSession)
	if err := bindSession(host.Session()); err != nil {
		log.Fatal(err)
	}
	if err := host.Session().PromptSync(ctx, "hello"); err != nil {
		log.Fatal(err)
	}
	original := host.Session().Manager().GetSessionFile()
	fmt.Println("Initial session:", original)

	if _, err := host.NewSession(ctx, nil); err != nil {
		log.Fatal(err)
	}
	fmt.Println("After NewSession():", host.Session().Manager().GetSessionFile())

	if original != "" {
		if _, err := host.SwitchSession(ctx, original, nil); err != nil {
			log.Fatal(err)
		}
		fmt.Println("After SwitchSession():", host.Session().Manager().GetSessionFile())
	}
	if unsubscribe != nil {
		unsubscribe()
	}
}
