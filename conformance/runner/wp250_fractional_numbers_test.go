package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/api"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type wp250FractionalNumbersFixture struct {
	Config        json.RawMessage `json:"config"`
	Models        []ai.Model      `json:"models"`
	SimpleOptions struct {
		DefaultMaxTokens   float64 `json:"defaultMaxTokens"`
		RequestedMaxTokens float64 `json:"requestedMaxTokens"`
	} `json:"simpleOptions"`
	ModelJSON        []string `json:"modelJSON"`
	IntegerModelJSON string   `json:"integerModelJSON"`
	Error            *string  `json:"error"`
}

func TestWP250FractionalModelNumbersMatchUpstream(t *testing.T) {
	var fixture wp250FractionalNumbersFixture
	runner.LoadJSON(t, "WP250", "fractional-numbers.json", &fixture)

	directory := t.TempDir()
	modelsPath := filepath.Join(directory, "models.json")
	if err := os.WriteFile(modelsPath, fixture.Config, 0o600); err != nil {
		t.Fatal(err)
	}
	modelConfig, err := config.LoadModelConfig(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := modelConfig.Error(); (fixture.Error == nil && got != "") || (fixture.Error != nil && got != *fixture.Error) {
		t.Fatalf("models.json error = %q, want %v", got, fixture.Error)
	}
	models, err := config.ApplyModelConfig(nil, modelConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != len(fixture.Models) {
		t.Fatalf("model count = %d, want %d", len(models), len(fixture.Models))
	}
	for index := range models {
		got, err := json.Marshal(models[index])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != fixture.ModelJSON[index] {
			t.Fatalf("model %d JSON = %s, want %s", index, got, fixture.ModelJSON[index])
		}
	}

	var integerModel ai.Model
	if err := json.Unmarshal([]byte(fixture.IntegerModelJSON), &integerModel); err != nil {
		t.Fatal(err)
	}
	integerJSON, err := json.Marshal(integerModel)
	if err != nil {
		t.Fatal(err)
	}
	if string(integerJSON) != fixture.IntegerModelJSON {
		t.Fatalf("integer model JSON shape changed: %s", integerJSON)
	}

	for name, test := range map[string]struct {
		requested *float64
		want      float64
	}{
		"default":   {want: fixture.SimpleOptions.DefaultMaxTokens},
		"requested": {requested: float64Pointer(1.75), want: fixture.SimpleOptions.RequestedMaxTokens},
	} {
		t.Run(name, func(t *testing.T) {
			got := captureWP250MaxTokens(t, models[1], test.requested)
			if got != test.want {
				t.Fatalf("max tokens = %v, want %v", got, test.want)
			}
		})
	}
}

func captureWP250MaxTokens(t *testing.T, model ai.Model, requested *float64) float64 {
	t.Helper()
	var payload map[string]any
	key := "fixture"
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey:    &key,
		MaxTokens: requested,
		OnPayload: func(_ context.Context, value any, _ *ai.Model) (any, bool, error) {
			payload = value.(map[string]any)
			return nil, false, context.Canceled
		},
	}}
	stream, err := api.StreamSimpleOpenAICompletions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	value, ok := payload["max_completion_tokens"].(float64)
	if !ok {
		t.Fatalf("max_completion_tokens = %#v", payload["max_completion_tokens"])
	}
	return value
}

func float64Pointer(value float64) *float64 { return &value }
