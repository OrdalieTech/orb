package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

type rpcSessionHost struct {
	mu           sync.RWMutex
	args         CLIArgs
	dependencies cliDependencies
	session      *codingagent.SessionRuntime
	disposed     bool
}

func rpcSlashCommands(runtime *codingagent.SessionRuntime) []modes.RPCSlashCommand {
	if runtime == nil {
		return []modes.RPCSlashCommand{}
	}
	commands := runtime.Commands()
	result := make([]modes.RPCSlashCommand, 0, len(commands))
	for _, command := range commands {
		var description, baseDir *string
		if command.Description != "" {
			description = stringValue(command.Description)
		}
		if command.SourceInfo.BaseDir != "" {
			baseDir = stringValue(command.SourceInfo.BaseDir)
		}
		result = append(result, modes.RPCSlashCommand{
			Name: command.Name, Description: description, Source: string(command.Source),
			SourceInfo: modes.RPCSourceInfo{
				Path: command.SourceInfo.Path, Source: command.SourceInfo.Source,
				Scope: command.SourceInfo.Scope, Origin: command.SourceInfo.Origin, BaseDir: baseDir,
			},
		})
	}
	return result
}

func newRPCSessionHost(
	args CLIArgs,
	dependencies cliDependencies,
	runtime *codingagent.SessionRuntime,
) *rpcSessionHost {
	return &rpcSessionHost{args: args, dependencies: dependencies, session: runtime}
}

func (host *rpcSessionHost) Session() *codingagent.SessionRuntime {
	host.mu.RLock()
	defer host.mu.RUnlock()
	return host.session
}

func (host *rpcSessionHost) NewSession(parentSession string) (bool, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	current, err := host.currentLocked()
	if err != nil {
		return false, err
	}
	manager := current.Manager()
	var replacement *session.SessionManager
	if manager.IsPersisted() {
		replacement, err = session.Create(manager.GetCWD(), manager.GetSessionDir())
	} else {
		replacement, err = session.InMemory(manager.GetCWD())
	}
	if err != nil {
		return false, err
	}
	if parentSession != "" {
		parent := parentSession
		_, err = replacement.NewSession(session.NewSessionOptions{ParentSession: &parent})
		if err != nil {
			return false, err
		}
	}
	return false, host.replaceLocked(replacement)
}

func (host *rpcSessionHost) SwitchSession(sessionPath string) (bool, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	current, err := host.currentLocked()
	if err != nil {
		return false, err
	}
	replacement, err := session.Open(sessionPath, "")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(replacement.GetCWD()); err != nil {
		return false, fmt.Errorf("Stored session working directory does not exist: %s\nSession file: %s\nCurrent working directory: %s", replacement.GetCWD(), replacement.GetSessionFile(), current.Manager().GetCWD()) //nolint:staticcheck // Upstream error text.
	}
	return false, host.replaceLocked(replacement)
}

func (host *rpcSessionHost) Fork(entryID string, atEntry bool) (string, bool, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	current, err := host.currentLocked()
	if err != nil {
		return "", false, err
	}
	manager := current.Manager()
	selected := manager.GetEntry(entryID)
	if selected == nil {
		return "", false, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream RPC error text.
	}
	targetID := selected.ID
	selectedText := ""
	if !atEntry {
		role, text := rpcMessageRoleAndText(selected.Message)
		if selected.Type != "message" || role != "user" {
			return "", false, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream RPC error text.
		}
		selectedText = text
		if selected.ParentID == nil {
			targetID = ""
		} else {
			targetID = *selected.ParentID
		}
	}

	var replacement *session.SessionManager
	if manager.IsPersisted() {
		currentFile := manager.GetSessionFile()
		if currentFile == "" {
			return "", false, errors.New("Persisted session is missing a session file") //nolint:staticcheck // Upstream RPC error text.
		}
		if targetID == "" {
			replacement, err = session.Create(manager.GetCWD(), manager.GetSessionDir())
			if err == nil {
				parent := currentFile
				_, err = replacement.NewSession(session.NewSessionOptions{ParentSession: &parent})
			}
		} else {
			if _, statErr := os.Stat(currentFile); statErr != nil {
				return "", false, errors.New("This session has not been saved yet. Wait for the first assistant response before cloning or forking it.") //nolint:staticcheck // Upstream RPC error text.
			}
			replacement, err = session.Open(currentFile, manager.GetSessionDir())
			if err == nil {
				_, err = replacement.CreateBranchedSession(targetID)
			}
		}
	} else {
		replacement = manager
		if targetID == "" {
			options := session.NewSessionOptions{}
			if currentFile := manager.GetSessionFile(); currentFile != "" {
				options.ParentSession = &currentFile
			}
			_, err = replacement.NewSession(options)
		} else {
			_, err = replacement.CreateBranchedSession(targetID)
		}
	}
	if err != nil {
		return "", false, err
	}
	return selectedText, false, host.replaceLocked(replacement)
}

func (host *rpcSessionHost) Dispose() {
	host.mu.Lock()
	if host.disposed {
		host.mu.Unlock()
		return
	}
	host.disposed = true
	current := host.session
	host.session = nil
	host.mu.Unlock()
	if current != nil {
		current.Dispose()
	}
}

func (host *rpcSessionHost) currentLocked() (*codingagent.SessionRuntime, error) {
	if host.disposed || host.session == nil {
		return nil, errors.New("RPC session host is disposed")
	}
	return host.session, nil
}

func (host *rpcSessionHost) replaceLocked(manager *session.SessionManager) error {
	contextState := manager.BuildSessionContext()
	args := host.args
	if len(manager.GetEntries()) > 0 {
		applySessionDefaults(&args, contextState, manager.GetBranch())
	}
	previous := host.session
	if previous != nil {
		previous.Dispose()
	}
	inputs, err := host.dependencies.createRuntime(manager.GetCWD(), args, decodeSessionMessages(contextState.Messages))
	if err != nil {
		return err
	}
	if err := appendInitialRuntimeState(manager, inputs.Agent.State(), contextState); err != nil {
		return err
	}
	settings := inputs.Settings
	if settings == nil {
		agentDir, settingsErr := config.GetAgentDir()
		if settingsErr != nil {
			return settingsErr
		}
		settings, settingsErr = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir))
		if settingsErr != nil {
			return settingsErr
		}
	}
	replacement, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: inputs.Agent, SessionManager: manager, Settings: settings,
		GetAPIKey: inputs.GetAPIKey, GetRequestAuth: inputs.GetRequestAuth, GetModelHeaders: inputs.GetModelHeaders,
		AvailableModels: inputs.AvailableModels,
		ScopedModels:    inputs.ScopedModels,
		SlashResolver:   inputs.SlashResolver,
	})
	if err != nil {
		return err
	}
	host.session = replacement
	return nil
}

func rpcMessageRoleAndText(raw json.RawMessage) (string, string) {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return "", ""
	}
	var plain string
	if json.Unmarshal(message.Content, &plain) == nil {
		return message.Role, plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return message.Role, ""
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return message.Role, text.String()
}
