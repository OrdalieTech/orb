package runner

import (
	"fmt"
	"strings"
	"testing"
)

// F13-dynamic-workflows: hermetic reference behavior of
// @quintinshaw/pi-dynamic-workflows@3.5.1 on the pinned upstream pi. This test
// validates the committed golden surface itself (shape, pins, and the D35
// reference-only marking). Driving the same scenarios through Orb's
// orb-extension-sdk + agent_session_v1/model_runtime_v1 bridges lives in
// f13_dynamic_workflows_orb_test.go, which skips when Node, npm, or the
// pinned .upstream tree are unavailable (the host e2e availability pattern).

const f13Family = "F13-dynamic-workflows"

// The 23 supported symbols per the design brief; the Orb SDK implements these
// and stubs every other upstream export with OrbUnsupportedCapability.
var f13SupportedExports = map[string][]string{
	"@earendil-works/pi-coding-agent": {
		"createAgentSession", "SessionManager", "SettingsManager", "DefaultResourceLoader",
		"ModelRuntime", "ModelRegistry", "createCodingTools", "defineTool", "getAgentDir",
		"getLanguageFromPath", "getMarkdownTheme", "parseFrontmatter", "renderDiff",
	},
	"@earendil-works/pi-ai": {"modelsAreEqual"},
	"@earendil-works/pi-tui": {
		"Container", "Markdown", "SelectList", "Spacer", "Text",
		"parseKey", "truncateToWidth", "visibleWidth", "wrapTextWithAnsi",
	},
}

type f13Manifest struct {
	Manifest
	Plugin struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Integrity string `json:"integrity"`
	} `json:"plugin"`
	ReferenceOnly []string `json:"referenceOnly"`
}

func loadF13Manifest(t *testing.T) f13Manifest {
	t.Helper()
	var manifest f13Manifest
	LoadJSON(t, f13Family, "manifest.json", &manifest)
	return manifest
}

func TestF13ManifestPinsPluginAndUpstream(t *testing.T) {
	manifest := loadF13Manifest(t)
	if manifest.Family != f13Family {
		t.Fatalf("family = %q, want %q", manifest.Family, f13Family)
	}
	if manifest.Plugin.Name != "@quintinshaw/pi-dynamic-workflows" {
		t.Fatalf("plugin name = %q", manifest.Plugin.Name)
	}
	if manifest.Plugin.Version != "3.5.1" {
		t.Fatalf("plugin version = %q, want 3.5.1", manifest.Plugin.Version)
	}
	if !strings.HasPrefix(manifest.Plugin.Integrity, "sha512-") {
		t.Fatalf("plugin integrity %q is not sha512-pinned", manifest.Plugin.Integrity)
	}
	if manifest.UpstreamCommit == "" {
		t.Fatal("manifest has no upstream commit")
	}
}

func TestF13ScenarioCasesAreWellFormed(t *testing.T) {
	manifest := loadF13Manifest(t)
	var index struct {
		SchemaVersion int      `json:"schemaVersion"`
		Scenarios     []string `json:"scenarios"`
	}
	LoadJSON(t, f13Family, "cases.json", &index)
	if index.SchemaVersion != 1 {
		t.Fatalf("cases.json schemaVersion = %d, want 1", index.SchemaVersion)
	}
	wantScenarios := []string{
		"foreground-basic", "structured-output", "store-tools", "web-toolset", "agent-types",
		"nested-workflow", "cancellation", "model-routing", "background-lifecycle",
		"persist-agent-sessions", "extension-lifecycle", "workflows-models", "export-surface",
	}
	got := map[string]bool{}
	for _, name := range index.Scenarios {
		got[name] = true
	}
	for _, name := range wantScenarios {
		if !got[name] {
			t.Errorf("cases.json is missing scenario %q", name)
		}
	}
	listed := map[string]bool{}
	for _, file := range manifest.Files {
		listed[file] = true
	}
	for _, name := range index.Scenarios {
		file := fmt.Sprintf("cases/%s.json", name)
		if !listed[file] {
			t.Errorf("manifest.files is missing %s", file)
		}
		var scenario struct {
			SchemaVersion int    `json:"schemaVersion"`
			Scenario      string `json:"scenario"`
		}
		LoadJSON(t, f13Family, file, &scenario)
		if scenario.SchemaVersion != 1 || scenario.Scenario != name {
			t.Errorf("%s: schemaVersion=%d scenario=%q", file, scenario.SchemaVersion, scenario.Scenario)
		}
	}
}

