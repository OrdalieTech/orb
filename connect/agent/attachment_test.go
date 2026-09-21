package agent

import (
	"context"
	"encoding/json"
	"github.com/OrdalieTech/orb/ai"
	"testing"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
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
	a, err := Attach(context.Background(), host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(connect.Request) bool { return true }})
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
	a, err := Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(connect.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err = a.SetGeneration("1"); err != nil {
		t.Fatal(err)
	}
	target := a.control.Target()
	request := connect.Request{Principal: connect.Principal{PeerID: "peer", Subject: connect.Subject{Kind: "controller"}}, Generation: "1", Call: connect.Call{InstanceID: a.options.InstanceID, Service: protocol.Service, Method: "prompt", SessionID: target.SessionID, Expected: connect.Expected{Generation: "1", Revision: target.Revision}, OperationID: protocol.NewID(), Args: connect.JSON(map[string]string{"text": "work"})}}
	connection, disconnect := context.WithCancel(ctx)
	raw, err := a.Invoke(connection, "call", connect.JSON(request))
	if err != nil {
		t.Fatal(err)
	}
	var receipt connect.Receipt
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
	a, err := Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &store{}, Authorize: func(connect.Request) bool { return true }})
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
	if _, err = a.observe(snapshot.Cursor, "", ""); connect.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if _, err = a.observe("", snapshot.ID, ""); connect.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
}
