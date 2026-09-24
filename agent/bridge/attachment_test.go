package bridge

import (
	"context"
	"encoding/json"
	"github.com/OrdalieTech/orb/ai"
	"strings"
	"testing"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
)

type store struct{ b []byte }

func (s *store) Load() ([]byte, error) { return s.b, nil }
func (s *store) Save(b []byte) error   { s.b = append([]byte(nil), b...); return nil }
func TestCloseAttachmentKeepsRuntime(t *testing.T) {
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	host, err := runtime.NewAgentSessionRuntime(context.Background(), runtime.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(context.Background())
	a, err := Attach(context.Background(), host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(bridge.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	if _, err = host.NewSession(context.Background(), nil); err != nil {
		t.Fatal("attachment disposed runtime", err)
	}
}

func TestAcceptedWorkOutlivesConnectionAndAttachment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	started, release := make(chan struct{}), make(chan struct{})
	provider := faux.New(faux.Options{})
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
		close(started)
		<-release
		return faux.AssistantMessage("finished independently"), nil
	})})
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	a, err := Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(bridge.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err = a.SetGeneration("1"); err != nil {
		t.Fatal(err)
	}
	target := a.control.Target()
	request := bridge.Request{Principal: bridge.Principal{PeerID: "peer", Subject: bridge.Subject{Kind: "controller"}}, Generation: "1", Call: bridge.Call{InstanceID: a.options.InstanceID, Service: protocol.Service, Method: "prompt", SessionID: target.SessionID, Expected: bridge.Expected{Generation: "1", Revision: target.Revision}, OperationID: protocol.NewID(), Args: bridge.JSON(map[string]string{"text": "work"})}}
	connection, disconnect := context.WithCancel(ctx)
	raw, err := a.Invoke(connection, "call", bridge.JSON(request))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bridge.Receipt
	if err = json.Unmarshal(raw, &receipt); err != nil || receipt.Status != "accepted" {
		t.Fatal(receipt, err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("dispatch did not start")
	}
	disconnect()
	_ = a.Close()
	close(release)
	for {
		receipt, err = a.ledger.Get(request.Principal, request.Call.InstanceID, request.Call.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Status == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(receipt)
		case <-time.After(time.Millisecond):
		}
	}
	if _, err = host.NewSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotExpiresWhenLocalTranscriptResets(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	a, err := Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(bridge.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	raw, err := a.observe("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ID     string `json:"snapshot_id"`
		Cursor string `json:"cursor"`
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	host.Session().Agent().SetMessages(nil)
	if _, err = a.observe(snapshot.Cursor, "", ""); bridge.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if _, err = a.observe("", snapshot.ID, ""); bridge.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
}

func BenchmarkBridgeStreaming(b *testing.B) {
	for _, attached := range []bool{false, true} {
		name := "detached"
		if attached {
			name = "attached"
		}
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			cwd := b.TempDir()
			manager, err := session.InMemory(cwd)
			if err != nil {
				b.Fatal(err)
			}
			provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(4096)})
			host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{CWD: cwd, AgentDir: b.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
			if err != nil {
				b.Fatal(err)
			}
			defer host.Dispose(ctx)
			var attachment *Attachment
			if attached {
				attachment, err = Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(bridge.Request) bool { return true }})
				if err != nil {
					b.Fatal(err)
				}
				defer func() { _ = attachment.Close() }()
			}
			message := faux.AssistantMessage(strings.Repeat("code with <tags> and Unicode λ\n", 2048))
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				host.Session().Agent().SetMessages(nil)
				provider.SetResponses([]faux.ResponseStep{message})
				if attachment != nil {
					attachment.mu.Lock()
					attachment.messages = nil
					attachment.messageBytes = 0
					attachment.mu.Unlock()
				}
				b.StartTimer()
				if err := host.Session().Agent().Prompt(ctx, "stream"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
