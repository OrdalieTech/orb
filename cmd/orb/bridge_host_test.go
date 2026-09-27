package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/bridge"
)

func TestHostListsThisMachinesThreadsAndLaunchesOnlyIntoFolders(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("PI_OFFLINE", "1")
	jsonl := filepath.Join(root, "s.jsonl")
	cwd, _ := json.Marshal(root) // a Windows path has backslashes to escape
	if err := os.WriteFile(jsonl, []byte(`{"type":"session","version":3,"id":"11111111-2222-4333-8444-555555555555","timestamp":"2026-09-27T10:00:00.000Z","cwd":`+string(cwd)+`}
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-09-27T10:00:01.000Z","message":{"role":"user","content":"`+strings.Repeat("long ", 60)+`","timestamp":1790503201000}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	if runNativeCLI(t.Context(), []string{"storage", "import", jsonl}, cliStreams{Stdout: io.Discard, Stderr: &errs}) != 0 {
		t.Fatal("import:", errs.String())
	}
	state, err := openNativeState(t.Context(), agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bridge.Open(&testBridgeStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	s := &bridgeService{b: b, launched: map[string]*launched{}, ctx: context.WithValue(t.Context(), nativeStateKey{}, state)}
	p := bridge.Principal{PeerID: "orb:ed25519:x", Subject: bridge.Subject{Kind: "controller"}}

	raw, err := s.host(t.Context(), p, "host.sessions", bridge.JSON(struct{}{}))
	var page struct {
		Items []struct {
			ID    string `json:"session_id"`
			CWD   string `json:"cwd"`
			First string `json:"first"`
		} `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Items) != 1 || page.Items[0].CWD != root || len([]rune(page.Items[0].First)) != 160 {
		t.Fatalf("sessions: %s %v", raw, err)
	}
	for _, bad := range []string{"relative/dir", filepath.Join(root, "missing"), jsonl} {
		if _, err := s.host(t.Context(), p, "host.launch", bridge.JSON(map[string]string{"cwd": bad})); err == nil {
			t.Fatalf("launched into %q", bad)
		}
	}
	if _, err := s.host(t.Context(), p, "host.launch", bridge.JSON(map[string]string{"session_id": "unknown"})); bridge.Code(err) != "not_found" {
		t.Fatalf("unknown thread: %v", err)
	}
	for i := range maxLaunched {
		s.launched[string(rune('a'+i))] = &launched{input: io.NopCloser(nil)}
	}
	if _, err := s.host(t.Context(), p, "host.launch", bridge.JSON(map[string]string{"cwd": root})); bridge.Code(err) != "resource_exhausted" {
		t.Fatalf("launch past the cap: %v", err)
	}
	raw, err = s.host(t.Context(), p, "host.update", bridge.JSON(struct{}{}))
	if err != nil || !strings.Contains(string(raw), "development build") {
		t.Fatalf("a development build updated itself: %s %v", raw, err)
	}
	if got, err := launchFolder("~"); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("~ = %q %v", got, err)
	}
}

type closeFlag struct{ closed atomic.Bool }

func (c *closeFlag) Close() error { c.closed.Store(true); return nil }

func TestALaunchedOrbEndsWhenQuietOutsideATurn(t *testing.T) {
	events, write := io.Pipe()
	input := &closeFlag{}
	go idle(events, input, 200*time.Millisecond)
	_, _ = write.Write([]byte(`{"type":"agent_start"}` + "\n"))
	time.Sleep(500 * time.Millisecond)
	if input.closed.Load() {
		t.Fatal("ended during a turn")
	}
	_, _ = write.Write([]byte(`{"type":"agent_end"}` + "\n"))
	for deadline := time.Now().Add(2 * time.Second); !input.closed.Load(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a quiet Orb was kept")
		}
	}
	_ = write.Close()
}
