package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/document"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

func TestBrowserWasmLifecycle(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "orb.wasm")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("Wasm build: %v\n%s", err, output)
	}
	root, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	b, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	instance, token, err := b.Enroll("wasm-test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := bridge.NewLocal(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"wasm_bridge":true}`), nil
	})
	if _, err = b.Attach(instance.ID, token, protocol.NewID(), endpoint); err != nil {
		t.Fatal(err)
	}
	if err = b.AddGrant(bridge.Grant{Principal: bridge.Principal{PeerID: bridge.PeerID(pub), Subject: bridge.Subject{Kind: "controller"}}, GroupID: b.PersonalGroup(), IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect"}}); err != nil {
		t.Fatal(err)
	}
	handler, err := webtransport.Handler(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	fixture := bridge.JSON(map[string]string{"url": "ws" + strings.TrimPrefix(server.URL, "http"), "peer": b.PeerID(), "seed": base64.RawStdEncoding.EncodeToString(key.Seed()), "agentID": protocol.NewID(), "instance": instance.ID})
	run := exec.CommandContext(t.Context(), "node", "testdata/smoke.cjs", filepath.Join(strings.TrimSpace(string(root)), "lib/wasm/wasm_exec.js"), binary)
	run.Env = append(os.Environ(), "ORB_TEST_BRIDGE="+string(fixture))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("Wasm lifecycle: %v\n%s", err, output)
	}
}

func TestBrowserBridgeController(t *testing.T) {
	run := exec.CommandContext(t.Context(), "node", "testdata/bridge-ui.mjs")
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("browser controller: %v\n%s", err, output)
	}
}
