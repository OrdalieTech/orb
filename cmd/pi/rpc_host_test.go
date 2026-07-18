package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestRPCSessionHostRebindsNewSessionAndForksUserEntry(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(root, session.WithSessionID("original"))
	if err != nil {
		t.Fatal(err)
	}
	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	newAgent := func(messages agent.AgentMessages) *agent.Agent {
		return agent.NewAgent(agent.WithInitialState(agent.AgentState{
			Model: provider.GetModel(), Messages: messages,
		}))
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: newAgent(agent.AgentMessages{}), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	createCalls := 0
	host := newRPCSessionHost(CLIArgs{}, cliDependencies{
		createRuntime: func(cwd string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
			createCalls++
			if cwd != root {
				t.Fatalf("runtime cwd = %q, want %q", cwd, root)
			}
			return runtimeInputs{Agent: newAgent(prior), Settings: settings}, nil
		},
	}, runtime)
	defer host.Dispose()

	cancelled, err := host.NewSession("parent.jsonl")
	if err != nil || cancelled {
		t.Fatalf("new session = cancelled %v, error %v", cancelled, err)
	}
	current := host.Session()
	if current == nil || current == runtime || current.Manager().GetSessionID() == "original" {
		t.Fatalf("replacement runtime = %#v", current)
	}
	header := current.Manager().GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != "parent.jsonl" || createCalls != 1 {
		t.Fatalf("new session header = %#v, create calls = %d", header, createCalls)
	}

	entryID, err := current.Manager().AppendMessage(map[string]any{
		"role": "user", "content": "draft prompt", "timestamp": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, cancelled, err := host.Fork(entryID, false)
	if err != nil || cancelled || text != "draft prompt" {
		t.Fatalf("fork = text %q, cancelled %v, error %v", text, cancelled, err)
	}
	entries := host.Session().Manager().GetEntries()
	for _, entry := range entries {
		if entry.Type == "message" {
			t.Fatalf("fork-before replacement retained message entry: %#v", entries)
		}
	}
	if createCalls != 2 {
		t.Fatalf("fork create calls = %d, want 2", createCalls)
	}
	if err := host.Session().PromptPreflight(context.Background()); err != nil {
		t.Fatalf("replacement runtime model preflight: %v", err)
	}
}

func TestRPCSessionHostRestoresEachTargetModelFromImmutableCLIArgs(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")
	makeSession := func(id, provider, model, thinking string) string {
		t.Helper()
		manager, createErr := session.Create(root, sessionDir, session.WithSessionID(id))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = manager.AppendModelChange(provider, model); createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = manager.AppendThinkingLevelChange(thinking); createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = manager.AppendMessage(map[string]any{"role": "user", "content": "hello", "timestamp": 1}); createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = manager.AppendMessage(map[string]any{"role": "assistant", "content": []any{}, "provider": provider, "model": model, "timestamp": 2}); createErr != nil {
			t.Fatal(createErr)
		}
		return manager.GetSessionFile()
	}
	pathA := makeSession("session-a", "provider-a", "model-a", "low")
	pathB := makeSession("session-b", "provider-b", "model-b", "high")

	initialManager, err := session.InMemory(root, session.WithSessionID("initial"))
	if err != nil {
		t.Fatal(err)
	}
	initialAgent := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	initialRuntime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: initialAgent, SessionManager: initialManager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	var selections []string
	host := newRPCSessionHost(CLIArgs{}, cliDependencies{
		createRuntime: func(_ string, args CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
			if args.Provider == nil || args.Model == nil || args.Thinking == nil {
				t.Fatalf("restored args = %#v", args)
			}
			selections = append(selections, *args.Provider+"/"+*args.Model+":"+*args.Thinking)
			model := ai.Model{ID: *args.Model, Provider: ai.ProviderID(*args.Provider), API: "faux", Reasoning: true, ContextWindow: 100, MaxTokens: 10}
			created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
				Model: &model, ThinkingLevel: ai.ModelThinkingLevel(*args.Thinking), Messages: prior,
			}))
			return runtimeInputs{Agent: created, Settings: settings}, nil
		},
	}, initialRuntime)
	defer host.Dispose()

	if cancelled, err := host.SwitchSession(pathA); err != nil || cancelled {
		t.Fatalf("switch A = cancelled %t, %v", cancelled, err)
	}
	if cancelled, err := host.SwitchSession(pathB); err != nil || cancelled {
		t.Fatalf("switch B = cancelled %t, %v", cancelled, err)
	}
	if want := []string{"provider-a/model-a:low", "provider-b/model-b:high"}; !reflect.DeepEqual(selections, want) {
		t.Fatalf("selections = %#v, want %#v", selections, want)
	}
}

func TestRPCSlashCommandsPreserveOptionalWireFields(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:rpc>", func(api extensions.API) error {
		api.RegisterCommand("ext", extensions.Command{Description: "Extension command"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent:             agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}})),
		SessionManager:    manager,
		Settings:          settings,
		ExtensionRegistry: registry,
		ExtensionMode:     extensions.ModeRPC,
		SlashResolver: &codingagent.SlashResolver{
			PromptTemplates: []codingagent.PromptTemplate{{
				Name: "review", SourceInfo: codingagent.SourceInfo{
					Path: "review.md", Source: "local", Scope: "project", Origin: "top-level",
				},
			}},
			Skills: []codingagent.Skill{{
				Name: "inspect", Description: "Inspect files.", SourceInfo: codingagent.SourceInfo{
					Path: "inspect/SKILL.md", Source: "local", Scope: "project", Origin: "top-level", BaseDir: "inspect",
				},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	encoded, err := ai.Marshal(rpcSlashCommands(runtime))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"ext","description":"Extension command","source":"extension","sourceInfo":{"path":"<inline:rpc>","source":"inline","scope":"temporary","origin":"top-level"}},{"name":"review","source":"prompt","sourceInfo":{"path":"review.md","source":"local","scope":"project","origin":"top-level"}},{"name":"skill:inspect","description":"Inspect files.","source":"skill","sourceInfo":{"path":"inspect/SKILL.md","source":"local","scope":"project","origin":"top-level","baseDir":"inspect"}}]`
	if string(encoded) != want {
		t.Fatalf("RPC commands = %s, want %s", encoded, want)
	}
}
