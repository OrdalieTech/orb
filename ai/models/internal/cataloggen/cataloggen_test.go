package cataloggen

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
)

// pinnedGeneratedAt matches the -generated-at value in the ai/models doc.go
// generation directive.
var pinnedGeneratedAt = time.Date(2026, 11, 2, 11, 53, 23, 0, time.UTC)

func readCatalogTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func pinnedSources(t *testing.T) Sources {
	t.Helper()
	sources := Sources{GeneratedAt: pinnedGeneratedAt}
	for _, item := range []struct {
		target *[]byte
		path   string
	}{
		{&sources.ModelsDev, "../../testdata/api.json"},
		{&sources.NvidiaNIM, "../../testdata/nvidia-nim.json"},
		{&sources.OpenRouter, "../../testdata/openrouter.json"},
		{&sources.Vercel, "../../testdata/vercel.json"},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		*item.target = data
	}
	return sources
}

func TestRenderMatchesCheckedInCatalog(t *testing.T) {
	sources := pinnedSources(t)
	sources.OpenRouter = readCatalogTestFile(t, "../../testdata/openrouter-current.json")
	previous, err := os.ReadFile("../../generated.go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(sources)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previous) {
		t.Fatalf("generated.go is stale: rendered %d bytes, checked in %d bytes; run go generate ./ai/models", len(got), len(previous))
	}
}

func TestGenerateCommittedSnapshotIsDeterministic(t *testing.T) {
	sources := pinnedSources(t)
	first, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("fixed source inputs generated different catalogs")
	}
	if len(first) < 30 {
		t.Fatalf("generated only %d providers", len(first))
	}
	if _, exists := first["radius"]; exists {
		t.Fatal("Radius must not enter the orb catalog")
	}
	model, exists := first["openai"]["gpt-5.4"]
	if !exists {
		t.Fatal("missing openai/gpt-5.4")
	}
	if model.ContextWindow == 0 || model.MaxTokens == 0 || model.Cost.Input == 0 {
		t.Fatalf("incomplete generated model: %#v", model)
	}
}

func TestCompatModelsMatchPinnedF2Models(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}

	var fixture struct {
		Cases []struct {
			Name  string   `json:"name"`
			Model ai.Model `json:"model"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "compat-models.json", &fixture)
	for _, item := range fixture.Cases {
		key := string(item.Model.Provider) + "/" + item.Model.ID
		got, ok := catalog[string(item.Model.Provider)][item.Model.ID]
		if !ok {
			t.Fatalf("generated catalog is missing %s", key)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(item.Model)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s (%s) metadata mismatch\n got: %s\nwant: %s", key, item.Name, gotJSON, wantJSON)
		}
	}
}

// pi's catalog fixes: OpenCode serves each model on the endpoint models.dev lists for its protocol
// (pi 1.5.0), so OpenCode Go's MiniMax M3 speaks Anthropic Messages under /inference/go, and Xiaomi
// MiMo thinks on or off only (pi 1.3.0).
func TestCatalogTakesPiProviderFixes(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if model := catalog["opencode"]["claude-sonnet-4-6"]; model.BaseURL != "https://opencode.ai/inference/anthropic" {
		t.Fatalf("opencode base URL = %q", model.BaseURL)
	}
	if model := catalog["opencode-go"]["minimax-m3"]; model.API != ai.APIAnthropicMessages || model.BaseURL != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("OpenCode Go MiniMax M3 = %s %s", model.API, model.BaseURL)
	}
	// pi 1.4.0: Codex no longer offers the models that always fail for ChatGPT accounts.
	if _, ok := catalog["openai-codex"]["gpt-5.5"]; ok {
		t.Fatal("openai-codex still offers gpt-5.5")
	}
	// pi 1.5.0: Codex lists GPT-6.1 Terra at its 500K default, and unlike GPT-6.1 Sol it accepts effort "none".
	terra := catalog["openai-codex"]["gpt-6.1-terra"]
	if off := (*terra.ThinkingLevelMap)[ai.ModelThinkingOff]; terra.ContextWindow != 500000 || off == nil || *off != "none" {
		t.Fatalf("Codex GPT-6.1 Terra = %v context, off %v", terra.ContextWindow, off)
	}
	// pi 1.5.0: GPT-5.5 namespaces dynamically loaded tools, so tool search starts with GPT-5.6 (#11415).
	if compat := string(catalog["openai"]["gpt-5.5"].Compat); strings.Contains(compat, "supportsToolSearch") {
		t.Fatalf("openai gpt-5.5 compat = %s", compat)
	}
	levels := *catalog["xiaomi"]["mimo-v2.6-pro"].ThinkingLevelMap
	if off, high, medium := levels[ai.ModelThinkingOff], levels[ai.ModelThinkingHigh], levels[ai.ModelThinkingMedium]; off == nil || *off != "off" || high == nil || *high != "high" || medium != nil {
		t.Fatalf("MiMo thinking levels = %v", levels)
	}
}

// pi 1.5.1 catalog changes the F2 compat fixture does not cover.
func TestCatalogTakesPi151Changes(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ provider, id, compat string }{
		{"anthropic", "claude-fable-5-2", `"supportsMidConvoEffort":true`},
		{"anthropic", "claude-fable-5-2", `"supportsMidConvoSystemMessages":true`},
		{"anthropic", "claude-fable-5-2", `"allowedFallbackModels":[{"provider":"anthropic","model":"claude-opus-5"`},
		{"anthropic", "claude-sonnet-5", `"supportsTemperature":false`},
		{"opencode-go", "claude-haiku-5-5", `"supportsMidConvoEffort":true`},
		{"opencode", "claude-opus-5", `"supportsMidConvoEffort":true`},
		{"opencode", "claude-fable-5-1", `"supportsMidConvoEffort":true`},
		{"qwen-token-plan-individual", "glm-5.3", `"thinkingFormat":"qwen"`},
		{"qwen-token-plan-individual-cn", "glm-5.3", `"thinkingFormat":"qwen"`},
	} {
		m, ok := catalog[item.provider][item.id]
		if !ok || !strings.Contains(string(m.Compat), item.compat) {
			t.Errorf("%s/%s compat = %s, want %s", item.provider, item.id, m.Compat, item.compat)
		}
	}
	if m := catalog["qwen-token-plan-individual-cn"]["glm-5.3"]; m.BaseURL != "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" || len(catalog["qwen-token-plan-individual-cn"]) != 10 {
		t.Errorf("qwen-token-plan-individual-cn = %d models, base URL %q", len(catalog["qwen-token-plan-individual-cn"]), m.BaseURL)
	}
	for _, id := range []string{"gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"} {
		if m := catalog["azure"][id]; m.ContextWindow != 1050000 || m.Cost.Tiers == nil || len(*m.Cost.Tiers) == 0 {
			t.Errorf("azure/%s = %v context, tiers %v", id, m.ContextWindow, m.Cost.Tiers)
		}
	}
}
