package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/hosts/native"
	"github.com/OrdalieTech/orb/connect"
	attach "github.com/OrdalieTech/orb/connect/agent"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/storage/sqlite"
	"github.com/OrdalieTech/orb/tui"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	agentDir := os.Getenv(config.EnvAgentDir)
	if !filepath.IsAbs(agentDir) {
		t.Fatal("isolated agent directory required")
	}
	state, err := openNativeState(ctx, agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.close() }()
	ctx = context.WithValue(ctx, nativeStateKey{}, state)
	for n := 0; n < 20; n++ {
		cwd := filepath.Join(root, "runtime", fmt.Sprint(n))
		if err := os.MkdirAll(cwd, 0700); err != nil {
			t.Fatal(err)
		}
		provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
		provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("live bridge answer")})
		saved, err := state.sessions().List(ctx, harness.SessionListOptions{CWD: cwd})
		if err != nil {
			t.Fatal(err)
		}
		var stored *harness.Session
		if len(saved) > 0 {
			stored, err = state.sessions().Open(ctx, saved[0])
		} else {
			stored, err = state.sessions().Create(ctx, harness.SessionCreateOptions{CWD: cwd})
		}
		if err != nil {
			t.Fatal(err)
		}
		manager, err := session.FromHarnessStorage(stored.Storage(), session.WithHarnessRepo(state.sessions()))
		if err != nil {
			t.Fatal(err)
		}
		host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{SessionManager: manager, CWD: cwd, AgentDir: cwd, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
		if err != nil {
			t.Fatal(err)
		}
		defer host.Dispose(context.Background())
		detach, err := attachEnabledBridge(ctx, host, CLIArgs{native: state, BridgeProfile: "personal", InstanceAlias: fmt.Sprintf("live-%02d", n), bridgeLink: &cliBridgeLink{}}, nil, os.Stderr)
		if err != nil {
			t.Fatal(err)
		}
		defer detach()
	}
	fmt.Println("LIVE_ATTACH_READY")
	<-ctx.Done()
}

func TestBridgeSettingsNavigationAndLayout(t *testing.T) {
	for _, running := range []bool{false, true} {
		status := bridgeSettingsStatus{Peers: []string{"device-fingerprint"}, Pending: []bridge.Invitation{{Claimant: "pending-device", Status: "claimed", Expires: time.Now().Add(time.Minute).Unix()}}}
		rows := bridgeSettingsRows("", running, running, false, status, extensions.NewNoopUI().Theme())
		var foundStart, foundStop, foundDevices bool
		for _, row := range rows {
			foundStart = foundStart || row.Value == "Start"
			foundStop = foundStop || row.Value == "Stop"
			foundDevices = foundDevices || row.Value == "peer:device-fingerprint"
		}
		if foundStart == running || foundStop != running || !foundDevices {
			t.Fatalf("wrong actions for running=%v: %+v", running, rows)
		}
		panel := newBridgeSettingsPanel("personal", "", "", rows, extensions.NewNoopUI().Theme(), func() int { return 24 }, func(any) {})
		for _, width := range []int{24, 48, 80, 120} {
			lines := panel.Render(width)
			if len(lines) > 24 {
				t.Fatalf("screen too tall: %d", len(lines))
			}
			for _, line := range lines {
				if tui.VisibleWidth(line) > width {
					t.Fatalf("overflow at %d: %q", width, line)
				}
			}
		}
	}
	rows := bridgeSettingsRows("", true, true, false, bridgeSettingsStatus{Peers: []string{"device-fingerprint"}, Pending: []bridge.Invitation{{Claimant: "pending", Status: "claimed", Expires: time.Now().Add(time.Minute).Unix()}}}, extensions.NewNoopUI().Theme())
	var paired, pending bool
	for _, row := range rows {
		paired = paired || row.Value == "peer:device-fingerprint"
		pending = pending || row.Value == "Approve pairing"
	}
	if !paired || !pending {
		t.Fatal("paired devices or pending approvals missing")
	}
	selected := ""
	panel := newBridgeSettingsPanel("personal", "", "", bridgeSettingsRows("", false, false, false, bridgeSettingsStatus{}, extensions.NewNoopUI().Theme()), extensions.NewNoopUI().Theme(), func() int { return 24 }, func(v any) { selected, _ = v.(string) })
	panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	if selected != "Start" {
		t.Fatalf("first action = %q", selected)
	}
	panel.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if selected != "" {
		t.Fatal("Escape did not close page")
	}
}

