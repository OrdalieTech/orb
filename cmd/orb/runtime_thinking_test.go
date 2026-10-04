package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestCLIInitialThinkingClampsToModelCapabilities(t *testing.T) {
	for _, test := range []struct {
		name      string
		reasoning bool
		requested string
		want      ai.ModelThinkingLevel
	}{
		{"nonreasoning-default", false, "", ai.ModelThinkingOff},
		{"nonreasoning-explicit", false, "high", ai.ModelThinkingOff},
		{"reasoning-supported", true, "medium", ai.ModelThinkingMedium},
		{"reasoning-unsupported", true, "xhigh", ai.ModelThinkingHigh},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv(config.EnvAgentDir, root)
			t.Setenv("PI_OFFLINE", "1")
			data, err := json.Marshal(map[string]any{"providers": map[string]any{"local": map[string]any{
				"baseUrl": "http://localhost/v1", "api": "openai-completions", "apiKey": "dummy",
				"models": []map[string]any{{"id": "one", "reasoning": test.reasoning}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "models.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			provider, model := "local", "one"
			args := CLIArgs{Provider: &provider, Model: &model, NoExtensions: true, NoContextFiles: true, NoSkills: true}
			if test.requested != "" {
				args.Thinking = &test.requested
			}
			inputs, err := createRuntimeInputs(t.TempDir(), args, engine.AgentMessages{})
			if err != nil {
				t.Fatal(err)
			}
			if got := inputs.Agent.State().ThinkingLevel; got != test.want {
				t.Fatalf("thinking = %q, want %q", got, test.want)
			}
		})
	}
}
