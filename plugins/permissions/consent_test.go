package permissions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
)

func permissionRunner(t *testing.T, policy *Policy, cwd string) *extensions.Runner {
	t.Helper()
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("permissions", Extension(policy, nil, nil)); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, SessionManager: manager})
}
func emit(r *extensions.Runner, ctx context.Context, tool string, args map[string]any) *extensions.ToolCallResult {
	return r.EmitToolCall(ctx, extensions.ToolCallEvent{ToolName: tool, Input: args})
}
func blocked(r *extensions.ToolCallResult) bool { return r != nil && r.Block }
func askPolicy() *Policy                        { return &Policy{Mode: "enforce", Rules: []Rule{{Tool: "*", Action: Ask}}} }

func TestConsentFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		err         error
		fallback    Action
		handler     bool
	}{
		{name: "headless"}, {name: "dismissed", err: context.Canceled, handler: true},
		{name: "dismissed despite allow fallback", err: context.Canceled, fallback: Allow, handler: true},
		{name: "failed prompt", err: errors.New("offline"), fallback: Allow, handler: true},
		{name: "invalid reply", reply: "yes please", handler: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := askPolicy()
			p.AskFallback = tc.fallback
			r := permissionRunner(t, p, t.TempDir())
			ctx := t.Context()
			if tc.handler {
				ctx = extensions.WithInputHandler(ctx, func(context.Context, string, []string) (string, error) { return tc.reply, tc.err })
			}
			if got := emit(r, ctx, "bash", map[string]any{"command": "echo probe"}); !blocked(got) {
				t.Fatalf("operation allowed: %#v", got)
			}
		})
	}
}

func TestApprovalScopeAndPrompt(t *testing.T) {
	p := askPolicy()
	cwd := t.TempDir()
	r := permissionRunner(t, p, cwd)
	prompts := 0
	ctx := extensions.WithInputHandler(t.Context(), func(_ context.Context, title string, _ []string) (string, error) {
		prompts++
		if !strings.Contains(title, `"query":"fact"`) || !strings.Contains(title, cwd) {
			t.Errorf("incomplete prompt: %q", title)
		}
		return "s approve for this session", nil
	})
	args := map[string]any{"query": "fact"}
	for _, name := range []string{"recall", "recall", "forget"} {
		if blocked(emit(r, ctx, name, args)) {
			t.Fatal("approved call denied")
		}
	}
	if prompts != 2 {
		t.Fatalf("prompts=%d, want 2", prompts)
	}
	other := permissionRunner(t, p, filepath.Join(cwd, "other"))
	emit(other, ctx, "recall", args)
	if prompts != 3 {
		t.Fatalf("cwd reused approval: %d", prompts)
	}
	p.SetMode("log")
	p.SetMode("enforce")
	emit(r, ctx, "recall", args)
	if prompts != 4 {
		t.Fatalf("mode change reused approval: %d", prompts)
	}
}

func TestGuardsAndAuthorizerErrors(t *testing.T) {
	for _, mode := range []string{"enforce", "auto", "log"} {
		for _, result := range []Action{Ask, Allow, Deny} {
			p := &Policy{Mode: mode, Authorizer: func(context.Context, ToolCallInfo) (Action, error) { return result, nil }, Guards: []func(context.Context, ToolCallInfo) string{func(context.Context, ToolCallInfo) string { return "hard limit" }}}
			if got := emit(permissionRunner(t, p, t.TempDir()), t.Context(), "bash", nil); !blocked(got) {
				t.Fatalf("guard bypassed in %s with %s", mode, result)
			}
		}
		p := &Policy{Mode: mode, Authorizer: func(context.Context, ToolCallInfo) (Action, error) { return "", errors.New("authorizer offline") }}
		if !blocked(emit(permissionRunner(t, p, t.TempDir()), t.Context(), "bash", nil)) {
			t.Fatalf("authorizer error allowed in %s", mode)
		}
	}
}

