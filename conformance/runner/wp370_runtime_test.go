package runner_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type wp370RuntimeFixture struct {
	SchemaVersion int `json:"schemaVersion"`
	Cases         struct {
		Success   wp370RuntimeCase     `json:"success"`
		Cancelled wp370RuntimeCase     `json:"cancelled"`
		Dispose   []wp370RuntimeRecord `json:"dispose"`
	} `json:"cases"`
}

type wp370RuntimeCase struct {
	Result  extensions.SessionReplacementResult `json:"result"`
	Records []wp370RuntimeRecord                `json:"records"`
}

type wp370RuntimeRecord struct {
	Phase string         `json:"phase"`
	Event map[string]any `json:"event,omitempty"`
	CWD   string         `json:"cwd,omitempty"`
}

func TestWP370RuntimeLifecycleMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "WP370Runtime")
	if manifest.Family != "WP370Runtime" || manifest.Generator != "conformance/extract/wp370-runtime.ts" {
		t.Fatalf("unexpected WP370Runtime manifest: %+v", manifest)
	}
	var fixture wp370RuntimeFixture
	runner.LoadJSON(t, "WP370Runtime", "lifecycle.json", &fixture)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("WP370Runtime schema version = %d", fixture.SchemaVersion)
	}

	for _, test := range []struct {
		name   string
		cancel bool
		want   wp370RuntimeCase
	}{
		{name: "success", want: fixture.Cases.Success},
		{name: "cancelled", cancel: true, want: fixture.Cases.Cancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runWP370NewSession(t, test.cancel)
			assertWP370JSONEqual(t, test.want, got)
		})
	}

	gotDispose := runWP370Dispose(t)
	assertWP370JSONEqual(t, fixture.Cases.Dispose, gotDispose)
}

func runWP370NewSession(t *testing.T, cancel bool) wp370RuntimeCase {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := faux.New()
	records := make([]wp370RuntimeRecord, 0, 8)
	registry := wp370RuntimeRegistry(t, cwd, cancel, &records)
	factory := func(ctx context.Context, options codingagent.AgentSessionOptions) (*codingagent.AgentSessionResult, error) {
		if options.SessionStartEvent != nil {
			records = append(records, wp370RuntimeRecord{
				Phase: "create",
				Event: wp370StartEvent(*options.SessionStartEvent),
				CWD:   "/fixture",
			})
		}
		return codingagent.NewAgentSession(options)
	}
	host, err := codingagent.NewAgentSessionRuntime(context.Background(), codingagent.AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, SessionManager: manager,
		StreamFn: provider.StreamSimple, Model: provider.GetModel(), ExtensionRegistry: registry,
	}, factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	records = records[:0]
	host.SetBeforeSessionInvalidate(func() {
		records = append(records, wp370RuntimeRecord{Phase: "beforeSessionInvalidate"})
	})
	host.SetRebindSession(func(session *codingagent.AgentSession) error {
		records = append(records, wp370RuntimeRecord{Phase: "rebindSession"})
		return session.BindExtensions(context.Background())
	})
	result, err := host.NewSession(context.Background(), &extensions.NewSessionOptions{
		Setup: func(*sessionstore.SessionManager) error {
			records = append(records, wp370RuntimeRecord{Phase: "setup"})
			return nil
		},
		WithSession: func(_ context.Context, replaced extensions.ReplacedSessionContext) error {
			if replaced.CWD() != cwd {
				t.Fatalf("replacement cwd = %q, want %q", replaced.CWD(), cwd)
			}
			records = append(records, wp370RuntimeRecord{Phase: "withSession", CWD: "/fixture"})
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wp370RuntimeCase{Result: result, Records: records}
}

func runWP370Dispose(t *testing.T) []wp370RuntimeRecord {
	t.Helper()
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := faux.New()
	records := make([]wp370RuntimeRecord, 0, 2)
	registry := wp370RuntimeRegistry(t, cwd, false, &records)
	host, err := codingagent.NewAgentSessionRuntime(context.Background(), codingagent.AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		StreamFn: provider.StreamSimple, Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	records = records[:0]
	host.SetBeforeSessionInvalidate(func() {
		records = append(records, wp370RuntimeRecord{Phase: "beforeSessionInvalidate"})
	})
	host.Dispose(context.Background())
	return records
}

func wp370RuntimeRegistry(
	t *testing.T,
	cwd string,
	cancel bool,
	records *[]wp370RuntimeRecord,
) *extensions.Registry {
	t.Helper()
	registry := extensions.NewRegistry(cwd)
	err := registry.Register("<wp370-runtime>", func(api extensions.API) error {
		api.On(extensions.EventSessionBeforeSwitch, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeSwitchEvent)
			*records = append(*records, wp370RuntimeRecord{Phase: "event", Event: wp370BeforeSwitchEvent(event)})
			return extensions.SessionBeforeSwitchResult{Cancel: cancel}, nil
		})
		api.On(extensions.EventSessionShutdown, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionShutdownEvent)
			*records = append(*records, wp370RuntimeRecord{Phase: "event", Event: wp370ShutdownEvent(event)})
			return nil, nil
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func wp370BeforeSwitchEvent(event extensions.SessionBeforeSwitchEvent) map[string]any {
	result := map[string]any{"type": "session_before_switch", "reason": event.Reason}
	if event.TargetSessionFile != nil {
		result["targetSessionFile"] = *event.TargetSessionFile
	}
	return result
}

func wp370ShutdownEvent(event extensions.SessionShutdownEvent) map[string]any {
	result := map[string]any{"type": "session_shutdown", "reason": event.Reason}
	if event.TargetSessionFile != nil {
		result["targetSessionFile"] = *event.TargetSessionFile
	}
	return result
}

func wp370StartEvent(event extensions.SessionStartEvent) map[string]any {
	result := map[string]any{"type": "session_start", "reason": event.Reason}
	if event.PreviousSessionFile != nil {
		result["previousSessionFile"] = *event.PreviousSessionFile
	}
	return result
}

func assertWP370JSONEqual(t *testing.T, want, got any) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err = runner.CanonicalJSON(wantJSON)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err = runner.CanonicalJSON(gotJSON)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(wantJSON, gotJSON); diff != "" {
		t.Fatal(diff)
	}
}
