package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/platforms/native"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

type countingMemoryClass struct {
	inner        memory.Shared
	mergeCalls   atomic.Int64
	changedTotal atomic.Int64
}

func (c *countingMemoryClass) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	return c.inner.Shareable(ctx)
}

func (c *countingMemoryClass) Removed(ctx context.Context) (map[string]string, error) {
	return c.inner.Removed(ctx)
}

func (c *countingMemoryClass) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	c.mergeCalls.Add(1)
	changed, err := c.inner.MergeShared(ctx, incoming, keepOwn, removed)
	c.changedTotal.Add(int64(len(changed)))
	return changed, err
}

// TestMemoryE2EMultiPeerMitosisSyncTorture builds a 4-peer Mitosis sync group
// (2 owners + 2 members) backed by filestore.FileStore + memory.Shared, performs
// concurrent colliding writes, profile additions, a non-owner member profile deletion,
// and an offline-then-rejoin peer, then asserts full convergence and 10 idle rounds
// with ZERO changed keys and ZERO version bumps.
func TestMemoryE2EMultiPeerMitosisSyncTorture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type peerNode struct {
		name  string
		m     *mitosis.Mitosis
		store *filestore.FileStore
		cls   *countingMemoryClass
	}

	newPeer := func(name string) *peerNode {
		m, err := mitosis.Open(&document.Memory{}, true)
		if err != nil {
			t.Fatalf("mitosis.Open(%s): %v", name, err)
		}
		t.Cleanup(func() { _ = m.Close() })
		st, err := filestore.NewFileStore(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatalf("NewFileStore(%s): %v", name, err)
		}
		cls := &countingMemoryClass{inner: memory.Shared{Store: st}}
		m.SetClass("memory", cls)
		return &peerNode{name: name, m: m, store: st, cls: cls}
	}

	owner1 := newPeer("owner1")
	owner2 := newPeer("owner2")
	member1 := newPeer("member1")
	member2 := newPeer("member2")
	allPeers := []*peerNode{owner1, owner2, member1, member2}

	connect := func(a, b *peerNode) {
		_ = a.m.SavePeer(b.m.PeerID(), "tc1")
		_ = b.m.SavePeer(a.m.PeerID(), "tc1")
		near, far := net.Pipe()
		served := make(chan error, 1)
		go func() {
			_, _, err := b.m.Connect(ctx, far, "", true)
			served <- err
		}()
		if _, _, err := a.m.Connect(ctx, near, b.m.PeerID(), false); err != nil {
			t.Fatalf("connect %s->%s: %v", a.name, b.name, err)
		}
		if err := <-served; err != nil {
			t.Fatalf("accept %s<-%s: %v", b.name, a.name, err)
		}
	}

	// Connect owner1, owner2, member1 initially; leave member2 offline during initial edits.
	connect(owner1, owner2)
	connect(owner1, member1)
	connect(owner2, member1)

	// Form group: owner1 & owner2 are owners; member1 is online member (member2 joins after offline edits).
	for _, id := range []string{owner2.m.PeerID(), member1.m.PeerID()} {
		if _, err := owner1.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"add": id})); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if _, err := owner1.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"promote": owner2.m.PeerID()})); err != nil {
		t.Fatalf("promote owner2: %v", err)
	}
	if _, err := owner2.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"join": owner1.m.PeerID()})); err != nil {
		t.Fatalf("owner2 join: %v", err)
	}
	if _, err := member1.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"join": owner1.m.PeerID()})); err != nil {
		t.Fatalf("member1 join: %v", err)
	}

	// Concurrent local writes on ALL 4 peers (colliding local entry IDs 0, 1, 2 + profile additions).
	baseTS := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for idx, p := range allPeers {
		wg.Add(1)
		go func(i int, p *peerNode) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				_, err := p.store.AppendEntry(ctx, memory.Entry{
					Kind:    memory.KindUser,
					Text:    fmt.Sprintf("peer=%s local_seq=%d", p.name, j),
					Peer:    p.name,
					Session: fmt.Sprintf("sess-%s", p.name),
					TS:      baseTS.Add(time.Duration(i*10+j) * time.Second),
				})
				if err != nil {
					t.Errorf("%s AppendEntry %d: %v", p.name, j, err)
				}
			}
			if err := p.store.SaveProfile(ctx, []memory.ProfileEntry{
				{ID: "prof-" + p.name, Content: "fact from " + p.name, TS: baseTS.Add(time.Duration(i) * time.Minute)},
				{ID: "prof-shared-to-delete", Content: "temporary fact to be deleted by member1", TS: baseTS},
			}); err != nil {
				t.Errorf("%s SaveProfile: %v", p.name, err)
			}
		}(idx, p)
	}
	wg.Wait()

	// Helper to run N sync rounds across a set of connected peers, exchanging
	// Mitosis contact records between peers (mirroring daemon peer discovery).
	syncGroup := func(peers []*peerNode, rounds int) {
		var st struct {
			Group string `json:"group_id"`
		}
		rawSt, err := owner1.m.Admin(ctx, "sync", mitosis.JSON(map[string]any{}))
		if err != nil {
			t.Fatalf("owner1 status: %v", err)
		}
		_ = json.Unmarshal(rawSt, &st)

		for r := 0; r < rounds; r++ {
			for _, p := range peers {
				if _, err := p.m.Sync(ctx, nil); err != nil {
					t.Fatalf("Sync(%s) round %d: %v", p.name, r, err)
				}
				for _, other := range peers {
					if other == p {
						continue
					}
					recs, err := p.m.Contacts(other.m.PeerID(), st.Group)
					if err != nil {
						continue
					}
					for _, rec := range recs {
						_, _ = other.m.MergeContact(p.m.PeerID(), rec)
					}
				}
			}
		}
	}

	// Converge the 3 online peers first.
	syncGroup([]*peerNode{owner1, owner2, member1}, 4)

	// Non-owner member1 deletes "prof-shared-to-delete".
	profM1, err := member1.store.GetProfile(ctx)
	if err != nil {
		t.Fatalf("member1 GetProfile: %v", err)
	}
	var kept []memory.ProfileEntry
	for _, pe := range profM1 {
		if pe.ID != "prof-shared-to-delete" {
			kept = append(kept, pe)
		}
	}
	if err := member1.store.SaveProfile(ctx, kept); err != nil {
		t.Fatalf("member1 SaveProfile delete: %v", err)
	}

	// Sync online peers so deletion propagates.
	syncGroup([]*peerNode{owner1, owner2, member1}, 4)

	// Now member2 comes online and joins!
	connect(owner1, member2)
	connect(owner2, member2)
	connect(member1, member2)
	if _, err := owner1.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"add": member2.m.PeerID()})); err != nil {
		t.Fatalf("add member2: %v", err)
	}
	if _, err := member2.m.Admin(ctx, "sync", mitosis.JSON(map[string]string{"join": owner1.m.PeerID()})); err != nil {
		t.Fatalf("member2 join: %v", err)
	}

	// Run convergence rounds across all 4 peers.
	syncGroup(allPeers, 6)

	// Verify ALL 4 peers converged to the exact same 12 entries (4 peers * 3 colliding local IDs)
	// and exact same 4 profile entries (with prof-shared-to-delete deleted on ALL 4 peers).
	type snapshot struct {
		entryTexts []string
		profileIDs []string
	}
	getSnap := func(p *peerNode) snapshot {
		entries, err := p.store.ListEntries(ctx, 0, -1)
		if err != nil {
			t.Fatalf("%s ListEntries: %v", p.name, err)
		}
		var texts []string
		for _, e := range entries {
			texts = append(texts, e.Text)
		}
		sort.Strings(texts)

		prof, err := p.store.GetProfile(ctx)
		if err != nil {
			t.Fatalf("%s GetProfile: %v", p.name, err)
		}
		var pids []string
		for _, pe := range prof {
			pids = append(pids, pe.ID)
		}
		sort.Strings(pids)
		return snapshot{entryTexts: texts, profileIDs: pids}
	}

	for _, p := range allPeers {
		s := getSnap(p)
		t.Logf("peer=%s mergeCalls=%d changed=%d entries=%d profile=%v", p.name, p.cls.mergeCalls.Load(), p.cls.changedTotal.Load(), len(s.entryTexts), s.profileIDs)
	}
	ref := getSnap(owner1)
	if len(ref.entryTexts) != 12 {
		t.Fatalf("expected 12 converged entries (zero lost from colliding local IDs), got %d: %v", len(ref.entryTexts), ref.entryTexts)
	}
	wantProfs := []string{"prof-member1", "prof-member2", "prof-owner1", "prof-owner2"}
	if strings.Join(ref.profileIDs, ",") != strings.Join(wantProfs, ",") {
		t.Fatalf("expected profile IDs %v (member1 deletion respected), got %v", wantProfs, ref.profileIDs)
	}

	for _, p := range allPeers[1:] {
		got := getSnap(p)
		if strings.Join(got.entryTexts, "|") != strings.Join(ref.entryTexts, "|") {
			t.Fatalf("peer %s entries diverged from owner1:\ngot:  %v\nwant: %v", p.name, got.entryTexts, ref.entryTexts)
		}
		if strings.Join(got.profileIDs, ",") != strings.Join(ref.profileIDs, ",") {
			t.Fatalf("peer %s profile diverged from owner1:\ngot:  %v\nwant: %v", p.name, got.profileIDs, ref.profileIDs)
		}
	}

	// Zero-spam assertion: run 10 consecutive sync rounds across all 4 peers and assert
	// ZERO changed keys and ZERO version bumps!
	changedBefore := make([]int64, len(allPeers))
	statusBefore := make([]string, len(allPeers))
	for i, p := range allPeers {
		changedBefore[i] = p.cls.changedTotal.Load()
		st, err := p.m.Admin(ctx, "sync", mitosis.JSON(map[string]any{}))
		if err != nil {
			t.Fatalf("status before(%s): %v", p.name, err)
		}
		statusBefore[i] = string(st)
	}

	syncGroup(allPeers, 10)

	for i, p := range allPeers {
		afterChanged := p.cls.changedTotal.Load()
		if afterChanged != changedBefore[i] {
			t.Fatalf("sync spam on %s: changedTotal grew %d -> %d during 10 idle rounds", p.name, changedBefore[i], afterChanged)
		}
		st, err := p.m.Admin(ctx, "sync", mitosis.JSON(map[string]any{}))
		if err != nil {
			t.Fatalf("status after(%s): %v", p.name, err)
		}
		if string(st) != statusBefore[i] {
			t.Fatalf("sync spam on %s: status/versions changed during 10 idle rounds:\nbefore: %s\nafter:  %s", p.name, statusBefore[i], string(st))
		}
	}
	t.Logf("4-peer Mitosis sync torture passed: 12/12 entries converged, member deletion respected, 10 idle rounds = 0 changed keys / 0 version bumps")
}