func TestAutoPermissions(t *testing.T) {
	for _, headless := range []bool{false, true} {
		p, err := FromSettings(map[string]any{"mode": "auto", "rules": []Rule{
			{Tool: "*", Action: Ask},
			{Tool: "read", Path: "public/**", Action: Allow},
			{Tool: "read", Path: "secrets/**", Action: Deny},
			{Tool: "bash", Command: "git push *", Action: Deny},
			{Tool: "bash", Command: "echo one && echo two", Action: Allow},
			{Tool: "remove", Action: Deny},
		}})
		if err != nil {
			t.Fatal(err)
		}
		r := permissionRunner(t, p, t.TempDir())
		ctx := t.Context()
		if !headless {
			ctx = extensions.WithInputHandler(ctx, func(context.Context, string, []string) (string, error) {
				t.Error("auto mode prompted")
				return "n deny", nil
			})
		}
		for _, tc := range []struct {
			tool, key, value string
			deny             bool
		}{
			{"write", "path", "file", false},
			{"read", "path", "public/nested/file", false},
			{"read", "path", "secrets/key", true},
			{"bash", "command", "git status", false},
			{"bash", "command", "git push", true},
			{"bash", "command", "git push origin main", true},
			{"bash", "command", "cd /tmp && git push", true},
			{"bash", "command", "echo $(git push)", true},
			{"bash", "command", "", true},
			{"bash", "command", "echo one && echo two", false},
			{"remove", "path", "file", true},
		} {
			got := emit(r, ctx, tc.tool, map[string]any{tc.key: tc.value})
			if got == nil || got.Block != tc.deny || got.Approved == tc.deny {
				t.Errorf("%s %q (headless=%v): %#v", tc.tool, tc.value, headless, got)
			}
		}
		if _, hidden := p.staticDeny(ToolCallInfo{Tool: "remove"}); !hidden {
			t.Error("auto exposed a statically denied tool")
		}
		if d := p.recent(0)[0]; d.Action != Ask || d.Resolved != Allow || d.Resolution != "auto approval" || d.Mode != "auto" {
			t.Fatalf("missing auto audit: %#v", d)
		}
		p.SetMode("enforce")
		if !blocked(emit(r, t.Context(), "write", map[string]any{"path": "file"})) {
			t.Fatal("auto consent survived switching to enforce")
		}
		p.SetMode("auto")
		if got := emit(r, ctx, "write", nil); got == nil || !got.Approved {
			t.Fatalf("SetMode did not enable auto: %#v", got)
		}
	}
}

func TestAutoNativeConsentAndCancellation(t *testing.T) {
	p := &Policy{Mode: "auto"}
	r := permissionRunner(t, p, t.TempDir())
	if got := emit(r, t.Context(), "write", nil); got == nil || !got.Approved {
		t.Fatalf("explicit auto mode did not authorize native tool: %#v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	p.Authorizer = func(context.Context, ToolCallInfo) (Action, error) { cancel(); return Ask, nil }
	if !blocked(emit(r, ctx, "write", nil)) {
		t.Fatal("auto approved after cancellation during authorization")
	}
}

func TestPassivePolicyDoesNotApproveNativeTools(t *testing.T) {
	for _, p := range []*Policy{{Mode: "log", Rules: []Rule{{Tool: "*", Action: Deny}}}, {Mode: "enforce"}} {
		if got := emit(permissionRunner(t, p, t.TempDir()), t.Context(), "write", map[string]any{"path": "file"}); got != nil {
			t.Fatalf("passive decision became approval: %#v", got)
		}
	}
}

func TestPathAndCommandRules(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		rules      []Rule
		want       Action
	}{
		{"nested glob", "read", map[string]any{"path": "secrets/nested/key"}, []Rule{{Path: "secrets/**", Action: Deny}}, Deny},
		{"new directories below symlink", "write", map[string]any{"path": "link/new/deep/file"}, []Rule{{Path: filepath.Join(real, "new/deep/file"), Action: Deny}}, Deny},
		{"symlink allow cannot override target deny", "write", map[string]any{"path": "link/file"}, []Rule{{Path: filepath.Join(real, "**"), Action: Deny}, {Path: filepath.Join(root, "l*", "**"), Action: Allow}}, Deny},
		{"mixed paths", "copy", map[string]any{"paths": []any{"secrets/key", "public/file"}}, []Rule{{Path: "secrets/**", Action: Deny}, {Path: "public/**", Action: Allow}}, Deny},
		{"compound allow", "bash", map[string]any{"command": "git status; echo other"}, []Rule{{Tool: "bash", Action: Ask}, {Tool: "bash", Command: "git status*", Action: Allow}}, Ask},
		{"compound restriction", "bash", map[string]any{"command": "cd /tmp && git push"}, []Rule{{Tool: "bash", Command: "git push*", Action: Deny}}, Ask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Policy{Rules: tc.rules}
			got := p.Evaluate(t.Context(), ToolCallInfo{Tool: tc.tool, Args: tc.args, CWD: root})
			if got.Action != tc.want {
				t.Fatalf("%s, want %s", got.Action, tc.want)
			}
		})
	}
}

