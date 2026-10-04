package main

import (
	"context"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestCLINoToolsPromptDoesNotRecommendBash(t *testing.T) {
	for _, noExtensions := range []bool{false, true} {
		t.Run(map[bool]string{false: "extensions", true: "no-extensions"}[noExtensions], func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv(config.EnvAgentDir, t.TempDir())
			t.Setenv("PI_OFFLINE", "1")
			provider, model, key := "openai", "gpt-test", "dummy"
			inputs, err := createRuntimeInputs(cwd, CLIArgs{
				Provider: &provider, Model: &model, APIKey: &key, NoTools: true,
				NoExtensions: noExtensions, NoSkills: true, NoContextFiles: true,
				extensionsLoaded: true, extensionRegistry: extensions.NewRegistry(cwd),
			}, engine.AgentMessages{})
			if err != nil {
				t.Fatal(err)
			}
			inputs.StreamFn = func(_ context.Context, model *ai.Model, request ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
				prompt := ai.CurrentSystemPrompt(request.Messages)
				if request.SystemPrompt != nil {
					prompt += *request.SystemPrompt
				}
				if strings.Contains(prompt, "Use bash") {
					t.Errorf("request recommends unavailable bash: %q", prompt)
				}
				message := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonStop}
				return func(yield func(ai.AssistantMessageEvent, error) bool) {
					yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}, nil)
				}, nil
			}
			manager, err := session.InMemory(cwd)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := buildSessionRuntime(inputs, manager, sessionRuntimeOptions{mode: extensions.ModePrint})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Dispose()
			if err := runtime.Prompt(context.Background(), "reply"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(runtime.State().SystemPrompt, "Use bash") {
				t.Fatal("runtime prompt recommends unavailable bash")
			}
			if len(runtime.State().Tools) != 0 {
				t.Fatal("tools became executable in --no-tools mode")
			}
		})
	}
}
