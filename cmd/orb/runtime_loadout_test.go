package main

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestCLIFirstRequestDeclaresExecutableTools(t *testing.T) {
	for _, test := range []struct {
		name            string
		overrideBash    bool
		bootstrapPrompt string
	}{
		{name: "builtins"},
		{name: "overridden-bash", overrideBash: true},
		{name: "unrecorded-bootstrap", overrideBash: true, bootstrapPrompt: "unrecorded-bootstrap-prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv(config.EnvAgentDir, t.TempDir())
			t.Setenv("ORB_OFFLINE", "1")
			registry := extensions.NewRegistry(cwd)
			if test.overrideBash {
				if err := registry.Register("<inline:bash>", func(api extensions.API) error {
					api.RegisterTool(extensions.ToolDefinition{Name: "bash", Description: "Overridden bash"})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			provider, model, key := "openai", "gpt-test", "fixture-key"
			var request ai.Context
			calls := 0
			var inputs runtimeInputs
			manager, err := session.InMemory(cwd)
			if err != nil {
				t.Fatal(err)
			}
			host, err := newCLISessionRuntimeHost(context.Background(), cliSessionRuntimeHostOptions{
				Args: &CLIArgs{
					Provider: &provider, Model: &model, APIKey: &key,
					NoExtensions: true, NoSkills: true, NoContextFiles: true,
					extensionsLoaded: true, extensionRegistry: registry,
				},
				Manager: manager, ExtensionMode: extensions.ModePrint,
				Dependencies: cliDependencies{createRuntime: func(cwd string, args CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
					created, err := createRuntimeInputs(cwd, args, prior)
					if err != nil {
						return created, err
					}
					if test.bootstrapPrompt != "" {
						created.Agent.AppendMessage(&ai.SystemMessage{Content: test.bootstrapPrompt})
					}
					created.StreamFn = func(_ context.Context, model *ai.Model, current ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
						request = current
						calls++
						message := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID,
							Content: ai.AssistantContent{&ai.TextContent{Text: "done"}}, StopReason: ai.StopReasonStop}
						return func(yield func(ai.AssistantMessageEvent, error) bool) {
							yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}, nil)
						}, nil
					}
					return created, nil
				}},
				Created: func(created runtimeInputs) { inputs = created },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Dispose(context.Background())
			runtime := host.Session()
			if err := runtime.Prompt(context.Background(), "Use the coding tools."); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || request.Tools == nil {
				t.Fatalf("requests = %d, tools = %v", calls, request.Tools)
			}
			if request.SystemPrompt == nil || strings.Contains(*request.SystemPrompt, "unrecorded-bootstrap-prompt") {
				t.Fatalf("request prompt = %v", request.SystemPrompt)
			}
			names := make([]string, 0, len(*request.Tools))
			for _, tool := range *request.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"bash", "edit", "read", "write"}) {
				t.Fatalf("first-request tools = %v", names)
			}
			replay, err := agent.ConvertToLLM(context.Background(), manager.ContextMessages())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ai.CurrentTools(replay), *request.Tools) {
				t.Fatalf("persisted tools differ from request tools: %#v / %#v", ai.CurrentTools(replay), *request.Tools)
			}
			executable := inputs.Agent.State().Tools
			if len(executable) != len(*request.Tools) {
				t.Fatalf("executable tools = %d, declared tools = %d", len(executable), len(*request.Tools))
			}
			for _, tool := range executable {
				spec := tool.Spec()
				index := slices.IndexFunc(*request.Tools, func(declared ai.Tool) bool { return declared.Name == spec.Name })
				if index < 0 || (*request.Tools)[index].Description != spec.Description || !reflect.DeepEqual((*request.Tools)[index].Parameters, spec.Parameters) || !reflect.DeepEqual((*request.Tools)[index].ConstrainedSampling, spec.ConstrainedSampling) {
					t.Fatalf("executable %q differs from its declaration", spec.Name)
				}
			}
		})
	}
}