func TestBridgeSettingsRespectProjectOverrides(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".pi", "settings.json"), []byte(`{"plugins":{"bridge":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := setBridgeSetting(settings, "bridge", true); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("project override silently ignored: %v", err)
	}
	if settings.GetPlugins()["bridge"] {
		t.Fatal("project override was bypassed")
	}
	if err := setBridgeSetting(settings, "bridge-agent-calls", true); err != nil {
		t.Fatal(err)
	}
	if !settings.GetPlugins()["bridge-agent-calls"] {
		t.Fatal("agent setting was not saved")
	}
}

type bridgeScriptUI struct {
	extensions.NoopUI
	t       *testing.T
	actions []string
	screens []string
	status  []string
	observe func(*bridgeSettingsPanel)
}

func (*bridgeScriptUI) Width() int  { return 80 }
func (*bridgeScriptUI) Height() int { return 24 }
func (*bridgeScriptUI) Invalidate() {}
func (ui *bridgeScriptUI) SetStatus(_ string, value *string) {
	if value != nil {
		ui.status = append(ui.status, *value)
	}
}
func (ui *bridgeScriptUI) Custom(_ context.Context, factory extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	ui.t.Helper()
	if len(ui.actions) == 0 {
		ui.t.Fatal("unexpected Bridge screen")
	}
	action := ui.actions[0]
	ui.actions = ui.actions[1:]
	var result any
	component, err := factory(ui, ui.Theme(), nil, func(value any) { result = value })
	if err != nil {
		return nil, false, err
	}
	panel := component.(*bridgeSettingsPanel)
	defer panel.Dispose()
	if ui.observe != nil {
		ui.observe(panel)
	}
	ui.screens = append(ui.screens, strings.Join(panel.Render(80), "\n"))
	if action == "back" || action == "close" {
		panel.HandleInput(tui.KeyEvent{Raw: "\x1b"})
		return result, true, nil
	}
	for i := 0; i < 30; i++ {
		panel.mu.Lock()
		panel.list.ListSelectRow(i)
		selected := panel.list.SelectedValue()
		panel.mu.Unlock()
		if selected == action {
			panel.HandleInput(tui.KeyEvent{Raw: "\r"})
			return result, true, nil
		}
	}
	ui.t.Fatalf("action %q missing from screen:\n%s", action, ui.screens[len(ui.screens)-1])
	return nil, false, nil
}

func TestBridgeManagementDoesNotStartServiceWhenOpened(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORB_BRIDGE_HOME", root)
	settings, err := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("bridge", bridgeExtension(CLIArgs{}, settings)); err != nil {
		t.Fatal(err)
	}
	ui := &bridgeScriptUI{t: t, actions: []string{"close"}}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: ui, ErrorHandler: func(err extensions.ExtensionError) { t.Error(err) }})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	if !runner.ExecuteCommand(t.Context(), "bridge", "") {
		t.Fatal("Bridge command unavailable")
	}
	if _, err := os.Stat(filepath.Join(root, "personal")); !os.IsNotExist(err) {
		t.Fatalf("opening Bridge created profile state: %v", err)
	}
	if !strings.Contains(ui.screens[0], "Enable Bridge") {
		t.Fatal(ui.screens[0])
	}
}

func TestBridgeManagementNavigatesAndStopsNativeService(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "orb-bridge-ui-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	t.Setenv("ORB_BRIDGE_HOME", root)
	dir := filepath.Join(root, "personal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token := "local-owner-token"
	if err := os.WriteFile(filepath.Join(dir, "admin.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := bridge.Open(&testBridgeStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	service, stop := context.WithCancel(t.Context())
	defer stop()
	closeServer, err := native.Listen(service, filepath.Join(dir, "admin.sock"), b, token, func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "stop" {
			time.AfterFunc(10*time.Millisecond, stop)
			return connect.JSON(struct{}{}), nil
		}
		return b.Admin(ctx, method, params)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	settings, err := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	var configured []bool
	link := &cliBridgeLink{configure: func(enabled bool) error { configured = append(configured, enabled); return nil }}
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("bridge", bridgeExtension(CLIArgs{bridgeLink: link}, settings)); err != nil {
		t.Fatal(err)
	}
	ui := &bridgeScriptUI{t: t, actions: []string{"Start", "page:add", "back", "page:advanced", "agent-calls", "back", "Stop", "close"}}
	observed := false
	ui.observe = func(panel *bridgeSettingsPanel) {
		if observed {
			return
		}
		observed = true
		if err := b.SavePeer(b.PeerID(), "test-locator"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(strings.Join(panel.Render(80), "\n"), bridgeDeviceLabel(b.PeerID())) {
			if time.Now().After(deadline) {
				t.Fatal("new device did not appear while the home screen stayed open")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	reloads := 0
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: ui, CommandActions: &extensions.CommandActions{Reload: func(context.Context) error { reloads++; return nil }}, ErrorHandler: func(err extensions.ExtensionError) { t.Error(err) }})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner.ExecuteCommand(ctx, "bridge", "")
	if len(configured) != 2 || !configured[0] || configured[1] || settings.GetPlugins()["bridge"] || !settings.GetPlugins()["bridge-agent-calls"] || reloads != 1 {
		t.Fatalf("configured=%v settings=%v reloads=%d", configured, settings.GetPlugins(), reloads)
	}
	if !strings.Contains(ui.screens[len(ui.screens)-1], "Enable Bridge") {
		t.Fatal("screen did not reflect stopped service")
	}
	if len(ui.status) != 0 {
		t.Fatalf("healthy Bridge wrote a footer label: %v", ui.status)
	}
}

func TestBridgePanelCompletionCanRestoreFocus(t *testing.T) {
	var panel *bridgeSettingsPanel
	completed := make(chan struct{})
	panel = newBridgeSettingsPanel("personal", "", "", bridgeSettingsRows("", false, false, false, bridgeSettingsStatus{}, extensions.NewNoopUI().Theme()), extensions.NewNoopUI().Theme(), func() int { return 24 }, func(any) { panel.SetFocused(false); close(completed) })
	go panel.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("closing Bridge deadlocked while restoring focus")
	}
}

func TestBridgeConnectActionsAreAvailableBeforeActivation(t *testing.T) {
	for _, running := range []bool{false, true} {
		rows := bridgeSettingsRows("add", running, running, false, bridgeSettingsStatus{}, extensions.NewNoopUI().Theme())
		for _, action := range []string{"Invite device", "Join device", "SSH"} {
			found := false
			for _, row := range rows {
				if row.Value == action {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s missing with service running=%v", action, running)
			}
		}
	}
}

func TestBridgeInvitationCodeRoundTrip(t *testing.T) {
	b, err := bridge.Open(&testBridgeStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	inv, err := b.Invite([]bridge.Grant{{GroupID: b.PersonalGroup(), Permissions: []string{"instance.inspect"}}})
	if err != nil {
		t.Fatal(err)
	}
	inv.Locator = "private-locator"
	for _, text := range []string{bridgeInvitationCode(inv), string(connect.JSON(inv))} {
		got, err := parseBridgeInvitation(" " + text + "\n")
		if err != nil || got.ID != inv.ID || got.Token != inv.Token || got.Locator != inv.Locator {
			t.Fatalf("round trip: %v", err)
		}
	}
	for _, input := range []string{"", "orb-bridge:v1:bad!", "{}", strings.Repeat("x", protocol.MaxFrame+1)} {
		if _, err := parseBridgeInvitation(input); err == nil {
			t.Fatal("invalid invitation accepted")
		}
	}
	inv.Expires = time.Now().Add(-time.Second).Unix()
	if _, err := parseBridgeInvitation(bridgeInvitationCode(inv)); err == nil {
		t.Fatal("expired invitation accepted")
	}
}

func TestBridgeSSHArgumentsKeepHostVerificationAndQuoteRemoteCommand(t *testing.T) {
	for _, target := range []string{"", "-oProxyCommand=bad", "host;touch /tmp/bad", "user@host command", "user@$(bad)"} {
		if _, err := bridgeSSHCommand(target, "orb", "personal"); err == nil {
			t.Fatalf("unsafe SSH target accepted: %q", target)
		}
	}
	args, err := bridgeSSHCommand("user@server", "/opt/Orb's tools/orb", "personal", "pair", "approve", "invitation", "peer")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-oStrictHostKeyChecking=yes", "-oBatchMode=yes", "-oClearAllForwardings=yes"} {
		if !strings.Contains(joined, want) {
			t.Fatal("missing SSH safeguard", want)
		}
	}
	// Let a real shell decode the remote quoting without executing a remote command.
	command := strings.TrimPrefix(args[len(args)-1], "exec ")
	output, err := exec.Command("sh", "-c", "set -- "+command+`; printf '%s\n' "$@"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "/opt/Orb's tools/orb\nbridge\n--profile\npersonal\npair\napprove\ninvitation\npeer\n"
	if string(output) != want {
		t.Fatalf("remote arguments changed: %q", output)
	}
}

