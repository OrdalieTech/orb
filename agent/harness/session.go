package harness

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
)

func (session *Session) LeafID() (*string, error) {
	if session == nil || session.storage == nil {
		return nil, nil
	}
	return session.storage.LeafID()
}

func (session *Session) Entry(id string) (*SessionTreeEntry, bool) {
	if session == nil || session.storage == nil {
		return nil, false
	}
	return session.storage.Entry(id)
}

func (session *Session) Entries(options ...SessionEntryCursorOptions) []SessionTreeEntry {
	if session == nil || session.storage == nil {
		return []SessionTreeEntry{}
	}
	return session.storage.Entries(options...)
}

func (session *Session) Branch(fromID ...string) ([]SessionTreeEntry, error) {
	if session == nil || session.storage == nil {
		return []SessionTreeEntry{}, nil
	}
	var leaf *string
	if len(fromID) > 0 {
		leaf = cloneHarnessString(&fromID[0])
	} else {
		var err error
		leaf, err = session.storage.LeafID()
		if err != nil {
			return nil, err
		}
	}
	return session.storage.PathToRootOrCompaction(leaf)
}

func (session *Session) Label(id string) (string, bool) {
	if session == nil || session.storage == nil {
		return "", false
	}
	return session.storage.Label(id)
}

func (session *Session) appendEntry(entry SessionTreeEntry) (string, error) {
	if session == nil || session.storage == nil {
		return "", newSessionError(SessionErrorStorage, "Session storage is unavailable")
	}
	id, err := session.storage.CreateEntryID()
	if err != nil {
		return "", err
	}
	entry.ID = id
	if entry.Type != "branch_summary" {
		entry.ParentID, err = session.storage.LeafID()
		if err != nil {
			return "", err
		}
	}
	entry.Timestamp = formatHarnessTimestamp(time.Now())
	entry.raw = nil
	if err := session.storage.AppendEntry(entry); err != nil {
		return "", err
	}
	return id, nil
}

func (session *Session) AppendMessage(message any) (string, error) {
	raw, err := marshalHarnessValue(message)
	if err != nil {
		return "", err
	}
	entry := SessionTreeEntry{Type: "message"}
	entry.Message = raw
	return session.appendEntry(entry)
}

func (session *Session) AppendThinkingLevelChange(level string) (string, error) {
	entry := SessionTreeEntry{Type: "thinking_level_change"}
	entry.ThinkingLevel = level
	return session.appendEntry(entry)
}

func (session *Session) AppendModelChange(provider, modelID string) (string, error) {
	entry := SessionTreeEntry{Type: "model_change"}
	entry.Provider, entry.ModelID = provider, modelID
	return session.appendEntry(entry)
}

func (session *Session) AppendActiveToolsChange(toolNames []string) (string, error) {
	entry := SessionTreeEntry{Type: "active_tools_change"}
	entry.ActiveToolNames = cloneHarnessStrings(toolNames)
	return session.appendEntry(entry)
}

func (session *Session) AppendCompaction(summary, firstKeptEntryID string, tokensBefore float64, details any, fromHook *bool, usage ...*ai.Usage) (string, error) {
	var summaryUsage *ai.Usage
	if len(usage) > 0 {
		summaryUsage = usage[0]
	}
	return session.AppendCompactionWithTail(summary, firstKeptEntryID, tokensBefore, details, fromHook, summaryUsage, nil)
}

