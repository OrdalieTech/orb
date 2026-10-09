package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/platforms/native"
)

func TestHostListsThisMachinesThreadsAndLaunchesOnlyIntoFolders(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("ORB_OFFLINE", "1")
	jsonl := filepath.Join(root, "s.jsonl")
	cwd, _ := json.Marshal(root) // a Windows path has backslashes to escape
	if err := os.WriteFile(jsonl, []byte(`{"type":"session","version":3,"id":"11111111-2222-4333-8444-555555555555","timestamp":"2026-09-27T10:00:00.000Z","cwd":`+string(cwd)+`}
{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-09-27T10:00:01.000Z","message":{"role":"user","content":"`+strings.Repeat("long ", 60)+`","timestamp":1790503201000}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := native.Open(t.Context(), agentDir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }() // Windows cannot remove the temp dir around an open database
	b, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := state.Sessions().OpenPath(t.Context(), jsonl); err != nil {
		t.Fatal("import:", err)
	}
	s := New(t.Context(), state, "personal", "dev", b, nil, "")
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
	// A thread open in another Orb here (a terminal one) is busy, not an Orb that dies unseen.
	lock, err := state.OwnerLock("11111111-2222-4333-8444-555555555555")
	if err != nil || lock.Lock() != nil {
		t.Fatal("lock:", err)
	}
	if _, err := s.host(t.Context(), p, "host.launch", bridge.JSON(map[string]string{"session_id": "11111111-2222-4333-8444-555555555555"})); bridge.Code(err) != "busy" {
		t.Fatalf("launched a thread open elsewhere: %v", err)
	}
	_ = lock.Close()
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
	go idle(events, &launched{input: input}, 200*time.Millisecond)
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

func TestHostLoginRelaysSignInToThePeer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	script := filepath.Join(t.TempDir(), "orb")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+
		"echo '{\"type\":\"auth_url\",\"url\":\"https://claude.ai/oauth/authorize\"}'\n"+
		"echo '{\"type\":\"prompt\",\"kind\":\"manual_code\",\"message\":\"Paste the code\"}'\n"+
		"read code\necho \"{\\\"type\\\":\\\"done\\\",\\\"code\\\":\\\"$code\\\"}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A sign-in ends the launched Orbs between turns, so they reopen with the new credential.
	quiet, working := &closeFlag{}, &closeFlag{}
	busy := &launched{input: working}
	busy.turn.Store(true)
	s := &Service{loginExecutable: func() (string, error) { return script, nil }, logins: map[string]*hostLogin{}, launched: map[string]*launched{"quiet": {input: quiet}, "busy": busy}, ctx: t.Context()}
	call := func(method string, params any) map[string]json.RawMessage {
		t.Helper()
		raw, err := s.login(t.Context(), method, bridge.JSON(params))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var result map[string]json.RawMessage
		_ = json.Unmarshal(raw, &result)
		return result
	}
	var id string
	_ = json.Unmarshal(call("host.login.start", map[string]string{"provider": "anthropic", "method": "oauth"})["login_id"], &id)
	var events []json.RawMessage
	cursor := 0
	for len(events) < 2 {
		page := call("host.login.poll", map[string]any{"login_id": id, "cursor": cursor})
		var batch []json.RawMessage
		_ = json.Unmarshal(page["events"], &batch)
		_ = json.Unmarshal(page["cursor"], &cursor)
		events = append(events, batch...)
	}
	call("host.login.answer", map[string]string{"login_id": id, "value": "the-code"})
	for {
		page := call("host.login.poll", map[string]any{"login_id": id, "cursor": cursor})
		var batch []json.RawMessage
		_ = json.Unmarshal(page["events"], &batch)
		_ = json.Unmarshal(page["cursor"], &cursor)
		events = append(events, batch...)
		if string(page["done"]) == "true" {
			break
		}
	}
	if len(events) != 3 || !strings.Contains(string(events[2]), `"code":"the-code"`) {
		t.Fatalf("events = %s", events)
	}
	if !quiet.closed.Load() || working.closed.Load() {
		t.Fatalf("after sign-in: quiet ended %v, working ended %v", quiet.closed.Load(), working.closed.Load())
	}
	if _, err := s.login(t.Context(), "host.login.poll", bridge.JSON(map[string]any{"login_id": id})); bridge.Code(err) != "not_found" {
		t.Fatalf("finished sign-in still polled: %v", err)
	}
	if _, err := s.login(t.Context(), "host.login.start", bridge.JSON(map[string]string{"provider": "anthropic", "method": "browser"})); bridge.Code(err) != "invalid_params" {
		t.Fatalf("unknown method: %v", err)
	}
}

// A peer allowed on the machine gets its shell in a folder: what it types runs there, the output
// comes back by offset until the shell ends, and a closed terminal is gone.
func TestHostTerminalRunsAShellInTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no pseudo-terminals on Windows")
	}
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	s := &Service{terminals: map[string]*hostTerminal{}, ctx: t.Context()}
	call := func(method string, params any, result any) {
		t.Helper()
		raw, err := s.terminal(t.Context(), method, bridge.JSON(params))
		if err != nil || result != nil && json.Unmarshal(raw, result) != nil {
			t.Fatalf("%s: %s %v", method, raw, err)
		}
	}
	var opened struct {
		ID string `json:"terminal_id"`
	}
	call("host.terminal.open", map[string]any{"cwd": dir, "cols": 80, "rows": 24}, &opened)
	call("host.terminal.write", map[string]any{"terminal_id": opened.ID, "data": []byte("pwd; exit\n")}, nil)
	var out []byte
	var r struct {
		Data   []byte `json:"data"`
		Offset int64  `json:"offset"`
		Done   bool   `json:"done"`
	}
	for deadline := time.Now().Add(10 * time.Second); !r.Done && time.Now().Before(deadline); {
		r.Data = nil
		call("host.terminal.read", map[string]any{"terminal_id": opened.ID, "offset": r.Offset}, &r)
		out = append(out, r.Data...)
	}
	if !r.Done || !bytes.Contains(out, []byte(filepath.Base(dir))) {
		t.Fatalf("shell output %q, done %v", out, r.Done)
	}
	call("host.terminal.close", map[string]any{"terminal_id": opened.ID}, nil)
	if _, err := s.terminal(t.Context(), "host.terminal.read", bridge.JSON(map[string]any{"terminal_id": opened.ID})); bridge.Code(err) != "not_found" {
		t.Fatalf("a closed terminal still answers: %v", err)
	}
}

// A Bridge whose binary is replaced restarts into the new one, once it runs: a
// replacement that fails is not one to restart into.
func TestBridgeFollowsAReplacedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Bridge cannot exec on Windows")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "orb")
	install := func(name, script string) {
		staged := filepath.Join(dir, name)
		if err := os.WriteFile(staged, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(staged, exe); err != nil {
			t.Fatal(err)
		}
	}
	install("v1", "echo 1")
	restarted := make(chan string, 1)
	go followBinary(t.Context(), exe, 10*time.Millisecond, func(path string) { restarted <- path })
	time.Sleep(50 * time.Millisecond)
	install("broken", "exit 1")
	select {
	case <-restarted:
		t.Fatal("restarted into a binary that does not run")
	case <-time.After(200 * time.Millisecond):
	}
	install("v2", "echo 2")
	select {
	case path := <-restarted:
		if path != exe {
			t.Fatalf("restarted into %s", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Bridge did not restart into its new binary")
	}
}