func TestBridgeDeviceRowsDistinguishPairingAndConnection(t *testing.T) {
	peers := []string{"orb:ed25519:aaaaaaaaaa", "orb:ed25519:bbbbbbbbbb", "orb:ed25519:cccccccccc"}
	rows := bridgeSettingsRows("", true, true, false, bridgeSettingsStatus{Peers: peers, PeerStates: map[string]string{peers[0]: "connected", peers[1]: "blocked"}}, extensions.NewNoopUI().Theme())
	states := map[string]string{peers[0]: "Connected", peers[1]: "Blocked", peers[2]: "Offline"}
	for _, row := range rows {
		if peer, ok := strings.CutPrefix(row.Value, "peer:"); ok {
			if row.Cells[1] != states[peer] {
				t.Fatalf("wrong peer state: %+v", row)
			}
			if strings.Contains(row.Cells[0], "orb:ed25519:") {
				t.Fatal("device label shows common prefix instead of fingerprint")
			}
		}
	}
}

type pairingTestUI struct {
	extensions.NoopUI
	action       string
	shown        func(string)
	approve      bool
	confirmation string
}

func (*pairingTestUI) Width() int  { return 80 }
func (*pairingTestUI) Height() int { return 24 }
func (*pairingTestUI) Invalidate() {}
func (ui *pairingTestUI) Custom(ctx context.Context, f extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	done := make(chan any, 1)
	var once sync.Once
	component, err := f(ui, ui.Theme(), nil, func(value any) { once.Do(func() { done <- value }) })
	if err != nil {
		return nil, false, err
	}
	panel := component.(*bridgeSettingsPanel)
	defer panel.Dispose()
	_ = panel.Render(80)
	action := ui.action
	ui.action = ""
	if action == "close" {
		panel.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	}
	if action == "show" {
		panel.list.ListSelectRow(1)
		panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	}
	if action == "copy" {
		ui.action = "show"
		panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	}
	select {
	case value := <-done:
		return value, true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}
func (ui *pairingTestUI) Editor(_ context.Context, _ string, text *string) (string, bool, error) {
	if ui.shown != nil {
		ui.shown(*text)
	}
	return "", false, nil
}
func (ui *pairingTestUI) Confirm(_ context.Context, _ string, text string, _ *extensions.DialogOptions) (bool, error) {
	ui.confirmation = text
	return ui.approve, nil
}

func TestGuidedShareWaitsForClaimAndRequiresApproval(t *testing.T) {
	bin := t.TempDir()
	copied := filepath.Join(bin, "copied")
	command := "xclip"
	if runtime.GOOS == "darwin" {
		command = "pbcopy"
	}
	if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\ncat > \"$ORB_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORB_TEST_CLIPBOARD", copied)
	for _, name := range []string{"SSH_CONNECTION", "SSH_CLIENT", "MOSH_CONNECTION", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "TERMUX_VERSION"} {
		t.Setenv(name, "")
	}
	t.Setenv("DISPLAY", ":test")
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			b, err := bridge.Open(&testBridgeStore{}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = b.Close() }()
			other, err := bridge.Open(&testBridgeStore{}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close() }()
			inv, err := b.Invite([]bridge.Grant{{GroupID: b.PersonalGroup(), Permissions: []string{"instance.inspect"}}})
			if err != nil {
				t.Fatal(err)
			}
			inv.Locator = "private-locator"
			x, y := net.Pipe()
			server := protocol.NewConn(y, b.Admin)
			client := protocol.NewConn(x, nil)
			defer func() { _ = client.Close(); _ = server.Close() }()
			ui := &pairingTestUI{action: "copy", approve: approve, shown: func(code string) {
				clipboard, err := os.ReadFile(copied)
				if err != nil || string(clipboard) != code {
					t.Fatal("Copy invitation did not copy the complete invitation shown in the editor")
				}
				parsed, err := parseBridgeInvitation(code)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err = b.Claim(other.PeerID(), parsed.ID, parsed.Token); err != nil {
					t.Error(err)
				}
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := shareBridgeInvitation(ctx, ui, client, inv, map[string]string{b.PersonalGroup(): "personal"}); err != nil {
				t.Fatal(err)
			}
			status, err := b.PairStatus(other.PeerID(), inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (status.Status == "approved") != approve {
				t.Fatal("approval choice ignored")
			}
			if !strings.Contains(ui.confirmation, other.PeerID()) || !strings.Contains(ui.confirmation, "current instances only") || !strings.Contains(ui.confirmation, "instance.inspect") {
				t.Fatal("approval omitted identity or authority")
			}
		})
	}
}

