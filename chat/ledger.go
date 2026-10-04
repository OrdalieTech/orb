package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

// turnCustomType is the session custom-entry type of turn ledger markers.
// Markers are appended via AppendCustomEntry (never AppendCustomMessageEntry,
// which would be injected into model context) and read from raw session
// entries so compaction never hides them.
const turnCustomType = "orb.chat.turn"

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

// turnLedger is the per-event view of the ledger: the most recent marker for
// each phase, or nil when that phase was never recorded.
type turnLedger struct {
	startedID                   string // entry holding the started marker
	preview, settled, delivered *turnMarker
}

// appendTurnMarker durably appends one ledger marker and returns its entry id.
func appendTurnMarker(manager *sessionstore.SessionManager, marker turnMarker) (string, error) {
	entryID, err := manager.AppendCustomEntry(turnCustomType, marker)
	if err != nil {
		return "", fmt.Errorf("chat: append %s marker: %w", marker.Phase, err)
	}
	return entryID, nil
}

// turnLedgers folds every raw ledger marker (branch-independent) into
// per-event state, in first-seen order; a non-empty only skips other events.
func turnLedgers(manager *sessionstore.SessionManager, only string) ([]string, map[string]*turnLedger) {
	var order []string
	ledgers := map[string]*turnLedger{}
	for entryID, data := range manager.CustomData(turnCustomType) {
		var marker turnMarker
		if json.Unmarshal(data, &marker) != nil || only != "" && marker.EventID != only {
			continue
		}
		ledger := ledgers[marker.EventID]
		if ledger == nil {
			ledger = &turnLedger{}
			ledgers[marker.EventID] = ledger
			order = append(order, marker.EventID)
		}
		switch marker.Phase {
		case phaseStarted:
			ledger.startedID = entryID
		case phasePreview:
			ledger.preview = &marker
		case phaseSettled:
			ledger.settled = &marker
		case phaseDelivered:
			ledger.delivered = &marker
		}
	}
	return order, ledgers
}

// scanTurnLedger returns the ledger state for eventID.
func scanTurnLedger(manager *sessionstore.SessionManager, eventID string) turnLedger {
	if _, ledgers := turnLedgers(manager, eventID); ledgers[eventID] != nil {
		return *ledgers[eventID]
	}
	return turnLedger{}
}

// carryableMarkers collects the ledger knowledge that must survive a session
// switch (/new): delivered markers keep redelivered events deduplicated, and
// settled-but-undelivered markers keep the never-re-prompt guarantee, with the
// reply text embedded because the assistant entry stays behind in the old
// session. Started-only events are not carried — their entry ids would dangle
// in the new session, and an unfinished turn is re-run by design.
func carryableMarkers(manager *sessionstore.SessionManager) []turnMarker {
	order, ledgers := turnLedgers(manager, "")
	var markers []turnMarker
	for _, eventID := range order {
		switch ledger := ledgers[eventID]; {
		case ledger.delivered != nil:
			markers = append(markers, *ledger.delivered)
		case ledger.settled != nil:
			settled := *ledger.settled
			if settled.Outcome == outcomeOK && settled.RecoveredText == "" {
				settled.RecoveredText = assistantText(decodeAssistantEntry(manager.GetEntry(settled.AssistantEntryID)))
			}
			settled.AssistantEntryID = "" // the entry does not survive the switch
			if ledger.preview != nil {
				markers = append(markers, *ledger.preview)
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
	assistant, _ := decoded.(*ai.AssistantMessage)
	return assistant
}
