package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
	"github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/plugins/subagents"
	"github.com/OrdalieTech/orb/sandbox"
	"github.com/OrdalieTech/orb/tui"
)

type selectorUI struct {
	extensions.NoopUI
	choices []string
	index   int
	keys    []string // raw key events fed to the /plugins window component
}

func (ui *selectorUI) Select(_ context.Context, _ string, _ []string, _ *extensions.DialogOptions) (string, bool, error) {
	choice := ui.choices[ui.index]
	ui.index++
	return choice, true, nil
}

type noopHost struct{}

func (noopHost) Width() int { return 100 }

func (noopHost) Height() int { return 40 }

func (noopHost) Invalidate() {}

// Custom runs the window factory headlessly and replays the scripted keys.
func (ui *selectorUI) Custom(_ context.Context, factory extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	var result any
	component, err := factory(noopHost{}, extensions.NewNoopUI().Theme(), nil, func(value any) { result = value })
	if err != nil {
		return nil, false, err
	}
	handler, ok := component.(interface{ HandleInput(tui.KeyEvent) })
	if !ok {
		return nil, false, fmt.Errorf("window component does not handle input")
	}
	if len(component.Render(80)) == 0 {
		return nil, false, fmt.Errorf("window component renders nothing")
	}
	for _, key := range ui.keys {
		handler.HandleInput(tui.KeyEvent{Raw: key})
	}
	return result, true, nil
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func TestPluginControlPersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:plugin-control>", Control("", "", settings)))
	ui := &selectorUI{keys: []string{" ", "\x1b"}} // toggle the first row (tasks), close
	reloads := 0
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
		CommandActions: &extensions.CommandActions{Reload: func(context.Context) error { reloads++; return nil }},
	})
	command := runner.Command("plugins")
	require(t, command != nil, "/plugins missing")
	mustOK(command.Handler(context.Background(), "", runner.CreateCommandContext()))
	require(t, settings.GetPlugins()["tasks"] && reloads == 1, "tasks=%t reloads=%d", settings.GetPlugins()["tasks"], reloads)
}

func TestPermissionsPresetsAndSandboxMode(t *testing.T) {
	checkPolicy := func(settings map[string]any, mode string, sandboxMode sandbox.Mode) {
		policy := must(permissions.FromSettings(settings))
		require(t, policy.Mode == mode && policy.Sandbox == sandboxMode, "policy = %#v", policy)
	}
	checkPolicy(map[string]any{"preset": "workspace-write"}, "enforce", sandbox.ModeWorkspaceWrite)
	checkPolicy(map[string]any{"preset": "danger-full-access"}, "log", sandbox.ModeDangerFullAccess)
	checkPolicy(map[string]any{"preset": "workspace-write", "mode": "log", "sandbox": "read-only"}, "log", sandbox.ModeReadOnly)
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	checkMode := func(want sandbox.Mode) {
		got, err := permissions.SandboxMode(settings)
		require(t, err == nil && got == want, "sandbox mode = %q, %v", got, err)
	}
	checkMode(sandbox.ModeDangerFullAccess)
	settings.SetPluginSetting("permissions", "preset", "workspace-write")
	checkMode(sandbox.ModeWorkspaceWrite)
	settings.SetPluginSetting("permissions", "sandbox", "read-only")
	checkMode(sandbox.ModeReadOnly)
	for _, invalid := range []map[string]any{{"preset": true}, {"preset": "unknown"}, {"sandbox": nil}, {"sandbox": true}, {"sandbox": "unknown"}, {"mode": ""}, {"mode": "audit"}, {"askFallback": "ask"}, {"rules": nil}, {"rules": []permissions.Rule{{Action: "audit"}}}, {"rules": []permissions.Rule{{Tool: "[", Action: permissions.Deny}}}, {"rules": []permissions.Rule{{Command: "[", Action: permissions.Deny}}}, {"rules": []permissions.Rule{{Path: "[", Action: permissions.Deny}}}, {"rules": []any{map[string]any{"tool": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"command": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"path": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"commnad": "git *", "action": "allow"}}}, {"mdoe": "enforce"}} {
		_, err := permissions.FromSettings(invalid)
		require(t, err != nil, "settings %#v accepted", invalid)
	}
	for _, invalid := range [][4]any{{"sandbox", "unknown", "read-only", "permissions.sandbox"}, {"mode", "", "log", "permissions.mode"}, {"mode", "audit", "log", "permissions.mode"}, {"askFallback", "ask", "allow", "permissions.askFallback"}, {"rules", []permissions.Rule{{Action: "audit"}}, []permissions.Rule{}, "permissions.rules[0].action"}, {"rules", []permissions.Rule{{Tool: "[", Action: permissions.Deny}}, []permissions.Rule{}, "permissions.rules[0]"}, {"rules", []permissions.Rule{{Command: "[", Action: permissions.Deny}}, []permissions.Rule{}, "permissions.rules[0]"}, {"rules", []permissions.Rule{{Path: "[", Action: permissions.Deny}}, []permissions.Rule{}, "permissions.rules[0]"}, {"rules", []any{map[string]any{"tool": nil, "action": "allow"}}, []permissions.Rule{}, "rules[0].tool"}, {"rules", []any{map[string]any{"command": nil, "action": "allow"}}, []permissions.Rule{}, "rules[0].command"}, {"rules", []any{map[string]any{"path": nil, "action": "allow"}}, []permissions.Rule{}, "rules[0].path"}, {"rules", []any{map[string]any{"commnad": "git *", "action": "allow"}}, []permissions.Rule{}, "rules[0].commnad"}} {
		key := invalid[0].(string)
		settings.SetPluginSetting("permissions", key, invalid[1])
		registry := extensions.NewRegistry(root)
		mustOK(registry.Register("<inline:permissions>", Catalog(CatalogOptions{Settings: settings})["permissions"]))
		blocked := extensions.NewRunner(registry, extensions.RunnerOptions{}).EmitToolCall(context.Background(), extensions.ToolCallEvent{ToolName: "read"})
		require(t, blocked != nil && blocked.Block && strings.Contains(blocked.Reason, invalid[3].(string)), "invalid SDK policy for %s = %#v", key, blocked)
		settings.SetPluginSetting("permissions", key, invalid[2])
	}
	settings.SetPluginEnabled("permissions", false)
	checkMode(sandbox.ModeReadOnly)
	got, err := permissions.SandboxMode(nil)
	require(t, err == nil && got == sandbox.ModeDangerFullAccess, "unset sandbox mode = %q, %v", got, err)
}