// TestMemoryE2ERealOrbBinary builds a real `orb` binary in /tmp/orb-mem-stress-2-*
// and tests:
//   - orb memory --help / status --json / view / compact --json / search --json / zoom --json
//     on BOTH native SQLite and --pi-files backends
//   - orb --mode rpc with plugins.memory: true
//   - orb session archive does NOT delete or break orb memory entries/zoom
func TestMemoryE2ERealOrbBinary(t *testing.T) {
	tmpRoot, err := os.MkdirTemp("/tmp", "orb-mem-stress-2-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpRoot) }()

	binPath := filepath.Join(tmpRoot, "orb")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build orb: %v\n%s", err, string(out))
	}

	runOrb := func(agentDir, stateHome string, args ...string) (string, string, error) {
		cmd := exec.Command(binPath, args...)
		cmd.Env = append(os.Environ(),
			config.EnvAgentDir+"="+agentDir,
			"HOME="+filepath.Join(tmpRoot, "fakehome"),
			"ORB_STATE_HOME="+stateHome,
			"ORB_MITOSIS_HOME="+filepath.Join(tmpRoot, "mitosis"),
			"ORB_OFFLINE=1",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	// Helper to verify all memory CLI subcommands on a given backend.
	verifyMemoryCLI := func(t *testing.T, label, agentDir, stateHome string, prefixArgs ...string) {
		t.Helper()
		run := func(sub ...string) (string, string, error) {
			full := append([]string{}, prefixArgs...)
			full = append(full, sub...)
			return runOrb(agentDir, stateHome, full...)
		}

		// 1. --help
		out, errOut, err := run("memory", "--help")
		if err != nil || !strings.Contains(out, "Usage: orb memory") {
			t.Fatalf("[%s] memory --help failed: err=%v out=%q errOut=%q", label, err, out, errOut)
		}

		// 2. status --json
		out, errOut, err = run("memory", "status", "--json")
		if err != nil {
			t.Fatalf("[%s] memory status --json failed: err=%v errOut=%q", label, err, errOut)
		}
		var st memoryStatusReport
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("[%s] unmarshal status: %v (%s)", label, err, out)
		}
		if st.Entries != 4 || st.ProfileEntries != 1 {
			t.Fatalf("[%s] unexpected status: %+v", label, st)
		}

		// 3. view
		out, errOut, err = run("memory", "view")
		if err != nil || !strings.Contains(out, "Prefers concise Go code.") {
			t.Fatalf("[%s] memory view failed: err=%v out=%q errOut=%q", label, err, out, errOut)
		}

		// 4. compact --json
		out, errOut, err = run("memory", "compact", "--json")
		if err != nil {
			t.Fatalf("[%s] memory compact --json failed: err=%v errOut=%q", label, err, errOut)
		}
		var comp memoryCompactReport
		if err := json.Unmarshal([]byte(out), &comp); err != nil {
			t.Fatalf("[%s] unmarshal compact: %v (%s)", label, err, out)
		}
		if comp.BuiltNodes < 4 {
			t.Fatalf("[%s] expected >= 4 built nodes, got %d", label, comp.BuiltNodes)
		}

		// 5. search <q> --json
		out, errOut, err = run("memory", "search", "OptChat", "--json")
		if err != nil || !strings.Contains(out, "OptChat") {
			t.Fatalf("[%s] memory search --json failed: err=%v out=%q errOut=%q", label, err, out, errOut)
		}

		// 6. zoom <id> [n] --json
		out, errOut, err = run("memory", "zoom", "0", "2", "--json")
		if err != nil || !strings.Contains(out, "Tuesday") {
			t.Fatalf("[%s] memory zoom --json failed: err=%v out=%q errOut=%q", label, err, out, errOut)
		}
	}

	ctx := context.Background()

	// --- A. Native SQLite backend + session archive isolation ---
	sqliteAgentDir := filepath.Join(tmpRoot, "sqlite-agent")
	sqliteStateHome := filepath.Join(tmpRoot, "sqlite-state")
	if err := os.MkdirAll(sqliteAgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sqliteStateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, sqliteAgentDir)
	t.Setenv("ORB_STATE_HOME", sqliteStateHome)
	t.Setenv("ORB_MITOSIS_HOME", filepath.Join(tmpRoot, "mitosis"))
	t.Setenv("ORB_OFFLINE", "1")
	state, err := native.Open(ctx, sqliteAgentDir, true, nil)
	if err != nil {
		t.Fatalf("native.Open: %v", err)
	}
	seedMemoryStore(t, ctx, state.Memory())
	// Also create a real session in SQLite so we can archive it and prove memory is untouched.
	ts := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	cwdJSON, _ := json.Marshal(tmpRoot)
	sessPath := filepath.Join(tmpRoot, "11111111-1111-4111-8111-111111111111.jsonl")
	sessData := fmt.Sprintf(
		"{\"type\":\"session\",\"version\":3,\"id\":\"11111111-1111-4111-8111-111111111111\",\"timestamp\":%q,\"cwd\":%s}\n"+
			"{\"type\":\"message\",\"id\":\"m1\",\"parentId\":null,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"Archive target session\",\"timestamp\":1790503201000}}\n",
		ts, string(cwdJSON), ts,
	)
	if err := os.WriteFile(sessPath, []byte(sessData), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Sessions().OpenPath(ctx, sessPath); err != nil {
		t.Fatalf("Sessions().OpenPath: %v", err)
	}
	_ = state.Close()

	verifyMemoryCLI(t, "native-sqlite", sqliteAgentDir, sqliteStateHome,
		"--agent-dir", sqliteAgentDir, "--state-home", sqliteStateHome)

	// Archive the session via real `orb session archive` and verify memory entries/zoom are intact!
	out, errOut, err := runOrb(sqliteAgentDir, sqliteStateHome,
		"--agent-dir", sqliteAgentDir, "--state-home", sqliteStateHome,
		"session", "archive", "11111111")
	if err != nil {
		t.Fatalf("orb session archive failed: err=%v out=%q errOut=%q", err, out, errOut)
	}
	out, errOut, err = runOrb(sqliteAgentDir, sqliteStateHome,
		"--agent-dir", sqliteAgentDir, "--state-home", sqliteStateHome,
		"memory", "zoom", "0", "2", "--json")
	if err != nil || !strings.Contains(out, "Tuesday") {
		t.Fatalf("memory zoom broken after session archive: err=%v out=%q errOut=%q", err, out, errOut)
	}

	// --- B. --pi-files backend ---
	piAgentDir := filepath.Join(tmpRoot, "pi-agent")
	piStateHome := filepath.Join(tmpRoot, "pi-state")
	fstore, err := filestore.NewFileStore(filepath.Join(piAgentDir, "memory"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	seedMemoryStore(t, ctx, fstore)

	verifyMemoryCLI(t, "pi-files", piAgentDir, piStateHome,
		"--pi-files", "--agent-dir", piAgentDir, "--state-home", piStateHome)

	// --- C. orb --mode rpc with plugins.memory: true ---
	rpcAgentDir := filepath.Join(tmpRoot, "rpc-agent")
	rpcStateHome := filepath.Join(tmpRoot, "rpc-state")
	if err := os.MkdirAll(rpcAgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rpcStateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rpcAgentDir, "settings.json"), []byte(`{"plugins":{"memory":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rpcCancel()
	rpcCmd := exec.CommandContext(rpcCtx, binPath,
		"--agent-dir", rpcAgentDir,
		"--state-home", rpcStateHome,
		"--mode", "rpc",
	)
	rpcCmd.Env = append(os.Environ(),
		config.EnvAgentDir+"="+rpcAgentDir,
		"HOME="+filepath.Join(tmpRoot, "fakehome"),
		"ORB_STATE_HOME="+rpcStateHome,
		"ORB_MITOSIS_HOME="+filepath.Join(tmpRoot, "mitosis"),
		"ORB_OFFLINE=1",
	)
	stdin, err := rpcCmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := rpcCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var rpcStderr bytes.Buffer
	rpcCmd.Stderr = &rpcStderr

	if err := rpcCmd.Start(); err != nil {
		t.Fatalf("start orb --mode rpc: %v", err)
	}

	// Send get_state RPC request and close stdin so orb exits cleanly.
	if _, err := io.WriteString(stdin, `{"id":"req-1","type":"get_state"}`+"\n"); err != nil {
		t.Fatalf("write rpc stdin: %v", err)
	}
	_ = stdin.Close()

	var lines []string
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := rpcCmd.Wait(); err != nil {
		t.Fatalf("orb --mode rpc exited with error: %v\nstderr: %s\nstdout: %s", err, rpcStderr.String(), strings.Join(lines, "\n"))
	}

	foundState := false
	for _, line := range lines {
		var resp map[string]any
		if json.Unmarshal([]byte(line), &resp) == nil {
			if resp["type"] == "response" && resp["id"] == "req-1" && resp["success"] == true {
				foundState = true
				break
			}
		}
	}
	if !foundState {
		t.Fatalf("orb --mode rpc did not return valid get_state response:\nstdout: %s\nstderr: %s", strings.Join(lines, "\n"), rpcStderr.String())
	}
	t.Logf("real orb binary E2E passed: native-sqlite, pi-files, session archive isolation, and --mode rpc with plugins.memory=true")
}
