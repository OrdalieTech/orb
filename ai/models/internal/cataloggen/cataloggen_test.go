package cataloggen

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

func TestRenderMatchesCheckedInCatalog(t *testing.T) {
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../generated.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated.go is stale: rendered %d bytes, checked in %d bytes; run go generate ./ai/models", len(got), len(want))
	}
}

func TestGenerateCommittedSnapshotIsDeterministic(t *testing.T) {
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(data)
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
		t.Fatal("fixed models.dev input generated different catalogs")
	}
	if len(first) < 30 {
		t.Fatalf("generated only %d providers", len(first))
	}
	if _, exists := first["radius"]; exists {
		t.Fatal("Radius must not enter the pigo catalog")
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
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Generate(data)
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

func TestGenerateFiltersUnsupportedSourceModels(t *testing.T) {
	data := []byte(`{
		"anthropic":{"models":{
			"kept":{"name":"Kept","tool_call":true,"reasoning":true,"modalities":{"input":["text","image"]},"limit":{"context":100,"output":20},"cost":{"input":1,"output":2,"cache_read":0.1,"cache_write":1}},
			"no-tools":{"tool_call":false},
			"old":{"tool_call":true,"status":"deprecated"}
		}}
	}`)
	catalog, err := Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	models := catalog["anthropic"]
	if len(models) != 2 {
		t.Fatalf("got %d anthropic models, want 2", len(models))
	}
	model := models["kept"]
	if !model.Reasoning || len(model.Input) != 2 || model.Cost.CacheRead != 0.1 {
		t.Fatalf("bad normalized model: %#v", model)
	}
	if _, exists := models["old"]; !exists {
		t.Fatal("pinned generator keeps deprecated Anthropic entries")
	}
}

func TestGenerateFreshUpstreamCatalogChanges(t *testing.T) {
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"qwen-token-plan", "qwen-token-plan-cn"} {
		if len(catalog[provider]) != 14 {
			t.Fatalf("%s models = %d, want 14", provider, len(catalog[provider]))
		}
	}
	if _, ok := catalog["kimi-coding"]["k2p7"]; ok {
		t.Fatal("legacy Kimi k2p7 alias remains in catalog")
	}
	if _, ok := catalog["kimi-coding"]["kimi-for-coding"]; !ok {
		t.Fatal("canonical Kimi coding model is missing")
	}
	for _, provider := range []string{"opencode", "opencode-go"} {
		if got := catalog[provider]["grok-4.5"].API; got != ai.APIOpenAIResponses {
			t.Fatalf("%s grok API = %q", provider, got)
		}
	}
	for provider, ids := range map[string][]string{
		"nvidia":            {"qwen/qwen3.5-122b-a10b"},
		"openrouter":        {"meta-llama/llama-3.3-70b-instruct:free", "qwen/qwen3-coder:free", "qwen/qwen3-next-80b-a3b-instruct:free"},
		"together":          {"Qwen/Qwen3-235B-A22B-Instruct-2507-tput", "Qwen/Qwen3.5-397B-A17B", "essentialai/Rnj-1-Instruct", "zai-org/GLM-5", "zai-org/GLM-5.1"},
		"vercel-ai-gateway": {"meta/llama-3.2-11b", "meta/llama-3.2-90b"},
	} {
		for _, id := range ids {
			if _, ok := catalog[provider][id]; ok {
				t.Fatalf("removed model remains in catalog: %s/%s", provider, id)
			}
		}
	}
	if _, ok := catalog["together"]["thinkingmachines/Inkling"]; !ok {
		t.Fatal("Together Inkling model is missing")
	}
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		if got := catalog["openai-codex"][model].ContextWindow; got != 272000 {
			t.Fatalf("OpenAI Codex %s context = %v", model, got)
		}
	}
}