func TestPermissionsPolicyRules(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	mustOK(os.Mkdir(realDir, 0o755))
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tests := []struct {
		name   string
		policy *permissions.Policy
		info   permissions.ToolCallInfo
		want   permissions.Action
	}{
		{"last match wins", &permissions.Policy{Rules: []permissions.Rule{{Tool: "*", Action: permissions.Allow}, {Tool: "bash", Action: permissions.Deny}, {Tool: "bash", Command: "git status*", Action: permissions.Allow}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "git status --short"}, CWD: root}, permissions.Allow},
		{"tool glob", &permissions.Policy{Rules: []permissions.Rule{{Tool: "mcp_*", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "mcp_delete", Args: map[string]any{}, CWD: root}, permissions.Deny},
		{"command glob treats slash as command text", &permissions.Policy{Rules: []permissions.Rule{{Tool: "bash", Command: "rm -rf *", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "rm -rf /tmp/example"}, CWD: root}, permissions.Deny},
		{"raw path", &permissions.Policy{Rules: []permissions.Rule{{Path: "link/*", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "custom", Args: map[string]any{"path": "link/file"}, CWD: root}, permissions.Deny},
		{"canonical path", &permissions.Policy{Rules: []permissions.Rule{{Path: filepath.Join(realDir, "*"), Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(link, "file")}, CWD: root}, permissions.Deny},
		{"canonical rule path", &permissions.Policy{Rules: []permissions.Rule{{Path: filepath.Join(link, "*"), Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(realDir, "file")}, CWD: root}, permissions.Deny},
		{"path rule matches a path inside a bash command", &permissions.Policy{Rules: []permissions.Rule{{Path: "secrets.txt", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat secrets.txt"}, CWD: root}, permissions.Deny},
		{"path rule ignores unrelated bash commands", &permissions.Policy{Rules: []permissions.Rule{{Path: "secrets.txt", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "ls -la"}, CWD: root}, permissions.Allow},
		{"path rule matches a redirect target", &permissions.Policy{Rules: []permissions.Rule{{Path: "*.env", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "echo TOKEN=1 > prod.env"}, CWD: root}, permissions.Deny},
		{"unparseable bash is ask with restrictive rule", &permissions.Policy{Rules: []permissions.Rule{{Tool: "bash", Command: "git push*", Action: permissions.Deny}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, permissions.Ask},
		{"unparseable bash is allow without restrictive rule", &permissions.Policy{Rules: []permissions.Rule{{Tool: "bash", Action: permissions.Allow}}}, permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, permissions.Allow},
		{"authorizer deny is final", &permissions.Policy{Authorizer: func(context.Context, permissions.ToolCallInfo) (permissions.Action, error) {
			return permissions.Deny, nil
		}, Guards: []func(context.Context, permissions.ToolCallInfo) string{func(context.Context, permissions.ToolCallInfo) string { panic("guard ran") }}, Rules: []permissions.Rule{{Tool: "*", Action: permissions.Allow}}}, permissions.ToolCallInfo{Tool: "todo"}, permissions.Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.policy.Evaluate(context.Background(), test.info).Action
			require(t, got == test.want, "action = %q, want %q", got, test.want)
		})
	}
	order := []string{}
	guard := func(label, reason string) func(context.Context, permissions.ToolCallInfo) string {
		return func(context.Context, permissions.ToolCallInfo) string { order = append(order, label); return reason }
	}
	policy := &permissions.Policy{
		Authorizer: func(context.Context, permissions.ToolCallInfo) (permissions.Action, error) {
			order = append(order, "authorizer")
			return permissions.Allow, nil
		},
		Guards: []func(context.Context, permissions.ToolCallInfo) string{guard("guard 1", ""), guard("guard 2", "extension denied"), guard("guard 3", "")},
		Rules:  []permissions.Rule{{Tool: "*", Action: permissions.Allow}},
	}
	decision := policy.Evaluate(context.Background(), permissions.ToolCallInfo{Tool: "todo"})
	require(t, decision.Action == permissions.Deny && decision.Resolution == "extension denied" && strings.Join(order, ",") == "authorizer,guard 1,guard 2", "guard decision = %#v, order = %v", decision, order)
	policy.Guards, policy.Rules = nil, []permissions.Rule{{Tool: "*", Action: permissions.Deny}}
	got := policy.Evaluate(context.Background(), permissions.ToolCallInfo{Tool: "todo"}).Action
	require(t, got == permissions.Allow, "authorizer allow was not final: %q", got)
}

func TestPermissionsEnforceHidesAndBlocksStaticDeny(t *testing.T) {
	logSession := newPermissionsSession(t, faux.New(), &permissions.Policy{Mode: "log", Rules: []permissions.Rule{{Tool: "bash", Action: permissions.Deny}}})
	require(t, containsName(logSession.GetActiveToolNames(), "bash"), "log mode hid bash")
	conditionalSession := newPermissionsSession(t, faux.New(), &permissions.Policy{Mode: "enforce", Rules: []permissions.Rule{{Tool: "bash", Command: "rm -rf *", Action: permissions.Deny}}})
	require(t, containsName(conditionalSession.GetActiveToolNames(), "bash"), "command-scoped deny hid the whole tool")

	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "touch " + marker}, faux.ToolCallOptions{ID: "deny-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "bash")
			return faux.AssistantMessage("done"), nil
		}),
	})
	policy := &permissions.Policy{Mode: "enforce", Rules: []permissions.Rule{{Tool: "bash", Action: permissions.Deny}}}
	session := newPermissionsSession(t, provider, policy)
	require(t, !containsName(session.GetActiveToolNames(), "bash"), "bash remained visible after session_start")
	active := append(session.GetActiveToolNames(), "bash")
	mustOK(session.SetActiveToolsByName(active))
	mustOK(session.PromptSync(context.Background(), "try it"))
	require(t, strings.Contains(returned, `permissions: denied by rule 1 (tool="bash")`), "tool result = %q", returned)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied command changed the filesystem: %v", err)
	}
}

func TestPermissionsAskFallbackDeniesHeadless(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("todo", map[string]any{"items": []any{}}, faux.ToolCallOptions{ID: "ask-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "todo")
			return faux.AssistantMessage("done"), nil
		}),
	})
	policy := &permissions.Policy{Mode: "enforce", AskFallback: permissions.Deny, Rules: []permissions.Rule{{Tool: "todo", Action: permissions.Ask}}}
	session := newPermissionsSession(t, provider, policy, "tasks")
	mustOK(session.PromptSync(context.Background(), "update tasks"))
	require(t, strings.Contains(returned, "ask resolved by askFallback"), "tool result = %q", returned)
}

type approvalUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	selects int
}

func (ui *approvalUI) Select(context.Context, string, []string, *extensions.DialogOptions) (string, bool, error) {
	ui.mu.Lock()
	ui.selects++
	ui.mu.Unlock()
	return "s approve for this session", true, nil
}

func (ui *approvalUI) count() int { ui.mu.Lock(); defer ui.mu.Unlock(); return ui.selects }

func TestPermissionsSessionApprovalAvoidsSecondPrompt(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	call := map[string]any{"items": []any{map[string]any{"text": "ship", "status": "pending"}}}
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("todo", call, faux.ToolCallOptions{ID: "ask-1"})),
		faux.AssistantMessage(faux.ToolCall("todo", call, faux.ToolCallOptions{ID: "ask-2"})),
		faux.AssistantMessage("done"),
	})
	policy := &permissions.Policy{Mode: "enforce", Rules: []permissions.Rule{{Tool: "todo", Action: permissions.Ask}}}
	session := newPermissionsSession(t, provider, policy, "tasks")
	ui := &approvalUI{}
	session.ExtensionRunner().SetUI(ui, extensions.ModeTUI)
	mustOK(session.PromptSync(context.Background(), "update twice"))
	if got := ui.count(); got != 1 {
		t.Fatalf("permission prompts = %d, want 1", got)
	}
	logged := 0
	for _, entry := range session.Manager().GetEntries() {
		if entry.CustomType == "orb.permissions.decision" {
			logged++
		}
	}
	require(t, logged == 2, "decision log entries = %d, want 2", logged)
}

func TestMemoryCatalogUsesAgentDirStore(t *testing.T) {
	agentDir := t.TempDir()
	tool := pluginTool(t, "memory", "remember", CatalogOptions{AgentDir: agentDir}, extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "remember-local", map[string]any{
		"target": "memory", "content": "Local profile marker.",
	}, nil))
	store := must(filestore.NewFileStore(filepath.Join(agentDir, "memory")))
	items := must(store.Query(context.Background(), memorysdk.Filter{}))
	if len(items) != 1 || items[0].Content != "Local profile marker." {
		t.Fatalf("local items = %#v", items)
	}
}

func pluginTool(t *testing.T, plugin, tool string, options CatalogOptions, runnerOptions extensions.RunnerOptions) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	factory := Catalog(options)[plugin]
	if factory == nil {
		t.Fatalf("plugin %q missing", plugin)
	}
	mustOK(registry.Register("<inline:"+plugin+">", factory))
	manager := must(sessionstore.InMemory(t.TempDir()))
	runnerOptions.SessionManager = manager
	runnerOptions.Actions.GetActiveTools = func() ([]string, error) { return []string{tool}, nil }
	runner := extensions.NewRunner(registry, runnerOptions)
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == tool {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("tool %q missing", tool)
	return nil
}

func newPermissionsSession(t *testing.T, provider *faux.Provider, policy *permissions.Policy, enabled ...string) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	manager := must(sessionstore.InMemory(root))
	registry := extensions.NewRegistry(root)
	catalog := Catalog(CatalogOptions{StreamFn: provider.StreamSimple, Policy: policy})
	for _, name := range append([]string{"permissions"}, enabled...) {
		mustOK(registry.Register("<inline:"+name+">", catalog[name]))
	}
	prompt := "permissions test"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func containsName(names []string, want string) bool { return slices.Contains(names, want) }

func toolResultText(request ai.Context, name string) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == name {
			return ai.ContentText(message.Content)
		}
	}
	return ""
}