// D35: rendered TUI frames from upstream are reference observations only. The
// manifest must declare every reference-tui file, and each file must carry the
// referenceOnly marker so no future consumer wires it up as a parity gate.
func TestF13ReferenceTUIFilesAreMarkedReferenceOnly(t *testing.T) {
	manifest := loadF13Manifest(t)
	if len(manifest.ReferenceOnly) == 0 {
		t.Fatal("manifest declares no reference-only files")
	}
	for _, file := range manifest.ReferenceOnly {
		if !strings.HasPrefix(file, "reference-tui/") {
			t.Errorf("reference-only file %q is outside reference-tui/", file)
		}
		var payload struct {
			ReferenceOnly bool   `json:"referenceOnly"`
			Note          string `json:"note"`
		}
		LoadJSON(t, f13Family, file, &payload)
		if !payload.ReferenceOnly {
			t.Errorf("%s: referenceOnly marker missing", file)
		}
		if !strings.Contains(payload.Note, "Orb frame goldens are Orb-owned") {
			t.Errorf("%s: note does not state Orb ownership of frame goldens", file)
		}
	}
}

// The export-surface golden is the authoritative inventory the orb-extension-sdk
// must mirror: every name exists, the supported 23 are real implementations,
// the rest throw OrbUnsupportedCapability (asserted by SDK-lane Go tests, not
// by a golden). Here: the supported set must be present in upstream's surface,
// or the SDK would be exporting names upstream never had.
func TestF13ExportSurfaceCoversSupportedSymbols(t *testing.T) {
	var surface struct {
		Exports           map[string][]string `json:"exports"`
		UnsupportedProbes []struct {
			Export  string `json:"export"`
			Present bool   `json:"present"`
			Typeof  string `json:"typeof"`
		} `json:"unsupportedProbes"`
	}
	LoadJSON(t, f13Family, "cases/export-surface.json", &surface)
	for pkg, symbols := range f13SupportedExports {
		names := map[string]bool{}
		for _, name := range surface.Exports[pkg] {
			names[name] = true
		}
		if len(names) == 0 {
			t.Fatalf("export surface for %s is empty", pkg)
		}
		for _, symbol := range symbols {
			if !names[symbol] {
				t.Errorf("%s: supported symbol %q missing from upstream export surface", pkg, symbol)
			}
		}
	}
	for _, probe := range surface.UnsupportedProbes {
		if !probe.Present || probe.Typeof == "undefined" {
			t.Errorf("unsupported-export probe %q is not a real upstream export (present=%v typeof=%s)",
				probe.Export, probe.Present, probe.Typeof)
		}
	}
}

// Golden self-consistency checks on load-bearing behavior contracts, so a
// regenerated fixture that silently lost a scenario leg fails here rather than
// in the (later) Orb replay.
func TestF13BehaviorGoldensCarryLoadBearingContracts(t *testing.T) {
	var structured struct {
		Result struct {
			Result map[string]map[string]any `json:"result"`
		} `json:"result"`
		PendingFauxResponses int `json:"pendingFauxResponses"`
	}
	LoadJSON(t, f13Family, "cases/structured-output.json", &structured)
	if structured.PendingFauxResponses != 0 {
		t.Errorf("structured-output left %d faux responses pending", structured.PendingFauxResponses)
	}
	for leg, key := range map[string]string{"direct": "fruit", "repaired": "veg", "prose": "mineral"} {
		if structured.Result.Result[leg][key] == nil {
			t.Errorf("structured-output %s leg lost its %q capture", leg, key)
		}
	}

	var background struct {
		PausedRuns []struct {
			Status      string `json:"status"`
			PauseReason string `json:"pauseReason"`
			ResetHint   string `json:"resetHint"`
		} `json:"pausedRuns"`
		Resumed bool `json:"resumed"`
	}
	LoadJSON(t, f13Family, "cases/background-lifecycle.json", &background)
	if len(background.PausedRuns) != 1 || background.PausedRuns[0].Status != "paused" ||
		background.PausedRuns[0].PauseReason != "usage_limit" || background.PausedRuns[0].ResetHint == "" {
		t.Errorf("background-lifecycle lost the provider-limit pause contract: %+v", background.PausedRuns)
	}
	if !background.Resumed {
		t.Error("background-lifecycle lost the resume leg")
	}

	var routing struct {
		Result struct {
			Result struct {
				ExplicitError struct {
					Code        string `json:"code"`
					Recoverable bool   `json:"recoverable"`
				} `json:"explicitError"`
				Synthesized string `json:"synthesized"`
			} `json:"result"`
		} `json:"result"`
	}
	LoadJSON(t, f13Family, "cases/model-routing.json", &routing)
	if routing.Result.Result.ExplicitError.Code != "MODEL_NOT_FOUND" || routing.Result.Result.ExplicitError.Recoverable {
		t.Errorf("model-routing lost the explicit MODEL_NOT_FOUND contract: %+v", routing.Result.Result.ExplicitError)
	}
	if routing.Result.Result.Synthesized == "" {
		t.Error("model-routing lost the synthesized off-catalog model leg")
	}

	var cancel struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	LoadJSON(t, f13Family, "cases/cancellation.json", &cancel)
	if cancel.Error.Message != "Subagent was aborted" {
		t.Errorf("cancellation error message = %q, want the upstream abort text", cancel.Error.Message)
	}
}
