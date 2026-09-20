package models

import (
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestBuiltinCatalogAndCorrections(t *testing.T) {
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	models := catalog.Models()
	providers := make(map[ai.ProviderID]struct{})
	for _, model := range models {
		providers[model.Provider] = struct{}{}
	}
	deepseek, ok := catalog.Find("deepseek", "deepseek-flash")
	if !ok || deepseek.Cost.Input != 0.3 || deepseek.Cost.Output != 1.2 || deepseek.Cost.CacheRead != 0.006 {
		t.Fatalf("released DeepSeek Flash correction missing: %#v", deepseek)
	}
	if _, ok := catalog.Find("deepseek", "deepseek-v4-flash"); ok {
		t.Fatal("retired deepseek-v4-flash remains in catalog")
	}
	deepseekPro, ok := catalog.Find("deepseek", "deepseek-v4-pro")
	if !ok || deepseekPro.Cost.Input != 1.32 || deepseekPro.Cost.Output != 3.96 || deepseekPro.Cost.CacheRead != 0.044 {
		t.Fatalf("released DeepSeek V4 Pro pricing missing: %#v", deepseekPro)
	}
	anthropic, ok := catalog.Find("anthropic", "claude-opus-4-6")
	if !ok || anthropic.PromptCache == nil || anthropic.PromptCache.Short == nil || *anthropic.PromptCache.Short != 300 || anthropic.PromptCache.Long == nil || *anthropic.PromptCache.Long != 3600 {
		t.Fatalf("Anthropic prompt cache metadata missing: %#v", anthropic.PromptCache)
	}
	for _, provider := range []string{"openai", "openai-codex"} {
		if _, ok := catalog.Find(provider, "gpt-6-astra"); !ok {
			t.Fatalf("missing released %s/gpt-6-astra", provider)
		}
	}
	for _, id := range []string{"gpt-5.4", "gpt-5.4-mini"} {
		if _, ok := catalog.Find("openai-codex", id); ok {
			t.Fatalf("retired openai-codex/%s remains in catalog", id)
		}
	}
	baseten, ok := catalog.Find("baseten", "openai/gpt-oss-120b")
	if !ok {
		t.Fatal("missing Baseten fixture model")
	}
	var completionsCompat ai.OpenAICompletionsCompat
	if err := json.Unmarshal(baseten.Compat, &completionsCompat); err != nil {
		t.Fatal(err)
	}
	if completionsCompat.SendSessionAffinityHeaders == nil || !*completionsCompat.SendSessionAffinityHeaders {
		t.Fatalf("Baseten session affinity metadata missing: %s", baseten.Compat)
	}
	vercel, ok := catalog.Find("vercel-ai-gateway", "anthropic/claude-opus-4.6")
	if !ok {
		t.Fatal("missing Vercel AI Gateway fixture model")
	}
	var anthropicCompat ai.AnthropicMessagesCompat
	if err := json.Unmarshal(vercel.Compat, &anthropicCompat); err != nil {
		t.Fatal(err)
	}
	if anthropicCompat.AllowEmptySignature == nil || !*anthropicCompat.AllowEmptySignature {
		t.Fatalf("Vercel empty-signature metadata missing: %s", vercel.Compat)
	}
	if len(models) != 1135 || len(providers) != 39 {
		t.Fatalf("snapshot catalog has %d providers/%d models, want 39/1135", len(providers), len(models))
	}
	for _, model := range models {
		if model.Provider == "radius" {
			t.Fatal("Radius must not enter the builtin catalog")
		}
	}
	openai, ok := catalog.Find("openai", "gpt-5.4")
	if !ok {
		t.Fatal("missing openai/gpt-5.4")
	}
	if openai.ContextWindow != 272000 || openai.MaxTokens != 128000 {
		t.Fatalf("hand correction was not applied: %#v", openai)
	}
	for provider, id := range map[string]string{
		"mistral":    "mistral-medium-3.5",
		"openrouter": "auto",
	} {
		if _, ok := catalog.Find(provider, id); !ok {
			t.Fatalf("missing pinned upstream alias %s/%s", provider, id)
		}
	}
	if _, ok := catalog.Find("openrouter", "openrouter/fusion"); !ok {
		t.Fatal("missing pinned upstream alias openrouter/openrouter/fusion")
	}
}

func TestOpenRouterAnthropicLatestAliasesEnableCacheControl(t *testing.T) {
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"~anthropic/claude-fable-latest",
		"~anthropic/claude-haiku-latest",
		"~anthropic/claude-opus-latest",
		"~anthropic/claude-sonnet-latest",
	} {
		model, ok := catalog.Find("openrouter", id)
		if !ok {
			t.Fatalf("missing openrouter/%s", id)
		}
		var compat ai.OpenAICompletionsCompat
		if err := json.Unmarshal(model.Compat, &compat); err != nil {
			t.Fatal(err)
		}
		if compat.CacheControlFormat == nil || *compat.CacheControlFormat != ai.CacheControlAnthropic {
			t.Fatalf("openrouter/%s cacheControlFormat = %s", id, model.Compat)
		}
	}
}

func TestCatalogReturnsDetachedValuesAndMerges(t *testing.T) {
	base, err := Decode([]byte(`{"p":{"m":{"id":"m","name":"M","api":"openai-completions","provider":"p","baseUrl":"https://base","reasoning":false,"input":["text"],"cost":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0},"contextWindow":10,"maxTokens":5,"headers":{"x":"base"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	model, _ := base.Find("p", "m")
	model.Input[0] = ai.InputImage
	(*model.Headers)["x"] = "mutated"
	again, _ := base.Find("p", "m")
	if again.Input[0] != ai.InputText || (*again.Headers)["x"] != "base" {
		t.Fatal("Find leaked catalog-owned slices or maps")
	}

	overlay, err := Decode([]byte(`{"p":{"m":{"id":"m","name":"Overlay","api":"openai-completions","provider":"p","baseUrl":"https://overlay","reasoning":true,"input":["text"],"cost":{"input":3,"output":4,"cacheRead":0,"cacheWrite":0},"contextWindow":20,"maxTokens":6}}}`))
	if err != nil {
		t.Fatal(err)
	}
	merged := base.Merge(overlay)
	mergedModel, _ := merged.Find("p", "m")
	if mergedModel.Name != "Overlay" || !mergedModel.Reasoning {
		t.Fatalf("overlay did not replace provider/id entry: %#v", mergedModel)
	}
	data, err := json.Marshal(merged)
	if err != nil || len(data) == 0 {
		t.Fatalf("marshal merged catalog: %v", err)
	}
}
