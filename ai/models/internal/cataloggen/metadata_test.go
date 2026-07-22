package cataloggen

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestGenerateAppliesPinnedCatalogQuirksWithoutLosingFloatMetadata(t *testing.T) {
	data := []byte(`{
		"anthropic":{"models":{
			"fractional":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"limit":{"context":100.5,"output":20.25},"cost":{"input":1,"tiers":[{"input":2,"tier":{"type":"context","size":50.5}}]}}
		}},
		"deepseek":{"models":{
			"deepseek-v3":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]}},
			"deepseek-v4-flash":{"tool_call":true,"reasoning":true,"modalities":{"input":["text"],"output":["text"]}}
		}},
		"fireworks-ai":{"models":{
			"accounts/fireworks/models/glm-5p2":{"tool_call":true,"reasoning":true,"modalities":{"input":["text"],"output":["text"]}}
		}},
		"google-vertex":{"models":{
			"gemini-2.5-flash":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"cost":{"cache_read":9,"cache_write":7}}
		}},
		"mistral":{"models":{
			"mistral-test":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"cost":{"input":0.1234567}}
		}},
		"nvidia":{"models":{
			"z-ai/glm-5.2":{"tool_call":true,"reasoning":true,"modalities":{"input":["text"],"output":["text"]}},
			"no-text-output":{"tool_call":true,"modalities":{"input":["text"],"output":["image"]}},
			"upstage/solar-10_7b-instruct":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]}}
		}},
		"github-copilot":{"models":{
			"claude-sonnet-4.6":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"cost":{"input":3,"tiers":[{"input":6,"output":9,"tier":{"type":"context","size":200000.5}}]}}
		}},
		"opencode":{"models":{
			"qwen-alibaba":{"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"provider":{"npm":"@ai-sdk/alibaba"}}
		}}
	}`)
	nim := []byte(`{"data":[{"id":"z-ai/glm-5.2"},{"id":"upstage/solar-10.7b-instruct"}]}`)
	sources := Sources{ModelsDev: data, NvidiaNIM: nim}
	catalog, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog["deepseek"]) != 1 || catalog["deepseek"]["deepseek-v4-flash"].ID == "" {
		t.Fatalf("DeepSeek filtering = %#v", catalog["deepseek"])
	}
	fireworks := catalog["fireworks"]["accounts/fireworks/models/glm-5p2"]
	if fireworks.API != ai.APIOpenAICompletions || fireworks.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Fatalf("Fireworks GLM route = %#v", fireworks)
	}
	if got := catalog["mistral"]["mistral-test"].Cost.CacheRead; got != 0.012346 {
		t.Fatalf("Mistral inferred cache-read cost = %v", got)
	}
	vertex := catalog["google-vertex"]["gemini-2.5-flash"]
	if vertex.Cost.CacheRead != 0.03 || vertex.Cost.CacheWrite != 0 {
		t.Fatalf("Vertex Gemini 2.5 Flash cache costs = %#v", vertex.Cost)
	}
	if len(catalog["nvidia"]) != 1 || catalog["nvidia"]["z-ai/glm-5.2"].ID == "" {
		t.Fatalf("NVIDIA filtering = %#v", catalog["nvidia"])
	}

	fractional := catalog["anthropic"]["fractional"]
	if fractional.ContextWindow != 100.5 || fractional.MaxTokens != 20.25 || fractional.Cost.Tiers != nil {
		t.Fatalf("generic fractional model = %#v", fractional)
	}
	copilot := catalog["github-copilot"]["claude-sonnet-4.6"]
	if copilot.API != ai.APIAnthropicMessages || copilot.ContextWindow != 1000000 || copilot.Cost.Tiers == nil || len(*copilot.Cost.Tiers) != 1 || (*copilot.Cost.Tiers)[0].InputTokensAbove != 200000.5 {
		t.Fatalf("Copilot tier metadata = %#v", copilot)
	}
	if _, exists := catalog["openrouter"]["auto"]; !exists {
		t.Fatal("missing OpenRouter auto alias")
	}
	if _, exists := catalog["openrouter"]["openrouter/fusion"]; !exists {
		t.Fatal("missing OpenRouter fusion alias")
	}
	if _, exists := catalog["mistral"]["mistral-medium-3.5"]; !exists {
		t.Fatal("missing Mistral Medium 3.5 alias")
	}
	if _, exists := catalog["openai"]["gpt-5-chat-latest"]; !exists {
		t.Fatal("missing GPT-5 Chat Latest alias")
	}

	var opencodeCompat ai.OpenAICompletionsCompat
	if err := json.Unmarshal(catalog["opencode"]["qwen-alibaba"].Compat, &opencodeCompat); err != nil {
		t.Fatal(err)
	}
	if opencodeCompat.CacheControlFormat == nil || *opencodeCompat.CacheControlFormat != ai.CacheControlAnthropic {
		t.Fatalf("Alibaba cache-control compat = %s", catalog["opencode"]["qwen-alibaba"].Compat)
	}

	sources.GeneratedAt = pinnedGeneratedAt
	rendered, err := Render(sources)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte("100.5")) || !bytes.Contains(rendered, []byte("200000.5")) {
		t.Fatalf("Render lost fractional metadata: %s", rendered)
	}
}

