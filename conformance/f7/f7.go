// Package f7 builds the deterministic session the F7 RPC transcript runs on:
// a faux model replaying the scenario's responses with fixed clocks and entry
// ids, behind a session host whose session switches are no-ops. The runner
// drives it in process; `go build -tags conformance ./cmd/orb` serves it as
// RPC mode when ScenarioEnv names a scenario.
package f7

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

const ScenarioEnv = "ORB_F7_SCENARIO"

type Scenario struct {
	SchemaVersion int               `json:"schemaVersion"`
	FixedNow      int64             `json:"fixedNow"`
	CWD           string            `json:"cwd"`
	SessionID     string            `json:"sessionId"`
	SystemPrompt  string            `json:"systemPrompt"`
	TokenSize     int               `json:"tokenSize"`
	Responses     []json.RawMessage `json:"responses"`
	Steps         []Step            `json:"steps"`
}

type Step struct {
	Name              string `json:"name"`
	Input             string `json:"input"`
	Framing           string `json:"framing"`
	ExpectedLineCount int    `json:"expectedLineCount"`
}

// Host serves one runtime and accepts every session switch without one.
type Host struct{ Runtime *agent.SessionRuntime }

func (host *Host) Session() *agent.SessionRuntime     { return host.Runtime }
func (*Host) NewSession(string) (bool, error)         { return true, nil }
func (*Host) SwitchSession(string) (bool, error)      { return true, nil }
func (*Host) Fork(string, bool) (string, bool, error) { return "", true, nil }
func (host *Host) Dispose()                           { host.Runtime.Dispose() }

// Load reads the scenario at path and builds its runtime on agentDir's settings.
func Load(path, agentDir string) (*agent.SessionRuntime, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var scenario Scenario
	if err := json.Unmarshal(encoded, &scenario); err != nil {
		return nil, err
	}
	if scenario.CWD == "" || scenario.SessionID == "" || scenario.TokenSize < 1 {
		return nil, errors.New("invalid F7 conformance scenario")
	}
	settings, err := config.NewSettingsManager(filepath.Dir(agentDir), config.WithAgentDir(agentDir))
	if err != nil {
		return nil, err
	}
	return NewRuntime(scenario, settings, scenario.CWD)
}

// NewRuntime builds the scenario's session runtime on settings, with an
// in-memory session rooted at sessionRoot.
func NewRuntime(scenario Scenario, settings *config.SettingsManager, sessionRoot string) (*agent.SessionRuntime, error) {
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
	nextEntryID := 0
	manager, err := session.InMemory(
		sessionRoot,
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
	initialPrompt, _, _ := strings.Cut(scenario.SystemPrompt, "\nCurrent working directory:")
	created := engine.NewAgent(
		provider.StreamSimple, engine.WithInitialState(engine.AgentState{
			Model: model, SystemPrompt: initialPrompt, Messages: engine.AgentMessages{}, Tools: []engine.AgentTool{},
		}),
		engine.WithConvertToLLM(agent.ConvertToLLM),
		engine.WithClock(func() int64 { return scenario.FixedNow }),
	)
	return agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: provider.StreamSimple,
		Clock:               func() int64 { return scenario.FixedNow },
		ExtensionRegistry:   extensions.NewRegistry(scenario.CWD),
		SystemPromptOptions: &agent.SystemPromptOptions{CustomPrompt: &initialPrompt, SelectedTools: []string{}, CWD: scenario.CWD},
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "faux-key"
			return &key, nil
		},
		AvailableModels: func() []ai.Model { return []ai.Model{*model} },
	})
}