func TestCancelledApprovalWaiterReturns(t *testing.T) {
	p := askPolicy()
	r := permissionRunner(t, p, t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	ctx := extensions.WithInputHandler(t.Context(), func(context.Context, string, []string) (string, error) {
		close(entered)
		<-release
		return "n deny", nil
	})
	go func() { defer close(done); emit(r, ctx, "first", nil) }()
	<-entered
	defer func() { close(release); <-done }()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	finished := make(chan *extensions.ToolCallResult, 1)
	go func() { finished <- emit(r, ctx, "second", nil) }()
	select {
	case result := <-finished:
		if !blocked(result) {
			t.Fatal("cancelled call allowed")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter stuck behind another approval")
	}
}

func TestApprovalNeedsSessionAndCanonicalScope(t *testing.T) {
	p := askPolicy()
	d := Decision{Matcher: "ask", Input: `{"path":"file"}`}
	info := ToolCallInfo{Tool: "write", CWD: t.TempDir()}
	p.approveForSession(info, d)
	if p.approvedForSession(info, d) {
		t.Fatal("approval cached without a session")
	}
	info.SessionID = "session"
	p.approveForSession(info, d)
	info.CWD = filepath.Join(info.CWD, "other")
	if p.approvedForSession(info, d) {
		t.Fatal("approval crossed working directories")
	}
}

func TestDefaultAutoStillEnforcesDenials(t *testing.T) {
	p, err := FromSettings(map[string]any{"enabled": true, "rules": []Rule{{Tool: "write", Action: Deny}}})
	if err != nil {
		t.Fatal(err)
	}
	if !blocked(emit(permissionRunner(t, p, t.TempDir()), t.Context(), "write", nil)) {
		t.Fatal("enabled rule silently ran in audit mode")
	}
	if p.Mode != "auto" {
		t.Fatalf("default mode = %s", p.Mode)
	}
}

func TestDefaultAutoApprovesAsks(t *testing.T) {
	for _, settings := range []map[string]any{nil, {"preset": "workspace-write"}} {
		p, err := FromSettings(settings)
		if err != nil {
			t.Fatal(err)
		}
		p.Rules = []Rule{{Tool: "*", Action: Ask}}
		if got := emit(permissionRunner(t, p, t.TempDir()), t.Context(), "write", nil); got == nil || !got.Approved {
			t.Fatalf("default mode prompted or denied: %#v", got)
		}
	}
	p := &Policy{Rules: []Rule{{Tool: "*", Action: Ask}}}
	if got := emit(permissionRunner(t, p, t.TempDir()), t.Context(), "write", nil); got == nil || !got.Approved {
		t.Fatalf("SDK default mode prompted or denied: %#v", got)
	}
}
