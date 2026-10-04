package bridge

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/document"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
)

// attached is a faux-model runtime with an attachment at generation 1.
func attached(t *testing.T, steps ...faux.ResponseStep) (*Attachment, *runtime.AgentSessionRuntime) {
	t.Helper()
	ctx := t.Context()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	provider.SetResponses(steps)
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	a, err := Attach(ctx, host, Options{InstanceID: protocol.NewID(), Store: &document.Memory{}, Authorize: func(bridge.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err = a.SetGeneration("1"); err != nil {
		t.Fatal(err)
	}
	return a, host
}

var peer = bridge.Principal{PeerID: "peer", Subject: bridge.Subject{Kind: "controller"}}

// request is a call on the attached instance's current session.
func request(a *Attachment, method string, args any) bridge.Request {
	target := a.control.Target()
	return bridge.Request{Principal: peer, Generation: "1", Call: bridge.Call{InstanceID: a.options.InstanceID, Service: protocol.Service, Method: method, SessionID: target.SessionID, Expected: bridge.Expected{Generation: "1", Revision: target.Revision}, OperationID: protocol.NewID(), Args: bridge.JSON(args)}}
}

// settle waits for a call's receipt to leave accepted and running.
func settle(t *testing.T, a *Attachment, r bridge.Request) bridge.Receipt {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		receipt, err := a.ledger.Get(r.Principal, r.Call.InstanceID, r.Call.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Status != "accepted" && receipt.Status != "running" || time.Now().After(deadline) {
			return receipt
		}
	}
}

// call sends a call and waits for its outcome; a call refused up front reports its error code.
func call(t *testing.T, a *Attachment, method string, args any) (bridge.Receipt, string) {
	t.Helper()
	r := request(a, method, args)
	if _, err := a.Invoke(t.Context(), "call", bridge.JSON(r)); err != nil {
		return bridge.Receipt{}, bridge.Code(err)
	}
	return settle(t, a, r), ""
}

func TestCloseAttachmentKeepsRuntime(t *testing.T) {
	a, host := attached(t)
	_ = a.Close()
	if _, err := host.NewSession(context.Background(), nil); err != nil {
		t.Fatal("attachment disposed runtime", err)
	}
}

func TestAcceptedWorkOutlivesConnectionAndAttachment(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a, host := attached(t, faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
		close(started)
		<-release
		return faux.AssistantMessage("finished independently"), nil
	}))
	r := request(a, "prompt", map[string]string{"text": "work"})
	connection, disconnect := context.WithCancel(t.Context())
	raw, err := a.Invoke(connection, "call", bridge.JSON(r))
	var receipt bridge.Receipt
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Status != "accepted" {
		t.Fatal(receipt, err)
	}
	<-started
	disconnect()
	_ = a.Close()
	close(release)
	if receipt = settle(t, a, r); receipt.Status != "succeeded" {
		t.Fatal(receipt)
	}
	if _, err = host.NewSession(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotExpiresWhenLocalTranscriptResets(t *testing.T) {
	a, host := attached(t)
	raw, err := a.observe("", "", "")
	var snapshot struct {
		ID     string `json:"snapshot_id"`
		Cursor string `json:"cursor"`
	}
	if err != nil || json.Unmarshal(raw, &snapshot) != nil {
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

func TestAPeerRenamesTheSession(t *testing.T) {
	a, host := attached(t)
	if receipt, _ := call(t, a, "session.name", map[string]string{"name": "  Pairing race  "}); receipt.Status != "succeeded" {
		t.Fatal(receipt)
	}
	if name := host.Session().Manager().GetSessionName(); name == nil || *name != "Pairing race" {
		t.Fatalf("name = %v", name)
	}
	if _, code := call(t, a, "session.name", map[string]string{"name": "   "}); code != "invalid_params" {
		t.Fatal("blank name accepted:", code)
	}
}

// A follower long-polls: the call waits while nothing happens, and answers at once when the
// conversation changes, with the stream's events and the instance's new pulse.
func TestAFollowerWaitsForTheConversationToChange(t *testing.T) {
	a, host := attached(t)
	raw, _ := a.observe("", "", "")
	var first struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(raw, &first)
	start, _ := a.replay(first.Cursor, protocol.MaxPage)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = host.Session().SetSessionName("renamed")
	}()
	began := time.Now()
	raw, err := a.Invoke(t.Context(), "events.subscribe", bridge.JSON(map[string]any{"principal": peer, "params": map[string]any{"instance_id": a.options.InstanceID, "cursor": first.Cursor, "wait": true, "state": start.State}}))
	var got page
	if err != nil || json.Unmarshal(raw, &got) != nil {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited < 200*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("answered after %v", waited)
	}
	if got.State == start.State || len(got.Events) != 1 || !strings.Contains(string(got.Events[0].Data), `"session_info_changed"`) {
		t.Fatalf("after a rename: %+v", got)
	}
}

// A controller compacts and runs a shell command as RPC's compact and bash would; the command
// and its output join the conversation, and the descriptor offers both with the instance's
// commands, reasoning and usage.
func TestAControllerRunsAShellCommandInTheConversation(t *testing.T) {
	a, host := attached(t)
	var d Descriptor
	if json.Unmarshal(a.inspect(), &d) != nil || !d.Waits || d.Stats == nil || d.Thinking == "" || !strings.Contains(strings.Join(d.Methods, " "), "session.compact shell") {
		t.Fatalf("descriptor = %+v", d)
	}
	if goruntime.GOOS == "js" || goruntime.GOOS == "wasip1" {
		t.Skip("no processes to run a shell in")
	}
	if receipt, _ := call(t, a, "shell", map[string]string{"command": "echo from-the-phone"}); receipt.Status != "succeeded" || !strings.Contains(string(receipt.Result), "from-the-phone") {
		t.Fatal(receipt)
	}
	messages, _ := json.Marshal(host.Session().State().Messages)
	if !strings.Contains(string(messages), `"bashExecution"`) {
		t.Fatalf("messages = %s", messages)
	}
	if _, code := call(t, a, "shell", map[string]string{"command": " "}); code != "invalid_params" {
		t.Fatal("blank command accepted:", code)
	}
}
