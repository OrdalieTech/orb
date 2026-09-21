package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/connect"
	attach "github.com/OrdalieTech/orb/connect/agent"
	"github.com/OrdalieTech/orb/connect/protocol"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBridgeProfileNames(t *testing.T) {
	for _, s := range []string{"../work", "", "a/b", "a b"} {
		if validBridgeName(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"personal", "work-2", "local_test"} {
		if !validBridgeName(s) {
			t.Fatal(s)
		}
	}
}

func TestBridgeRuntimeReceiptReconnectAndSessionFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bStore := &testBridgeStore{}
	b, err := bridge.Open(bStore, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	phone, err := bridge.Open(&testBridgeStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = phone.Close() }()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("remote answer")})
	host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	enrolled, token, err := b.Enroll("test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	a, err := attach.Attach(ctx, host, attach.Options{InstanceID: enrolled.ID, Store: &testBridgeStore{}, Authorize: b.Authorize})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	gen, err := b.Attach(enrolled.ID, token, protocol.NewID(), connect.NewLocal(a.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SetGeneration(gen); err != nil {
		t.Fatal(err)
	}
	if err = b.AddGrant(bridge.Grant{Principal: phone.Principal(), Instances: []string{enrolled.ID}, Permissions: []string{"instance.inspect", "instance.prompt", "instance.session.manage"}}); err != nil {
		t.Fatal(err)
	}
	control, _ := host.EnableControl()
	target := control.Target()
	call := connect.Call{InstanceID: enrolled.ID, Service: protocol.Service, Method: "prompt", SessionID: target.SessionID, Expected: connect.Expected{Generation: gen, Revision: target.Revision}, OperationID: protocol.NewID(), Args: connect.JSON(map[string]string{"text": "hello"})}
	raw, err := b.Call(ctx, phone.Principal(), call)
	if err != nil {
		t.Fatal(err)
	}
	var receipt connect.Receipt
	_ = json.Unmarshal(raw, &receipt)
	if receipt.Status != "accepted" {
		t.Fatal(string(raw))
	}
	get := connect.JSON(map[string]any{"principal": phone.Principal(), "params": map[string]string{"instance_id": enrolled.ID, "operation_id": call.OperationID}})
	for receipt.Status == "accepted" || receipt.Status == "running" {
		select {
		case <-ctx.Done():
			t.Fatal("receipt did not finish")
		case <-time.After(time.Millisecond):
		}
		raw, err = a.Invoke(ctx, "operations.get", get)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &receipt)
	}
	if receipt.Status != "succeeded" {
		t.Fatal(string(raw))
	}
	b.Detach(enrolled.ID, gen)
	next, err := b.Attach(enrolled.ID, token, protocol.NewID(), connect.NewLocal(a.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	_ = a.SetGeneration(next)
	raw, err = b.Call(ctx, phone.Principal(), call)
	if err != nil {
		t.Fatal("old-generation identical retry rejected", err)
	}
	_ = json.Unmarshal(raw, &receipt)
	if receipt.Status != "succeeded" {
		t.Fatal(string(raw))
	}
	call.Args = connect.JSON(map[string]string{"text": "different"})
	if _, err = b.Call(ctx, phone.Principal(), call); connect.Code(err) != "operation_conflict" {
		t.Fatal(err)
	}
	call.OperationID = protocol.NewID()
	if _, err = b.Call(ctx, phone.Principal(), call); connect.Code(err) != "stale_target" {
		t.Fatal(err)
	}
	_ = b.Close()
	if _, err = host.NewSession(ctx, nil); err != nil {
		t.Fatal("bridge owned runtime", err)
	}
}

type testBridgeStore struct{ data []byte }

func (s *testBridgeStore) Load() ([]byte, error) { return append([]byte(nil), s.data...), nil }
func (s *testBridgeStore) Save(b []byte) error   { s.data = append([]byte(nil), b...); return nil }

// This separate live fixture uses no model credentials and never touches the
// owner's profiles. Run the compiled test binary with an isolated bridge home.
func TestBridgeLiveAttach(t *testing.T) {
	if os.Getenv("ORB_BRIDGE_LIVE_ATTACH") != "1" {
		t.Skip("native live fixture")
	}
	root := os.Getenv("ORB_BRIDGE_HOME")
	if !filepath.IsAbs(root) {
		t.Fatal("isolated ORB_BRIDGE_HOME required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	for n := 0; n < 20; n++ {
		cwd := filepath.Join(root, "runtime", fmt.Sprint(n))
		if err := os.MkdirAll(cwd, 0700); err != nil {
			t.Fatal(err)
		}
		provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
		provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("live bridge answer")})
		host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{CWD: cwd, AgentDir: cwd, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
		if err != nil {
			t.Fatal(err)
		}
		defer host.Dispose(context.Background())
		detach, err := attachEnabledBridge(ctx, host, CLIArgs{BridgeProfile: "personal", InstanceAlias: fmt.Sprintf("live-%02d", n), bridgeLink: &cliBridgeLink{}}, nil, os.Stderr)
		if err != nil {
			t.Fatal(err)
		}
		defer detach()
	}
	fmt.Println("LIVE_ATTACH_READY")
	<-ctx.Done()
}