// AppendCompactionWithTail persists the v0.81 checkpoint form. A non-nil
// retained tail, including an empty one, marks the compaction as a complete
// ancestry checkpoint.
func (session *Session) AppendCompactionWithTail(
	summary, firstKeptEntryID string,
	tokensBefore float64,
	details any,
	fromHook *bool,
	usage *ai.Usage,
	retainedTail agent.AgentMessages,
) (string, error) {
	entry := SessionTreeEntry{Type: "compaction"}
	entry.Summary, entry.FirstKeptEntryID, entry.TokensBefore = summary, firstKeptEntryID, tokensBefore
	entry.FromHook = cloneHarnessBool(fromHook)
	entry.Usage = cloneHarnessUsage(usage)
	if retainedTail != nil {
		entry.RetainedTail = make([]json.RawMessage, len(retainedTail))
		for index, message := range retainedTail {
			encoded, err := marshalHarnessValue(message)
			if err != nil {
				return "", err
			}
			entry.RetainedTail[index] = encoded
		}
	}
	if details != nil {
		encoded, err := marshalHarnessValue(details)
		if err != nil {
			return "", err
		}
		entry.Details = encoded
	}
	return session.appendEntry(entry)
}

func (session *Session) AppendCustom(customType string, data ...any) (string, error) {
	entry := SessionTreeEntry{Type: "custom"}
	entry.CustomType = customType
	if len(data) > 0 {
		encoded, err := marshalHarnessValue(data[0])
		if err != nil {
			return "", err
		}
		entry.Data = encoded
	}
	return session.appendEntry(entry)
}

func (session *Session) AppendCustomMessage(customType string, content any, display bool, details ...any) (string, error) {
	entry := SessionTreeEntry{Type: "custom_message"}
	entry.CustomType, entry.Display = customType, display
	encoded, err := marshalHarnessValue(content)
	if err != nil {
		return "", err
	}
	entry.Content = encoded
	if len(details) > 0 {
		encoded, err = marshalHarnessValue(details[0])
		if err != nil {
			return "", err
		}
		entry.Details = encoded
	}
	return session.appendEntry(entry)
}

func (session *Session) AppendLabel(targetID string, label *string) (string, error) {
	if _, ok := session.storage.Entry(targetID); !ok {
		return "", newSessionError(SessionErrorNotFound, "Entry %s not found", targetID)
	}
	entry := SessionTreeEntry{Type: "label"}
	entry.TargetID, entry.HasTargetID, entry.Label = cloneHarnessString(&targetID), true, cloneHarnessString(label)
	return session.appendEntry(entry)
}

func (session *Session) AppendName(name string) (string, error) {
	entry := SessionTreeEntry{Type: "session_info"}
	entry.Name = sanitizeHarnessSessionName(name)
	return session.appendEntry(entry)
}

type BranchSummary struct {
	Summary  string
	Details  any
	Usage    *ai.Usage
	FromHook *bool
}

func (session *Session) MoveTo(entryID *string, summary *BranchSummary) (string, error) {
	if entryID != nil {
		if _, ok := session.storage.Entry(*entryID); !ok {
			return "", newSessionError(SessionErrorNotFound, "Entry %s not found", *entryID)
		}
	}
	if err := session.storage.SetLeafID(entryID); err != nil {
		return "", err
	}
	if summary == nil {
		return "", nil
	}
	entry := SessionTreeEntry{Type: "branch_summary"}
	entry.ParentID = cloneHarnessString(entryID)
	entry.FromID = "root"
	if entryID != nil {
		entry.FromID = *entryID
	}
	entry.Summary, entry.FromHook = summary.Summary, cloneHarnessBool(summary.FromHook)
	entry.Usage = cloneHarnessUsage(summary.Usage)
	if summary.Details != nil {
		encoded, err := marshalHarnessValue(summary.Details)
		if err != nil {
			return "", err
		}
		entry.Details = encoded
	}
	return session.appendEntry(entry)
}

func sanitizeHarnessSessionName(name string) string {
	var output strings.Builder
	output.Grow(len(name))
	inBreak := false
	for _, character := range name {
		if character == '\r' || character == '\n' {
			if !inBreak {
				output.WriteByte(' ')
				inBreak = true
			}
			continue
		}
		inBreak = false
		output.WriteRune(character)
	}
	return trimHarnessJSSpace(output.String())
}
