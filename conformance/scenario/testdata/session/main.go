// Command session runs one scripted agent turn with every host port supplied in memory,
// and prints the host-independent projection of the resulting session.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/platforms/memory"
)

type document struct {
	mu   sync.Mutex
	data []byte
}

func (d *document) Read(context.Context) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data, nil
}

func (d *document) Update(_ context.Context, update func([]byte) ([]byte, error)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	next, err := update(d.data)
	if err == nil {
		d.data = next
	}
	return err
}

type store map[string]*document

func (s store) Document(path string) host.Document {
	if s[path] == nil {
		s[path] = &document{}
	}
	return s[path]
}

func main() {
	if err := run(); err != nil {
		fmt.Println("scenario error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	const cwd, agentDir = "/workspace", "/agent"
	files := memory.New(memory.Options{})
	platform := &host.Host{AgentDir: agentDir, FS: files, Store: store{}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("write", map[string]any{"path": "notes/hello.txt", "content": "portable core\n"}, faux.ToolCallOptions{ID: "call-write"})),
		faux.AssistantMessage(faux.ToolCall("read", map[string]any{"path": "notes/hello.txt"}, faux.ToolCallOptions{ID: "call-read"})),
		faux.AssistantMessage("hello from every host"),
	})
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Model: provider.GetModel(), ThinkingLevel: ai.ModelThinkingOff,
		StreamFn: provider.StreamSimple, Host: platform,
	})
	if err != nil {
		return fmt.Errorf("agent session: %w", err)
	}
	defer result.Session.Dispose()
	if err := result.Session.Prompt(ctx, "Say hello."); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	for _, entry := range result.Session.Manager().GetEntries() {
		if entry.Type != "message" {
			fmt.Println(entry.Type)
			continue
		}
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return err
		}
		fmt.Printf("message %s %s\n", message.Role, message.Content)
	}
	fmt.Printf("tools %v\n", result.Session.GetActiveToolNames())
	for _, path := range slices.Sorted(maps.Keys(files.Snapshot())) {
		if strings.HasSuffix(path, ".jsonl") {
			fmt.Println("journal", path[:strings.LastIndex(path, "/")])
			continue
		}
		fmt.Printf("file %s %q\n", path, files.Snapshot()[path])
	}
	fmt.Println("scenario OK")
	return nil
}
