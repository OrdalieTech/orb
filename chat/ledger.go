package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// turnCustomType is the session custom-entry type of turn ledger markers.
// Markers are appended via AppendCustomEntry (never AppendCustomMessageEntry,
// which would be injected into model context) and read from raw session
// entries so compaction never hides them.
const turnCustomType = "pigo.chat.turn"

const (
	phaseStarted   = "started"
	phasePreview   = "preview"
	phaseSettled   = "settled"
	phaseDelivered = "delivered"
)

const (
	outcomeOK      = "ok"
	outcomeError   = "error"
	outcomeAborted = "aborted"
)

// turnMarker is the JSON payload of one ledger phase for one event.
type turnMarker struct {
	EventID          string   `json:"eventId"`
	Phase            string   `json:"phase"`
	PreviewID        string   `json:"previewId,omitempty"`
	Outcome          string   `json:"outcome,omitempty"`
	AssistantEntryID string   `json:"assistantEntryId,omitempty"`
	Receipt          *Receipt `json:"receipt,omitempty"`
	// RecoveredText is only set on settled markers carried across a /new
	// session switch: the assistant entry stays behind in the old session, so
	// the reply text travels with the marker for settled-recovery delivery.
	RecoveredText string `json:"recoveredText,omitempty"`
}

// ledgerEntry pairs a decoded marker with the session entry that holds it.
type ledgerEntry struct {
	entryID string
	marker  turnMarker
}

// turnLedger is the per-event view of the ledger: the most recent marker for
// each phase, or nil when that phase was never recorded.
type turnLedger struct {
	started   *ledgerEntry
	preview   *ledgerEntry
	settled   *ledgerEntry
	delivered *ledgerEntry
}

// appendTurnMarker durably appends one ledger marker and returns its entry id.
func appendTurnMarker(manager *sessionstore.SessionManager, marker turnMarker) (string, error) {
	entryID, err := manager.AppendCustomEntry(turnCustomType, marker)
	if err != nil {
		return "", fmt.Errorf("chat: append %s marker: %w", marker.Phase, err)
	}
	return entryID, nil
}

// scanTurnLedger reads every raw session entry (branch-independent) and
// returns the ledger state for eventID.
//
// ponytail: O(session history) with deep clones per inbound message; a
// non-cloning entry iterator (or tail index) on SessionManager is the upgrade
// path if profile time ever shows this next to the Acquire-time JSONL parse.
func scanTurnLedger(manager *sessionstore.SessionManager, eventID string) turnLedger {
	var ledger turnLedger
	for _, entry := range manager.GetEntries() {
		if entry.Type != "custom" || entry.CustomType != turnCustomType {
			continue
		}
		var marker turnMarker
		if err := json.Unmarshal(entry.Data, &marker); err != nil {
			continue
		}
		if marker.EventID != eventID {
			continue
		}
		record := &ledgerEntry{entryID: entry.ID, marker: marker}
		switch marker.Phase {
		case phaseStarted:
			ledger.started = record
		case phasePreview:
			ledger.preview = record
		case phaseSettled:
			ledger.settled = record
		case phaseDelivered:
			ledger.delivered = record
		}
	}
	return ledger
}

// carryableMarkers collects the ledger knowledge that must survive a session
// switch (/new): delivered markers keep redelivered events deduplicated, and
// settled-but-undelivered markers keep the never-re-prompt guarantee, with the
// reply text embedded because the assistant entry stays behind in the old
// session. Started-only events are not carried — their entry ids would dangle
// in the new session, and an unfinished turn is re-run by design.
func carryableMarkers(manager *sessionstore.SessionManager) []turnMarker {
	type eventState struct {
		preview, settled, delivered *turnMarker
		settledEntryID              string
	}
	states := map[string]*eventState{}
	var order []string
	for _, entry := range manager.GetEntries() {
		if entry.Type != "custom" || entry.CustomType != turnCustomType {
			continue
		}
		var marker turnMarker
		if err := json.Unmarshal(entry.Data, &marker); err != nil {
			continue
		}
		state := states[marker.EventID]
		if state == nil {
			state = &eventState{}
			states[marker.EventID] = state
			order = append(order, marker.EventID)
		}
		carried := marker
		switch marker.Phase {
		case phasePreview:
			state.preview = &carried
		case phaseSettled:
			state.settled = &carried
			state.settledEntryID = entry.ID
		case phaseDelivered:
			state.delivered = &carried
		}
	}
	var markers []turnMarker
	for _, eventID := range order {
		state := states[eventID]
		switch {
		case state.delivered != nil:
			markers = append(markers, *state.delivered)
		case state.settled != nil:
			settled := *state.settled
			if settled.Outcome == outcomeOK && settled.RecoveredText == "" {
				settled.RecoveredText = assistantText(recoveredAssistant(manager, settled.AssistantEntryID, state.settledEntryID))
			}
			settled.AssistantEntryID = "" // the entry does not survive the switch
			if state.preview != nil {
				markers = append(markers, *state.preview)
			}
			markers = append(markers, settled)
		}
	}
	return markers
}

// assistantText concatenates the text blocks of an assistant message.
func assistantText(message *ai.AssistantMessage) string {
	if message == nil {
		return ""
	}
	if len(message.Content) == 1 {
		if text, ok := message.Content[0].(*ai.TextContent); ok {
			return text.Text
		}
	}
	var builder strings.Builder
	for _, block := range message.Content {
		if text, ok := block.(*ai.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

// decodeAssistantEntry decodes a session message entry into an assistant
// message, returning nil when the entry is missing or not an assistant turn.
func decodeAssistantEntry(entry *sessionstore.SessionEntry) *ai.AssistantMessage {
	if entry == nil || entry.Type != "message" || len(entry.Message) == 0 {
		return nil
	}
	decoded, err := ai.UnmarshalMessage(entry.Message)
	if err != nil {
		return nil
	}
	assistant, ok := decoded.(*ai.AssistantMessage)
	if !ok {
		return nil
	}
	return assistant
}

// assistantEntryAfter walks the current branch and returns the last assistant
// message entry appended after the entry afterID, or nil when none exists.
func assistantEntryAfter(manager *sessionstore.SessionManager, afterID string) *sessionstore.SessionEntry {
	branch := manager.GetBranch()
	start := 0
	for index := range branch {
		if branch[index].ID == afterID {
			start = index + 1
			break
		}
	}
	for index := len(branch) - 1; index >= start; index-- {
		if decodeAssistantEntry(&branch[index]) != nil {
			return &branch[index]
		}
	}
	return nil
}

// recoveredAssistant loads the assistant message for a settled marker: by
// assistantEntryId when recorded, else the last assistant entry on the
// settled marker's path.
func recoveredAssistant(manager *sessionstore.SessionManager, assistantEntryID, settledEntryID string) *ai.AssistantMessage {
	if assistantEntryID != "" {
		if assistant := decodeAssistantEntry(manager.GetEntry(assistantEntryID)); assistant != nil {
			return assistant
		}
	}
	branch := manager.GetBranch(settledEntryID)
	for index := len(branch) - 1; index >= 0; index-- {
		if assistant := decodeAssistantEntry(&branch[index]); assistant != nil {
			return assistant
		}
	}
	return nil
}
