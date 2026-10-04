package chat

import (
	"testing"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
)

// The native chat gateway keeps sessions in harness journals, so the ledger
// must read the same over both backends.
func TestTurnLedgerRoundTrip(t *testing.T) {
	file, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := harness.OpenSessionJournal([]byte(`{"type":"session","version":3,"id":"s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/"}`+"\n"), func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	native, err := sessionstore.FromHarnessStorage(journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*sessionstore.SessionManager{file, native} {
		assertTurnLedgerRoundTrip(t, manager)
	}
}

func assertTurnLedgerRoundTrip(t *testing.T, manager *sessionstore.SessionManager) {
	t.Helper()
	receipt := Receipt{MessageIDs: []string{"a", "b"}, At: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	startedID := mustAppendMarker(t, manager, turnMarker{EventID: "ev", Phase: phaseStarted})
	mustAppendMarker(t, manager, turnMarker{EventID: "ev", Phase: phasePreview, PreviewID: "pv-7"})
	mustAppendMarker(t, manager, turnMarker{
		EventID: "ev", Phase: phaseSettled, Outcome: outcomeOK, AssistantEntryID: "entry-9",
	})
	mustAppendMarker(t, manager, turnMarker{EventID: "ev", Phase: phaseDelivered, Receipt: &receipt})
	// A different event's markers must not bleed in.
	mustAppendMarker(t, manager, turnMarker{EventID: "other", Phase: phaseStarted})

	ledger := scanTurnLedger(manager, "ev")
	if ledger.startedID != startedID {
		t.Fatalf("startedID = %q, want %q", ledger.startedID, startedID)
	}
	if ledger.preview == nil || ledger.preview.PreviewID != "pv-7" {
		t.Fatalf("preview = %+v", ledger.preview)
	}
	if ledger.settled == nil || ledger.settled.Outcome != outcomeOK || ledger.settled.AssistantEntryID != "entry-9" {
		t.Fatalf("settled = %+v", ledger.settled)
	}
	got := ledger.delivered.Receipt
	if got == nil || len(got.MessageIDs) != 2 || got.MessageIDs[0] != "a" || !got.At.Equal(receipt.At) {
		t.Fatalf("receipt round-trip = %+v", got)
	}

	other := scanTurnLedger(manager, "other")
	if other.startedID == "" || other.settled != nil || other.delivered != nil {
		t.Fatalf("other event ledger = %+v", other)
	}
	if missing := scanTurnLedger(manager, "absent"); missing != (turnLedger{}) {
		t.Fatalf("absent event ledger = %+v", missing)
	}
}

func TestConversationKeyStringIsSafeAndInjective(t *testing.T) {
	weird := ConversationKey{Platform: "telegram", Account: "bot", ChatID: "we/ird?~id", ThreadID: "a_"}
	safe := weird.String()
	for _, forbidden := range []string{"/", "?", "\\"} {
		if containsAny(safe, forbidden) {
			t.Fatalf("key %q contains forbidden %q", safe, forbidden)
		}
	}
	// Injective: shifting a character across the segment boundary must not
	// collide.
	left := ConversationKey{Platform: "p", Account: "a_", ChatID: "b"}
	right := ConversationKey{Platform: "p", Account: "a", ChatID: "_b"}
	if left.String() == right.String() {
		t.Fatalf("segment boundary collision: %q", left.String())
	}
	first, second := weird.String(), weird.String()
	if first != second {
		t.Fatal("key string is not stable")
	}
}

func containsAny(s, chars string) bool {
	for _, c := range chars {
		for _, r := range s {
			if r == c {
				return true
			}
		}
	}
	return false
}
