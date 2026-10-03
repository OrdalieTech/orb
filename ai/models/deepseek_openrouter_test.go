package models

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestBuiltinOpenRouterDeepSeekV41Flash(t *testing.T) {
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	model, ok := catalog.Find("openrouter", "deepseek/deepseek-v4.1-flash")
	if !ok {
		t.Fatal("missing OpenRouter DeepSeek V4.1 Flash")
	}
	if model.API != ai.APIOpenAICompletions || model.BaseURL != "https://openrouter.ai/api/v1" || !model.Reasoning || !slices.Equal(model.Input, ai.InputModalities{ai.InputText, ai.InputImage}) {
		t.Fatalf("model capabilities = %#v", model)
	}
	if model.ContextWindow != 1048576 || model.MaxTokens != 943718 {
		t.Fatalf("limits = %v/%v", model.ContextWindow, model.MaxTokens)
	}
	if model.Cost.Input != 0.3 || model.Cost.Output != 1.2 || model.Cost.CacheRead != 0.006 || model.Cost.CacheWrite != 0 {
		t.Fatalf("pricing = %#v", model.Cost)
	}
	if model.ThinkingLevelMap == nil || (*model.ThinkingLevelMap)[ai.ModelThinkingHigh] == nil || *(*model.ThinkingLevelMap)[ai.ModelThinkingHigh] != "high" || (*model.ThinkingLevelMap)[ai.ModelThinkingOff] == nil || *(*model.ThinkingLevelMap)[ai.ModelThinkingOff] != "none" {
		t.Fatalf("thinking levels = %#v", model.ThinkingLevelMap)
	}
}
