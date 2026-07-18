//go:build conformance

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

const f7ScenarioEnv = "PI_GO_F7_SCENARIO"

type f7Scenario struct {
	FixedNow     int64             `json:"fixedNow"`
	CWD          string            `json:"cwd"`
	SessionID    string            `json:"sessionId"`
	SystemPrompt string            `json:"systemPrompt"`
	TokenSize    int               `json:"tokenSize"`
	Responses    []json.RawMessage `json:"responses"`
}

type f7SessionHost struct {
	session *codingagent.SessionRuntime
}

func (host *f7SessionHost) Session() *codingagent.SessionRuntime { return host.session }
func (*f7SessionHost) NewSession(string) (bool, error)           { return true, nil }
func (*f7SessionHost) SwitchSession(string) (bool, error)        { return true, nil }
func (*f7SessionHost) Fork(string, bool) (string, bool, error)   { return "", true, nil }
func (host *f7SessionHost) Dispose()                             { host.session.Dispose() }

func platformCLIDependencies() cliDependencies {
	return cliDependencies{createRuntime: createRuntimeInputs, runRPCFixture: runF7RPCFixture}
}

// runF7RPCFixture supplies deterministic model and session boundaries to the
// generated F7 transcript while retaining the production CLI and RPC paths.
func runF7RPCFixture(ctx context.Context, _ CLIArgs, streams cliStreams, _ string) (bool, int) {
	path := os.Getenv(f7ScenarioEnv)
	if path == "" {
		return false, 0
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	var scenario f7Scenario
	if err := json.Unmarshal(encoded, &scenario); err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	runtime, err := newF7SessionRuntime(scenario)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	return true, modes.RunRPCMode(ctx, &f7SessionHost{session: runtime}, modes.RPCModeOptions{
		Stdin: streams.Stdin, Stdout: streams.Stdout, Stderr: streams.Stderr,
	})
}

func newF7SessionRuntime(scenario f7Scenario) (*codingagent.SessionRuntime, error) {
	if scenario.CWD == "" || scenario.SessionID == "" || scenario.TokenSize < 1 {
		return nil, errors.New("invalid F7 conformance scenario")
	}
	provider := faux.New(faux.Options{
		API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(scenario.TokenSize),
		Now: func() int64 { return scenario.FixedNow },
	})
	responses := make([]faux.ResponseStep, len(scenario.Responses))
	for index, raw := range scenario.Responses {
		message, err := ai.UnmarshalMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("decode F7 response %d: %w", index, err)
		}
		assistant, ok := message.(*ai.AssistantMessage)
		if !ok {
			return nil, fmt.Errorf("decode F7 response %d: got %T", index, message)
		}
		responses[index] = assistant
	}
	provider.SetResponses(responses)

	agentDir, err := config.GetAgentDir()
	if err != nil {
		return nil, err
	}
	settings, err := config.NewSettingsManager(filepath.Dir(agentDir), config.WithAgentDir(agentDir))
	if err != nil {
		return nil, err
	}
	nextEntryID := 0
	manager, err := session.InMemory(
		scenario.CWD,
		session.WithSessionID(scenario.SessionID),
		session.WithClock(func() time.Time { return time.UnixMilli(scenario.FixedNow).UTC() }),
		session.WithEntryIDGenerator(func() (string, error) {
			nextEntryID++
			return fmt.Sprintf("%08x", nextEntryID), nil
		}),
	)
	if err != nil {
		return nil, err
	}
	model := provider.GetModel()
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{
			Model: model, SystemPrompt: scenario.SystemPrompt, Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
		}),
		agent.WithStreamFn(provider.StreamSimple),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
		agent.WithClock(func() int64 { return scenario.FixedNow }),
	)
	return codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: provider.StreamSimple,
		Clock: func() int64 { return scenario.FixedNow },
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "faux-key"
			return &key, nil
		},
		AvailableModels: func() []ai.Model { return []ai.Model{*model} },
	})
}