func TestPairingWaitCancelsWorkAndRejectsDifferentInvitation(t *testing.T) {
	inv := bridge.Invitation{ID: protocol.NewID(), PeerID: "expected", Expires: time.Now().Add(time.Minute).Unix()}
	stopped := make(chan struct{})
	_, err := waitBridgePairing(t.Context(), &pairingTestUI{action: "close"}, "Pair", "Wait", inv, "", func(ctx context.Context) (bridge.Invitation, error) {
		<-ctx.Done()
		close(stopped)
		return inv, ctx.Err()
	}, func(bridge.Invitation) bool { return false })
	if err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancel left pairing work running")
	}
	_, err = waitBridgePairing(t.Context(), &pairingTestUI{}, "Pair", "Wait", inv, "", func(context.Context) (bridge.Invitation, error) {
		wrong := inv
		wrong.ID = protocol.NewID()
		return wrong, nil
	}, func(bridge.Invitation) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("substituted invitation accepted: %v", err)
	}
}

func TestBridgeCLIStopWaitsForDisconnection(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "orb-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	t.Setenv("ORB_BRIDGE_HOME", root)
	dir := filepath.Join(root, "personal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.token"), []byte("owner"), 0600); err != nil {
		t.Fatal(err)
	}
	service, stop := context.WithCancel(t.Context())
	defer stop()
	closeServer, err := native.Listen(service, filepath.Join(dir, "admin.sock"), nil, "owner", func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		time.AfterFunc(100*time.Millisecond, stop)
		return connect.JSON(struct{}{}), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	var output, errors bytes.Buffer
	if code := runBridgeCommand(t.Context(), []string{"stop"}, cliStreams{Stdout: &output, Stderr: &errors}); code != 0 {
		t.Fatalf("stop: %s", errors.String())
	}
	select {
	case <-service.Done():
	default:
		t.Fatal("stop returned while the old service still accepts connections")
	}
}

