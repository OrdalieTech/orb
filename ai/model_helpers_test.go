package ai

import (
	"regexp"
	"testing"
)

func thinkingMap(pairs map[ModelThinkingLevel]*string) *map[ModelThinkingLevel]*string { return &pairs }

func stringPtr(value string) *string { return &value }

func TestSupportedAndClampThinkingLevels(t *testing.T) {
	nonReasoning := &Model{}
	if levels := SupportedThinkingLevels(nonReasoning); len(levels) != 1 || levels[0] != ModelThinkingOff {
		t.Fatalf("non-reasoning levels = %v", levels)
	}
	reasoning := &Model{Reasoning: true}
	levels := SupportedThinkingLevels(reasoning)
	want := []ModelThinkingLevel{ModelThinkingOff, ModelThinkingMinimal, ModelThinkingLow, ModelThinkingMedium, ModelThinkingHigh}
	if len(levels) != len(want) {
		t.Fatalf("default reasoning levels = %v", levels)
	}
	mapped := &Model{Reasoning: true, ThinkingLevelMap: thinkingMap(map[ModelThinkingLevel]*string{
		ModelThinkingXHigh: stringPtr("xhigh"),
		ModelThinkingLow:   nil,
	})}
	mappedLevels := SupportedThinkingLevels(mapped)
	for _, level := range mappedLevels {
		if level == ModelThinkingLow || level == ModelThinkingMax {
			t.Fatalf("mapped levels wrongly include %s: %v", level, mappedLevels)
		}
	}
	if ClampThinkingLevel(mapped, ModelThinkingLow) != ModelThinkingMedium {
		t.Fatalf("clamp low = %v", ClampThinkingLevel(mapped, ModelThinkingLow))
	}
	if ClampThinkingLevel(mapped, ModelThinkingMax) != ModelThinkingXHigh {
		t.Fatalf("clamp max = %v", ClampThinkingLevel(mapped, ModelThinkingMax))
	}
	if ClampThinkingLevel(nonReasoning, ModelThinkingHigh) != ModelThinkingOff {
		t.Fatalf("clamp on non-reasoning = %v", ClampThinkingLevel(nonReasoning, ModelThinkingHigh))
	}
}

func TestCalculateCostTiersAndLongCacheWrites(t *testing.T) {
	tiers := []ModelCostTier{{InputTokensAbove: 1000, ModelCostRates: ModelCostRates{Input: 2, Output: 4, CacheRead: 1, CacheWrite: 2}}}
	model := &Model{Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 1}, Tiers: &tiers}}
	long := int64(100)
	usage := &Usage{Input: 900, Output: 10, CacheRead: 50, CacheWrite: 200, CacheWrite1h: &long}
	CalculateCost(model, usage)
	// 900+50+200 = 1150 > 1000: tier rates apply.
	if usage.Cost.Input != 2.0/1_000_000*900 {
		t.Fatalf("input cost = %v", usage.Cost.Input)
	}
	wantCacheWrite := (2.0*100 + 2.0*2*100) / 1_000_000
	if usage.Cost.CacheWrite != wantCacheWrite {
		t.Fatalf("cache write cost = %v, want %v", usage.Cost.CacheWrite, wantCacheWrite)
	}
	below := &Usage{Input: 10}
	CalculateCost(model, below)
	baseRate := model.Cost.Input
	if want := baseRate / 1_000_000 * float64(below.Input); below.Cost.Input != want {
		t.Fatalf("base tier input cost = %v, want %v", below.Cost.Input, want)
	}
}

func TestModelsAreEqualAndHasAPI(t *testing.T) {
	a := &Model{ID: "m", Provider: "p", API: APIOpenAIResponses}
	b := &Model{ID: "m", Provider: "p"}
	c := &Model{ID: "m", Provider: "other"}
	if !ModelsAreEqual(a, b) || ModelsAreEqual(a, c) || ModelsAreEqual(nil, a) || ModelsAreEqual(a, nil) {
		t.Fatal("ModelsAreEqual mismatch")
	}
	if !HasAPI(a, APIOpenAIResponses) || HasAPI(a, APIAnthropicMessages) || HasAPI(nil, APIOpenAIResponses) {
		t.Fatal("HasAPI mismatch")
	}
}

func TestContentTextAndUUIDv7PublicHelpers(t *testing.T) {
	content := AssistantContent{
		&ThinkingContent{Thinking: "reasoning"},
		&TextContent{Text: "first"},
		&ToolCall{ID: "1", Name: "read", Arguments: map[string]any{}},
		&TextContent{Text: "second"},
	}
	if got := ContentText(content); got != "first\nsecond" {
		t.Fatalf("assistant text = %q", got)
	}
	if got := ContentText(ToolResultContent{&TextContent{Text: "first"}, &ImageContent{}, &TextContent{Text: "second"}}, ""); got != "firstsecond" {
		t.Fatalf("tool result text = %q", got)
	}
	if got := ContentText("hello"); got != "hello" {
		t.Fatalf("string text = %q", got)
	}

	first, err := UUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	second, err := UUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) || first >= second {
		t.Fatalf("UUIDv7 values = %q, %q", first, second)
	}
}