func TestApplyCatalogMetadataMatchesRepresentativePinnedCompat(t *testing.T) {
	model := ai.Model{
		ID: "deepseek-ai/DeepSeek-V4-Pro", Name: "DeepSeek V4 Pro", API: ai.APIOpenAICompletions,
		Provider: "together", BaseURL: "https://api.together.ai/v1", Reasoning: true,
	}
	applyCatalogMetadata(&model)
	var compat ai.OpenAICompletionsCompat
	if err := json.Unmarshal(model.Compat, &compat); err != nil {
		t.Fatal(err)
	}
	if compat.SupportsStore == nil || *compat.SupportsStore || compat.SupportsReasoningEffort == nil || !*compat.SupportsReasoningEffort || compat.ThinkingFormat == nil || *compat.ThinkingFormat != ai.ThinkingFormatTogether {
		t.Fatalf("Together compat = %s", model.Compat)
	}
	levels := *model.ThinkingLevelMap
	if levels[ai.ModelThinkingHigh] == nil || *levels[ai.ModelThinkingHigh] != "high" {
		t.Fatalf("Together thinking map = %#v", levels)
	}
	for _, level := range []ai.ModelThinkingLevel{ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium, ai.ModelThinkingXHigh} {
		value, exists := levels[level]
		if !exists || value != nil {
			t.Fatalf("thinking level %q = %#v, want explicit null", level, value)
		}
	}

	workers := ai.Model{
		ID: "workers-model", API: ai.APIOpenAICompletions, Provider: "cloudflare-workers-ai",
		BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
	}
	applyCatalogMetadata(&workers)
	if err := json.Unmarshal(workers.Compat, &compat); err != nil {
		t.Fatal(err)
	}
	if compat.SendSessionAffinityHeaders == nil || !*compat.SendSessionAffinityHeaders || compat.SupportsLongCacheRetention == nil || *compat.SupportsLongCacheRetention {
		t.Fatalf("Cloudflare Workers compat = %s", workers.Compat)
	}
}

// SYNC-1: upstream v0.81.1 gives moonshot kimi-k3 the OpenAI thinking format and
// reasoning-effort support (generate-models.ts:1761-1766).
func TestSYNC1MoonshotKimiK3Compat(t *testing.T) {
	for _, provider := range []string{"moonshotai", "moonshotai-cn"} {
		model := ai.Model{ID: "kimi-k3", API: ai.APIOpenAICompletions, Provider: ai.ProviderID(provider), Reasoning: true}
		applyCatalogMetadata(&model)
		var compat ai.OpenAICompletionsCompat
		if err := json.Unmarshal(model.Compat, &compat); err != nil {
			t.Fatal(err)
		}
		if compat.ThinkingFormat == nil || *compat.ThinkingFormat != ai.ThinkingFormatOpenAI {
			t.Fatalf("%s kimi-k3 thinkingFormat = %s", provider, model.Compat)
		}
		if compat.SupportsReasoningEffort == nil || !*compat.SupportsReasoningEffort {
			t.Fatalf("%s kimi-k3 supportsReasoningEffort = %s", provider, model.Compat)
		}
		if compat.RequiresReasoningContentOnAssistantMessages == nil || !*compat.RequiresReasoningContentOnAssistantMessages ||
			compat.DeferredToolsMode == nil || *compat.DeferredToolsMode != ai.DeferredToolsKimi {
			t.Fatalf("%s kimi-k3 lost pinned compat = %s", provider, model.Compat)
		}
		other := ai.Model{ID: "kimi-k2.7-code", API: ai.APIOpenAICompletions, Provider: ai.ProviderID(provider)}
		applyCatalogMetadata(&other)
		var otherCompat ai.OpenAICompletionsCompat
		if err := json.Unmarshal(other.Compat, &otherCompat); err != nil {
			t.Fatal(err)
		}
		if otherCompat.ThinkingFormat == nil || *otherCompat.ThinkingFormat != ai.ThinkingFormatDeepSeek {
			t.Fatalf("%s kimi-k2.7-code thinkingFormat = %s", provider, other.Compat)
		}
	}
}

func TestFreshUpstreamThinkingMetadata(t *testing.T) {
	kimi := ai.Model{ID: "k3", API: ai.APIAnthropicMessages, Provider: "kimi-coding", Reasoning: true}
	applyCatalogMetadata(&kimi)
	levels := *kimi.ThinkingLevelMap
	for level, want := range map[ai.ModelThinkingLevel]string{ai.ModelThinkingLow: "low", ai.ModelThinkingHigh: "high", ai.ModelThinkingMax: "max"} {
		if levels[level] == nil || *levels[level] != want {
			t.Fatalf("Kimi K3 %s = %#v", level, levels[level])
		}
	}

	qwen := ai.Model{ID: "qwen3.7-max", API: ai.APIOpenAICompletions, Provider: "qwen-token-plan", BaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", Reasoning: true}
	applyCatalogMetadata(&qwen)
	var compat ai.OpenAICompletionsCompat
	if err := json.Unmarshal(qwen.Compat, &compat); err != nil {
		t.Fatal(err)
	}
	if compat.SupportsStore == nil || *compat.SupportsStore || compat.SupportsDeveloperRole == nil || *compat.SupportsDeveloperRole || compat.ThinkingFormat == nil || *compat.ThinkingFormat != ai.ThinkingFormatQwen {
		t.Fatalf("Qwen Token Plan compat = %s", qwen.Compat)
	}
}