// Command words widen the path candidate set, and rules are last-match-wins, so
// widening an allow would let it override an earlier deny.
func TestPermissionsCommandPathsDoNotWidenAllowRules(t *testing.T) {
	policy := &permissions.Policy{Mode: "enforce", Rules: []permissions.Rule{
		{Path: "secrets.txt", Action: permissions.Deny},
		{Path: "src/**", Action: permissions.Allow},
	}}
	info := permissions.ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat src/a.go && cat secrets.txt"}}
	decision := policy.Evaluate(context.Background(), info)
	require(t, decision.Action == permissions.Deny, "action = %q, want deny: an allow widened by command words outranked the deny", decision.Action)
}

func TestToggleExternalCLIPreservesCommandsAndAddsKnownCLIs(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", map[string]any{"claude": "/bin/cat"})
	mustOK(subagents.ToggleExternalCLI(settings, "claude"))
	entries := must(subagents.ExternalEntries(settings))
	require(t, !entries["claude"].Enabled && entries["claude"].Command == "/bin/cat", "after off: %#v", entries)
	mustOK(subagents.ToggleExternalCLI(settings, "claude"))
	raw := settingsObjectValue(settings.GetPluginSettings("subagents")["external"])
	require(t, raw["claude"] == "/bin/cat", "re-enabled entry should collapse to the string form: %#v", raw)
	mustOK(subagents.ToggleExternalCLI(settings, "codex"))
	entries = must(subagents.ExternalEntries(settings))
	require(t, entries["codex"].Enabled && strings.HasPrefix(entries["codex"].Command, "codex exec"), "detected add: %#v", entries)
	require(t, subagents.ToggleExternalCLI(settings, "nope") != nil, "unknown CLI must be rejected")
	require(t, len(settings.DrainErrors()) == 0, "settings errors")
}

func TestCapabilitiesWithDedicatedSettingsHaveNoPluginToggles(t *testing.T) {
	settings := must(config.NewSettingsManager(t.TempDir(), config.WithAgentDir(t.TempDir())))
	for _, row := range pluginGridRows(settings, extensions.NewNoopUI().Theme()) {
		if row.Value == "bridge" || row.Value == "bridge-agent-calls" || row.Value == "provider-usage" {
			t.Fatalf("duplicate settings toggle: %s", row.Value)
		}
	}
}
