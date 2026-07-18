package harness

import (
	"encoding/json"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
)

type ContextEntryTransform func([]SessionTreeEntry) []SessionTreeEntry

type CustomEntryContextMessageProjector func(SessionTreeEntry, int, []SessionTreeEntry) agent.AgentMessages

type SessionContextBuildOptions struct {
	EntryTransforms []ContextEntryTransform
	EntryProjectors map[string]CustomEntryContextMessageProjector
}

func cloneSessionContextBuildOptions(options SessionContextBuildOptions) SessionContextBuildOptions {
	cloned := SessionContextBuildOptions{
		EntryTransforms: append([]ContextEntryTransform(nil), options.EntryTransforms...),
	}
	if options.EntryProjectors != nil {
		cloned.EntryProjectors = make(map[string]CustomEntryContextMessageProjector, len(options.EntryProjectors))
		for name, projector := range options.EntryProjectors {
			cloned.EntryProjectors[name] = projector
		}
	}
	return cloned
}

func mergeSessionContextBuildOptions(base SessionContextBuildOptions, overlays ...SessionContextBuildOptions) SessionContextBuildOptions {
	merged := cloneSessionContextBuildOptions(base)
	for _, overlay := range overlays {
		merged.EntryTransforms = append(merged.EntryTransforms, overlay.EntryTransforms...)
		if len(overlay.EntryProjectors) > 0 && merged.EntryProjectors == nil {
			merged.EntryProjectors = make(map[string]CustomEntryContextMessageProjector, len(overlay.EntryProjectors))
		}
		for name, projector := range overlay.EntryProjectors {
			merged.EntryProjectors[name] = projector
		}
	}
	return merged
}

// BuildSessionContext reduces one active path using the same state and latest
// compaction rules as upstream's harness Session.
func BuildSessionContext(entries []SessionTreeEntry, options ...SessionContextBuildOptions) SessionContext {
	resolved := mergeSessionContextBuildOptions(SessionContextBuildOptions{}, options...)
	contextState := SessionContext{ThinkingLevel: "off", Messages: []any{}}
	for _, entry := range entries {
		switch entry.Type {
		case "thinking_level_change":
			contextState.ThinkingLevel = entry.ThinkingLevel
		case "model_change":
			contextState.Model = &SessionModel{Provider: entry.Provider, ModelID: entry.ModelID}
		case "message":
			var envelope struct {
				Role     string `json:"role"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if json.Unmarshal(entry.Message, &envelope) == nil && envelope.Role == "assistant" {
				contextState.Model = &SessionModel{Provider: envelope.Provider, ModelID: envelope.Model}
			}
		case "active_tools_change":
			contextState.ActiveToolNames = cloneHarnessStrings(entry.ActiveToolNames)
		}
	}
	contextEntries := BuildContextEntries(entries, resolved)
	for index, entry := range contextEntries {
		if entry.Type == "custom" {
			if projector := resolved.EntryProjectors[entry.CustomType]; projector != nil {
				contextState.Messages = append(contextState.Messages, projector(entry.clone(), index, cloneHarnessEntries(contextEntries))...)
			}
			continue
		}
		if entry.Type == "branch_summary" && entry.Summary == "" {
			continue
		}
		if message := entryMessage(projectTreeEntry(entry), true); message != nil {
			contextState.Messages = append(contextState.Messages, message)
		}
	}
	return contextState
}

func BuildContextEntries(entries []SessionTreeEntry, options ...SessionContextBuildOptions) []SessionTreeEntry {
	selected := DefaultContextEntryTransform(entries)
	resolved := mergeSessionContextBuildOptions(SessionContextBuildOptions{}, options...)
	for _, transform := range resolved.EntryTransforms {
		if transform != nil {
			selected = cloneHarnessEntries(transform(cloneHarnessEntries(selected)))
		}
	}
	return selected
}

func DefaultContextEntryTransform(entries []SessionTreeEntry) []SessionTreeEntry {
	latest := -1
	for index := range entries {
		if entries[index].Type == "compaction" {
			latest = index
		}
	}
	if latest < 0 {
		return cloneHarnessEntries(entries)
	}
	selected := []SessionTreeEntry{entries[latest].clone()}
	foundFirstKept := false
	for index := 0; index < latest; index++ {
		if entries[index].ID == entries[latest].FirstKeptEntryID {
			foundFirstKept = true
		}
		if foundFirstKept {
			selected = append(selected, entries[index].clone())
		}
	}
	return append(selected, cloneHarnessEntries(entries[latest+1:])...)
}

func projectTreeEntry(entry SessionTreeEntry) SessionEntry {
	fromHook := false
	if entry.FromHook != nil {
		fromHook = *entry.FromHook
	}
	return SessionEntry{
		Type: entry.Type, ID: entry.ID, ParentID: cloneHarnessString(entry.ParentID), Timestamp: entry.Timestamp,
		Message: entry.Message, Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID,
		TokensBefore: entry.TokensBefore, Details: entry.Details, FromHook: fromHook,
		FromID: entry.FromID, CustomType: entry.CustomType, Content: entry.Content, Display: entry.Display,
	}
}

// EntriesToFork selects the source entries copied by repository fork.
func EntriesToFork(storage SessionStorage, entryID string, position ForkPosition) ([]SessionTreeEntry, error) {
	if entryID == "" {
		return storage.Entries(), nil
	}
	target, ok := storage.Entry(entryID)
	if !ok {
		return nil, newSessionError(SessionErrorInvalidFork, "Entry %s not found", entryID)
	}
	leaf := target.ID
	if position == "" || position == ForkBefore {
		if target.Type != "message" || rawMessageRole(target.Message) != "user" {
			return nil, newSessionError(SessionErrorInvalidFork, "Entry %s is not a user message", entryID)
		}
		if target.ParentID == nil {
			return []SessionTreeEntry{}, nil
		}
		leaf = *target.ParentID
	}
	return storage.PathToRoot(&leaf)
}

func rawMessageRole(message json.RawMessage) string {
	var envelope struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(message, &envelope)
	return envelope.Role
}

func trimHarnessJSSpace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch {
		case character >= '\t' && character <= '\r':
			return true
		case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
			return true
		case character >= '\u2000' && character <= '\u200a':
			return true
		default:
			return false
		}
	})
}
