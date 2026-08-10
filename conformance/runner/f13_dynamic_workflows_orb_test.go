// F13 Orb replay: the Orb path runs the SAME plugin
// sources (@quintinshaw/pi-dynamic-workflows@3.5.1, hermetically installed by
// integrity-pinned lockfile) through the real extension host: loader.mjs
// aliases the @earendil-works/pi-* specifiers to the materialized
// orb-extension-sdk, model catalogs resolve over model_runtime_v1, and child
// sessions bridge over agent_session_v1 onto codingagent.NewAgentSession with
// the Go faux provider streaming the same scripted responses the extractor
// fed upstream. Behavior goldens (events, journals, tool calls/results,
// structured outputs, usage, persistence artifacts) must match after the same
// canonicalization the extractor applies. TUI frames are NOT compared against
// reference-tui/* — Orb owns its frame snapshots (D35).
package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	host "github.com/OrdalieTech/orb/codingagent/extensions/host"
)

// Capability identifiers the host_hello frame must advertise (design brief:
// "host_hello capability list grows: sdk_v1, agent_session_v1,
// model_runtime_v1"). The strings are the wire contract; HelloCapabilities is
// the public accessor over protocol.go's capability list.
func TestF13OrbHostAdvertisesSDKCapabilities(t *testing.T) {
	caps := host.HelloCapabilities()
	for _, want := range []string{"sdk_v1", "agent_session_v1", "model_runtime_v1"} {
		found := false
		for _, got := range caps {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("host_hello capabilities %v missing %q", caps, want)
		}
	}
}

// The embedded SDK's manifest (sdk/sdk.json, go:embed-ed beside host.mjs and
// materialized at host start) pins the semver and the per-module implemented
// symbol inventory. Every implemented symbol must exist in the upstream export
// surface the F13 extractor recorded — the SDK may implement or stub upstream
// names, never invent new ones. Full name-for-name surface parity (stubs
// included) is replayed through the real host by the export-surface scenario
// in TestF13OrbReplaysBehaviorGoldens.
func TestF13OrbSDKManifestMirrorsUpstreamExportSurface(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join(FixtureRoot(), "..", "..",
		"codingagent", "extensions", "host", "sdk", "sdk.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
		Modules map[string]struct {
			Implemented []string `json:"implemented"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	// The unsupported-stub diagnostic format embeds this version:
	// "OrbUnsupportedCapability: <pkg>#<export> is not implemented by
	// orb-extension-sdk <version>; ...". Semver starts 1.0.0.
	if !strings.HasPrefix(manifest.Version, "1.") {
		t.Errorf("sdk.json version = %q, want 1.x (semver starts 1.0.0)", manifest.Version)
	}
	var surface struct {
		Exports map[string][]string `json:"exports"`
	}
	loadF13JSON(t, "cases/export-surface.json", &surface)
	modulePackages := map[string]string{
		"coding-agent": "@earendil-works/pi-coding-agent",
		"ai":           "@earendil-works/pi-ai",
		"tui":          "@earendil-works/pi-tui",
	}
	for module, pkg := range modulePackages {
		upstream := map[string]bool{}
		for _, name := range surface.Exports[pkg] {
			upstream[name] = true
		}
		if len(upstream) == 0 {
			t.Fatalf("export surface for %s is empty", pkg)
		}
		for _, name := range manifest.Modules[module].Implemented {
			if !upstream[name] {
				t.Errorf("%s: sdk.json implements %q, which upstream never exported", pkg, name)
			}
		}
	}
}

// Full scenario replay through the real extension host, in extractor order
// (state under HOME accumulates across scenarios exactly as it did during
// extraction). The harness lives in f13_orb_harness_test.go.
func TestF13OrbReplaysBehaviorGoldens(t *testing.T) {
	var index struct {
		Scenarios []string `json:"scenarios"`
	}
	LoadJSON(t, f13Family, "cases.json", &index)
	harness := startF13Harness(t)
	for _, scenario := range index.Scenarios {
		t.Run(scenario, func(t *testing.T) {
			golden := readF13(t, "cases/"+scenario+".json")
			replayed := harness.replayScenario(t, scenario)
			if diff := f13DiffCanonical(golden, replayed); diff != "" {
				t.Errorf("scenario %s diverged from the upstream golden:\n%s", scenario, diff)
			}
		})
	}
}