func TestBridgeServiceCompatibilityStopsOnlyOlderDaemons(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			x, y := net.Pipe()
			stopped := make(chan struct{}, 1)
			var server *protocol.Conn
			server = protocol.NewConn(y, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
				if method == "stop" {
					stopped <- struct{}{}
					time.AfterFunc(10*time.Millisecond, func() { _ = server.Close() })
					return connect.JSON(struct{}{}), nil
				}
				if current {
					return connect.JSON(map[string]bool{"supports_full_access": true}), nil
				}
				return connect.JSON(struct{}{}), nil
			})
			client := protocol.NewConn(x, nil)
			defer func() { _ = client.Close(); _ = server.Close() }()
			ready, err := bridgeServiceReady(t.Context(), client)
			if err != nil || ready != current || (len(stopped) != 0) == current {
				t.Fatalf("ready=%v stopped=%d err=%v", ready, len(stopped), err)
			}
			if !current {
				select {
				case <-client.Done():
				default:
					t.Fatal("replacement started before old daemon disconnected")
				}
			}
		})
	}
}

func TestSSHSetupInstallsMissingOrOldOrbAndReusesCompatibleOrb(t *testing.T) {
	for _, test := range []struct {
		name, failure string
		old           bool
	}{{name: "missing"}, {name: "old", old: true}, {name: "shadowed", old: true}, {name: "unsupported", old: true, failure: "support Bridge"}, {name: "checksum", old: true, failure: "checksum"}, {name: "truncated", old: true, failure: "checksum"}, {name: "ssh", old: true, failure: "SSH login failed"}, {name: "custom-old", old: true, failure: "support Bridge"}} {
		t.Run(test.name, func(t *testing.T) {
			bin, dest := t.TempDir(), t.TempDir()
			ssh := "#!/bin/sh\nfor arg do command=$arg; done\nexec /bin/sh -c \"$command\"\n"
			if test.name == "ssh" {
				ssh = "#!/bin/sh\nexit 255\n"
			}
			if test.name == "truncated" {
				ssh = strings.Replace(ssh, "exec /bin/sh", "head -c 12 | /bin/sh", 1)
			}
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			t.Setenv("ORB_INSTALL_DIR", dest)
			if test.name == "shadowed" {
				if err := os.WriteFile(filepath.Join(bin, "orb"), []byte("#!/bin/sh\necho old-orb\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if test.old {
				if err := os.WriteFile(filepath.Join(dest, "orb"), []byte("#!/bin/sh\necho old-orb\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			payload := []byte("#!/bin/sh\necho 'orb bridge trust <peer-id>'\n")
			if test.name == "unsupported" {
				payload = []byte("#!/bin/sh\necho old-orb\n")
			}
			state := &release{archive: buildArchive(t, tarEntry{name: "orb", body: payload})}
			if test.name == "checksum" {
				state.checksums = "invalid"
			}
			updater := updaterFor(t, "dev", state)
			remoteOrb := "orb"
			if test.name == "custom-old" {
				remoteOrb = filepath.Join(dest, "orb")
			}
			path, err := ensureBridgeSSH(t.Context(), "server", remoteOrb, updater)
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) {
					t.Fatalf("wrong failure: %v", err)
				}
				got, readErr := os.ReadFile(filepath.Join(dest, "orb"))
				if readErr != nil || string(got) != "#!/bin/sh\necho old-orb\n" {
					t.Fatal("failed install changed the original Orb")
				}
				assertOnlyOrb(t, dest)
				if test.name == "ssh" && (state.metadataHits != 0 || strings.Count(err.Error(), "\n") != 1) {
					t.Fatal("login failure downloaded an installer or did not produce two error lines")
				}
				if test.name == "custom-old" && state.metadataHits != 0 {
					t.Fatal("explicit executable was replaced by an automatic install")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(payload) || path != filepath.Join(dest, "orb") {
				t.Fatal("verified Orb was not installed")
			}
			path, err = ensureBridgeSSH(t.Context(), "server", "orb", updater)
			if err != nil || path != filepath.Join(dest, "orb") || state.archiveHits != 1 {
				t.Fatalf("compatible Orb was not reused: %v", err)
			}
		})
	}
}

func TestBridgeHomeKeepsAdministrationOutOfEverydayFlow(t *testing.T) {
	rows := bridgeSettingsRows("", true, true, true, bridgeSettingsStatus{}, extensions.NewNoopUI().Theme())
	var actions []string
	for _, row := range rows {
		if !row.Header {
			actions = append(actions, row.Value)
		}
	}
	if strings.Join(actions, ",") != "Stop,page:add,page:advanced" {
		t.Fatalf("home actions: %v", actions)
	}
	for _, page := range []string{"", "add", "advanced"} {
		for _, row := range bridgeSettingsRows(page, true, true, false, bridgeSettingsStatus{}, extensions.NewNoopUI().Theme()) {
			switch row.Value {
			case "Grant access", "Revoke access", "Groups", "Instances", "Discovery scopes", "Operation status":
				t.Fatalf("exposed administration: %s", row.Value)
			}
		}
	}
}

func TestBridgePanelRefreshPreservesSelectionAndCancels(t *testing.T) {
	ui := &pairingTestUI{}
	panel := newBridgeSettingsPanel("personal", "", "peer:b", []tui.GridRow{{Value: "peer:a", Cells: []string{"A"}}, {Value: "peer:b", Cells: []string{"B"}}}, ui.Theme(), ui.Height, func(any) {})
	updated, stopped := make(chan struct{}), make(chan struct{})
	calls := 0
	panel.watch(t.Context(), ui, func(ctx context.Context) []tui.GridRow {
		calls++
		if calls == 1 {
			close(updated)
			return []tui.GridRow{{Value: "peer:c", Cells: []string{"C"}}, {Value: "peer:b", Cells: []string{"B connected"}}}
		}
		<-ctx.Done()
		close(stopped)
		return nil
	})
	defer panel.Dispose()
	<-updated
	deadline := time.Now().Add(time.Second)
	for {
		panel.mu.Lock()
		selected := panel.list.SelectedValue()
		rendered := strings.Join(panel.Frame.Render(80), "\n")
		panel.mu.Unlock()
		if selected != "peer:b" {
			t.Fatalf("selection jumped: %s", selected)
		}
		if strings.Contains(rendered, "B connected") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live update was not rendered")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(1100 * time.Millisecond)
	panel.Dispose()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("closing the panel did not cancel its pending refresh")
	}
}

func TestBridgeConversationListFollowsPagesAndUsesStableIDs(t *testing.T) {
	x, y := net.Pipe()
	server := protocol.NewConn(y, func(_ context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Peer   string `json:"peer_id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if method != "remote" || req.Method != "instances.list" || req.Peer != "peer" {
			t.Errorf("wrong catalog request: %s", raw)
		}
		if req.Params.Cursor == "" {
			return connect.JSON(map[string]any{"items": []bridge.Instance{{ID: "first", Alias: "same · alias", Available: true}, {ID: "stale", Alias: "closed", Available: false}}, "cursor": "next"}), nil
		}
		if req.Params.Cursor != "next" {
			t.Errorf("wrong cursor: %s", req.Params.Cursor)
		}
		return connect.JSON(map[string]any{"items": []bridge.Instance{{ID: "second", Alias: "same · alias", Available: true}}}), nil
	})
	client := protocol.NewConn(x, nil)
	defer func() { _ = client.Close(); _ = server.Close() }()
	rows, err := bridgeConversationRows(t.Context(), client, "peer", extensions.NewNoopUI().Theme())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range rows {
		if !row.Header {
			ids = append(ids, row.Value)
		}
	}
	if strings.Join(ids, ",") != "first,second" {
		t.Fatalf("wrong live conversations: %v", ids)
	}
}

func TestRemotePreviewCacheReconnectAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "private", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cache := db.Foreign("personal")
	var phase, commands atomic.Int32
	x, y := net.Pipe()
	server := protocol.NewConn(y, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if phase.Load() == 1 {
			return nil, connect.Fail("unavailable")
		}
		if phase.Load() == 2 {
			return nil, &protocol.RPCError{Code: -32000, Message: "unauthorized"}
		}
		switch method {
		case "instances.describe":
			revision := "1"
			if phase.Load() == 5 {
				revision = "2"
			}
			id := "remote-session"
			if phase.Load() == 3 {
				id = "another-session"
			}
			return connect.JSON(remoteDescriptor{Name: "Remote title", CWD: "/remote/project", Generation: "1", Target: agent.ControlTarget{SessionID: id, Revision: revision}, Methods: []string{"prompt"}}), nil
		case "events.subscribe":
			if phase.Load() == 4 {
				phase.Store(5)
			}
			return connect.JSON(map[string]any{"snapshot_id": "snapshot", "cursor": "1", "messages": []json.RawMessage{json.RawMessage(`{"role":"user","content":"hello from remote"}`), json.RawMessage(`{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"remote answer"}]}`)}}), nil
		case "instances.call":
			commands.Add(1)
		}
		return connect.JSON(struct{}{}), nil
	})
	client := protocol.NewConn(x, nil)
	defer func() { _ = client.Close(); _ = server.Close() }()
	requests := make(chan string, 1)
	body, status := &remoteTranscript{}, &remoteTranscript{}
	changed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRemoteConversation(ctx, "instance", func(method string, p, result any) error { return client.Call(ctx, method, p, result) }, requests, body, status, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		}, cache, "peer", "remote-session")
	}()
	defer func() { cancel(); <-done }()
	wait := func(contains string) {
		t.Helper()
		for {
			status.mu.Lock()
			text := status.text
			status.mu.Unlock()
			if strings.Contains(text, contains) {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("status never reached", contains, text)
			}
		}
	}
	wait("idle")
	rows, err := cache.List(ctx, "peer")
	if err != nil || len(rows) != 1 || rows[0].CWD != "/remote/project" {
		t.Fatalf("cache: %+v %v", rows, err)
	}
	if text := cachedTranscript(rows[0]); !strings.Contains(text, "remote answer") || strings.Contains(text, "hidden") {
		t.Fatal(text)
	}
	phase.Store(1)
	wait("Offline")
	requests <- "must not execute"
	wait("Read-only")
	if commands.Load() != 0 {
		t.Fatal("offline control was dispatched")
	}
	phase.Store(0)
	wait("idle")
	phase.Store(4)
	wait("Session changed")
	phase.Store(1)
	wait("Offline")
	phase.Store(3)
	wait("no longer active")
	requests <- "must not retarget"
	wait("Read-only")
	if commands.Load() != 0 {
		t.Fatal("cached session was retargeted")
	}
	phase.Store(2)
	wait("Access revoked")
	rows, err = cache.List(ctx, "peer")
	if err != nil || len(rows) != 0 {
		t.Fatal("revoked cache retained", err)
	}
	body.mu.Lock()
	remaining := body.text
	body.mu.Unlock()
	if remaining != "" {
		t.Fatal("revoked transcript remained visible")
	}
}

func TestBridgeLiveForeignPreview(t *testing.T) {
	if os.Getenv("ORB_BRIDGE_LIVE_CACHE") != "1" {
		t.Skip("isolated native live fixture")
	}
	peer := os.Getenv("ORB_BRIDGE_LIVE_PEER")
	if peer == "" || !filepath.IsAbs(os.Getenv("ORB_BRIDGE_HOME")) {
		t.Fatal("isolated Bridge home and peer required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	agentDir := os.Getenv(config.EnvAgentDir)
	if !filepath.IsAbs(agentDir) {
		t.Fatal("isolated agent directory required")
	}
	nativeState, err := openNativeState(ctx, agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nativeState.close() }()
	ctx = context.WithValue(ctx, nativeStateKey{}, nativeState)
	client, err := bridgeAdmin(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	remote := func(method string, p, result any) error {
		return client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": method, "params": p}, result)
	}
	var catalog struct {
		Items []bridge.Instance `json:"items"`
	}
	if err := remote("instances.list", struct{}{}, &catalog); err != nil {
		t.Fatal(err)
	}
	instance := ""
	for _, i := range catalog.Items {
		if i.Available {
			instance = i.ID
			break
		}
	}
	if instance == "" {
		t.Fatal("no live instance")
	}
	db, err := openBridgeCache(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cache := db.Foreign("personal")
	requests := make(chan string, 1)
	body, status := &remoteTranscript{}, &remoteTranscript{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRemoteConversation(ctx, instance, remote, requests, body, status, func() {}, cache, peer, "")
	}()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	sent := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("live preview timeout")
		case <-ticker.C:
		}
		status.mu.Lock()
		state := status.text
		status.mu.Unlock()
		if !sent && strings.HasPrefix(state, "idle") {
			requests <- "Please answer for the isolated preview cache check."
			sent = true
		}
		rows, err := cache.List(ctx, peer)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 && strings.Contains(cachedTranscript(rows[0]), "live bridge answer") {
			if err := client.Call(ctx, "block", map[string]string{"peer_id": peer}, nil); err != nil {
				t.Fatal(err)
			}
			rows, err = cache.List(ctx, peer)
			if err != nil || len(rows) != 0 {
				t.Fatal("local block retained foreign preview", err)
			}
			t.Log("native Bridge prompt, completed preview persistence, and block purge verified")
			return
		}
	}
}
