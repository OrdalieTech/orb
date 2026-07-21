package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
)

func TestHandleDeliversTurnAndWritesLedger(t *testing.T) {
	env := newTestEnv(t, nil)
	env.provider.SetResponses(responsesOf("hello there"))
	m := testMessage("ev-1", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	delivery := env.adapter.delivery(t, 0)
	if delivery.typings != 1 {
		t.Fatalf("typing calls = %d, want 1", delivery.typings)
	}
	if got := delivery.snapshotFinalized(); len(got) != 1 || got[0] != "hello there" {
		t.Fatalf("finalized = %#v", got)
	}
	if delivery.resumePreviewID != "" || delivery.replyTo != "ev-1" {
		t.Fatalf("delivery created with resume=%q replyTo=%q", delivery.resumePreviewID, delivery.replyTo)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), "ev-1")
	if got := phasesOf(markers); len(got) != 3 || got[0] != "started" || got[1] != "settled" || got[2] != "delivered" {
		t.Fatalf("phases = %v", got)
	}
	settled := markers[1]
	if settled.Outcome != "ok" || settled.AssistantEntryID == "" {
		t.Fatalf("settled marker = %+v", settled)
	}
	delivered := markers[2]
	if delivered.Receipt == nil || len(delivered.Receipt.MessageIDs) != 1 || delivered.Receipt.MessageIDs[0] != "fin-1" {
		t.Fatalf("delivered marker = %+v", delivered)
	}
}

func TestDuplicateEventIsNoOp(t *testing.T) {
	env := newTestEnv(t, nil)
	env.provider.SetResponses(responsesOf("only once"))
	m := testMessage("ev-dup", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls := env.provider.State().CallCount; calls != 1 {
		t.Fatalf("stream calls = %d, want 1", calls)
	}
	if count := env.adapter.deliveryCount(); count != 1 {
		t.Fatalf("deliveries = %d, want 1 (duplicate must not touch the adapter)", count)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), "ev-dup")
	if got := phasesOf(markers); len(got) != 3 {
		t.Fatalf("duplicate re-appended markers: %v", got)
	}
}

func TestReplayAfterStartedRunsTurnWithoutSecondStartedMarker(t *testing.T) {
	env := newTestEnv(t, nil)
	env.provider.SetResponses(responsesOf("recovered run"))
	m := testMessage("ev-crash-started", "chat-1", "hi")
	manager := env.sessions.manager(t, m.Key())
	mustAppendMarker(t, manager, turnMarker{EventID: m.EventID, Phase: phaseStarted})

	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	markers := markersFor(t, manager, m.EventID)
	if got := phasesOf(markers); len(got) != 3 || got[0] != "started" || got[1] != "settled" || got[2] != "delivered" {
		t.Fatalf("phases = %v (started must not repeat)", got)
	}
	if got := env.adapter.delivery(t, 0).snapshotFinalized(); len(got) != 1 || got[0] != "recovered run" {
		t.Fatalf("finalized = %#v", got)
	}
}

func TestReplayAfterUserMessageOrphansPartialBranch(t *testing.T) {
	env := newTestEnv(t, nil)
	env.provider.SetResponses(responsesOf("fresh reply"))
	m := testMessage("ev-crash-user", "chat-1", "hi")
	manager := env.sessions.manager(t, m.Key())
	startedID := mustAppendMarker(t, manager, turnMarker{EventID: m.EventID, Phase: phaseStarted})
	orphanID, err := manager.AppendMessage(map[string]any{
		"role": "user", "content": "orphaned partial", "timestamp": 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	// The started marker must now have two children in the manager tree: the
	// orphaned partial user message and the replayed turn's user message.
	children := manager.GetChildren(startedID)
	if len(children) != 2 {
		t.Fatalf("children of started marker = %d, want 2 (orphan + replay)", len(children))
	}
	sawOrphan := false
	for _, child := range children {
		if child.ID == orphanID {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Fatal("orphaned partial user message left the tree")
	}
	// The orphan must be off the delivered branch.
	for _, entry := range manager.GetBranch() {
		if entry.ID == orphanID {
			t.Fatal("orphaned entry still on the active branch")
		}
	}
	markers := markersFor(t, manager, m.EventID)
	if got := phasesOf(markers); len(got) != 3 || got[1] != "settled" || got[2] != "delivered" {
		t.Fatalf("phases = %v", got)
	}
}

// seedSettledTurn reproduces the on-disk state of a turn that crashed after
// settling: started + user + assistant + optional preview + settled markers.
func seedSettledTurn(t *testing.T, env *testEnv, m Message, previewID string) {
	t.Helper()
	manager := env.sessions.manager(t, m.Key())
	mustAppendMarker(t, manager, turnMarker{EventID: m.EventID, Phase: phaseStarted})
	if _, err := manager.AppendMessage(map[string]any{"role": "user", "content": "hi", "timestamp": 1}); err != nil {
		t.Fatal(err)
	}
	assistantID, err := manager.AppendMessage(faux.AssistantMessage("the settled answer"))
	if err != nil {
		t.Fatal(err)
	}
	if previewID != "" {
		mustAppendMarker(t, manager, turnMarker{EventID: m.EventID, Phase: phasePreview, PreviewID: previewID})
	}
	mustAppendMarker(t, manager, turnMarker{
		EventID: m.EventID, Phase: phaseSettled, Outcome: outcomeOK, AssistantEntryID: assistantID,
	})
}

func TestRecoverySettledResumesPreviewAndPrefixesReply(t *testing.T) {
	env := newTestEnv(t, nil)
	m := testMessage("ev-settled", "chat-1", "hi")
	seedSettledTurn(t, env, m, "pv-9")

	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls := env.provider.State().CallCount; calls != 0 {
		t.Fatalf("stream calls = %d, want 0 (settled turns must not re-prompt)", calls)
	}
	delivery := env.adapter.delivery(t, 0)
	if delivery.resumePreviewID != "pv-9" {
		t.Fatalf("resumePreviewID = %q, want pv-9", delivery.resumePreviewID)
	}
	want := "♻ recovered reply\n\nthe settled answer"
	if got := delivery.snapshotFinalized(); len(got) != 1 || got[0] != want {
		t.Fatalf("finalized = %#v, want %q", got, want)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), m.EventID)
	if phases := phasesOf(markers); phases[len(phases)-1] != "delivered" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestRecoverySendBeforeDeliveredMarksPossibleDuplicate(t *testing.T) {
	// Crash between a successful Finalize and the delivered marker: from the
	// ledger's view this is settled-without-delivered with no preview to
	// resume; the reply repeats with the honest recovered prefix.
	env := newTestEnv(t, nil)
	m := testMessage("ev-sent", "chat-1", "hi")
	seedSettledTurn(t, env, m, "")

	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	delivery := env.adapter.delivery(t, 0)
	if delivery.resumePreviewID != "" {
		t.Fatalf("resumePreviewID = %q, want empty", delivery.resumePreviewID)
	}
	got := delivery.snapshotFinalized()
	if len(got) != 1 || !strings.HasPrefix(got[0], "♻ recovered reply\n\n") {
		t.Fatalf("finalized = %#v, want recovered prefix", got)
	}
	if calls := env.provider.State().CallCount; calls != 0 {
		t.Fatalf("stream calls = %d, want 0", calls)
	}
}

func TestDeliveryFailureReturnsErrorThenRecoversWithoutReprompt(t *testing.T) {
	env := newTestEnv(t, nil)
	env.adapter.prepare = func(d *fauxDelivery) { d.finalizeFails = 1000 }
	env.provider.SetResponses(responsesOf("hard to deliver"))
	m := testMessage("ev-fail", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err == nil {
		t.Fatal("Handle succeeded although every Finalize failed")
	}
	manager := env.sessions.manager(t, m.Key())
	markers := markersFor(t, manager, m.EventID)
	if got := phasesOf(markers); len(got) != 2 || got[1] != "settled" {
		t.Fatalf("phases after failed delivery = %v, want [started settled]", got)
	}

	// Queue redelivery with a healthy adapter: no re-prompt, recovered prefix.
	env.adapter.prepare = nil
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls := env.provider.State().CallCount; calls != 1 {
		t.Fatalf("stream calls = %d, want 1", calls)
	}
	second := env.adapter.delivery(t, 1)
	got := second.snapshotFinalized()
	if len(got) != 1 || got[0] != "♻ recovered reply\n\nhard to deliver" {
		t.Fatalf("recovered finalize = %#v", got)
	}
	markers = markersFor(t, manager, m.EventID)
	if phases := phasesOf(markers); phases[len(phases)-1] != "delivered" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestErrorOutcomeDeliversConciseNotice(t *testing.T) {
	env := newTestEnv(t, nil)
	errText := "429 quota exceeded" // classified non-retryable upstream
	env.provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("", faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &errText}),
	})
	m := testMessage("ev-err", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	delivery := env.adapter.delivery(t, 0)
	notices := delivery.snapshotNotices()
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "turn failed") {
		t.Fatalf("notices = %#v", notices)
	}
	if got := delivery.snapshotFinalized(); len(got) != 0 {
		t.Fatalf("finalized on error outcome = %#v", got)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), m.EventID)
	if markers[1].Outcome != "error" {
		t.Fatalf("settled outcome = %q, want error", markers[1].Outcome)
	}
	if phases := phasesOf(markers); phases[len(phases)-1] != "delivered" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestPreviewRendererAppendsPreviewMarkerOnce(t *testing.T) {
	env := newTestEnv(t, func(o *Options) { o.PreviewInterval = 5 * time.Millisecond },
		faux.Options{TokenSize: faux.FixedTokenSize(4), TokensPerSecond: 100})
	env.provider.SetResponses(responsesOf("a somewhat longer answer that streams in several chunks"))
	m := testMessage("ev-preview", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	delivery := env.adapter.delivery(t, 0)
	previews := delivery.snapshotPreviews()
	if len(previews) == 0 {
		t.Fatal("no preview was rendered")
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), m.EventID)
	previewMarkers := 0
	for _, marker := range markers {
		if marker.Phase == phasePreview {
			previewMarkers++
			if marker.PreviewID != "pv-1" {
				t.Fatalf("preview marker id = %q", marker.PreviewID)
			}
		}
	}
	if previewMarkers != 1 {
		t.Fatalf("preview markers = %d, want exactly 1", previewMarkers)
	}
}

func TestStopPreemptsActiveTurn(t *testing.T) {
	env := newTestEnv(t, nil)
	streaming := make(chan struct{})
	env.provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			close(streaming)
			<-ctx.Done()
			return faux.AssistantMessage("too late"), nil
		}),
	})
	m := testMessage("ev-long", "chat-1", "please think forever")
	turnDone := make(chan error, 1)
	go func() { turnDone <- env.proc.Handle(context.Background(), m) }()
	select {
	case <-streaming:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started streaming")
	}
	stop := testMessage("ev-stop", "chat-1", "/stop")
	if err := env.proc.Handle(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}

	manager := env.sessions.manager(t, m.Key())
	markers := markersFor(t, manager, m.EventID)
	if markers[1].Phase != "settled" || markers[1].Outcome != "aborted" {
		t.Fatalf("settled marker = %+v", markers[1])
	}
	if stopMarkers := markersFor(t, manager, stop.EventID); len(stopMarkers) != 0 {
		t.Fatalf("/stop wrote ledger markers: %v", phasesOf(stopMarkers))
	}
	// Delivery 0 = the turn, delivery 1 = the /stop notice.
	if notices := env.adapter.delivery(t, 1).snapshotNotices(); len(notices) != 1 || notices[0] != "stopped" {
		t.Fatalf("stop notices = %#v", notices)
	}
	if notices := env.adapter.delivery(t, 0).snapshotNotices(); len(notices) != 1 || notices[0] != "turn aborted" {
		t.Fatalf("turn notices = %#v", notices)
	}
}

func TestStopWithoutActiveTurnNotifies(t *testing.T) {
	env := newTestEnv(t, nil)
	if err := env.proc.Handle(context.Background(), testMessage("ev-idle-stop", "chat-1", "/stop")); err != nil {
		t.Fatal(err)
	}
	if notices := env.adapter.delivery(t, 0).snapshotNotices(); len(notices) != 1 || notices[0] != "nothing to stop" {
		t.Fatalf("notices = %#v", notices)
	}
}

func TestStopDuringAttachmentDownloadAbortsTurn(t *testing.T) {
	// /stop landing while buildPrompt is still downloading attachments must
	// abort the turn, not reply "stopped" and then run the turn anyway.
	env := newTestEnv(t, nil)
	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	defer close(gate)
	env.adapter.mu.Lock()
	env.adapter.downloads = map[string]string{"file-1": "img"}
	env.adapter.downloadStarted = started
	env.adapter.downloadGate = gate
	env.adapter.mu.Unlock()
	env.provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return faux.AssistantMessage("ran after stop"), nil
		}),
	})

	m := testMessage("ev-dl", "chat-1", "look at this")
	m.Attachments = []AttachmentRef{{Kind: "photo", ID: "file-1", MIME: "image/png"}}
	turnDone := make(chan error, 1)
	go func() { turnDone <- env.proc.Handle(context.Background(), m) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download never started")
	}

	if err := env.proc.Handle(context.Background(), testMessage("ev-stop-dl", "chat-1", "/stop")); err != nil {
		t.Fatal(err)
	}
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	// Delivery 0 = the turn, delivery 1 = the /stop notice.
	if notices := env.adapter.delivery(t, 1).snapshotNotices(); len(notices) != 1 || notices[0] != "stopped" {
		t.Fatalf("stop notices = %#v", notices)
	}
	turn := env.adapter.delivery(t, 0)
	if got := turn.snapshotFinalized(); len(got) != 0 {
		t.Fatalf("stopped turn still delivered a reply: %#v", got)
	}
	if notices := turn.snapshotNotices(); len(notices) != 1 || notices[0] != "turn aborted" {
		t.Fatalf("turn notices = %#v, want [turn aborted]", notices)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), m.EventID)
	if markers[1].Phase != "settled" || markers[1].Outcome != "aborted" {
		t.Fatalf("settled marker = %+v", markers[1])
	}
}

func TestBusyKeyWaitersDoNotHoldSemaphoreSlots(t *testing.T) {
	// Concurrent deliveries stacked on one hot conversation wait on the keyed
	// lock without pinning MaxConcurrent slots, so other conversations keep
	// flowing.
	env := newTestEnv(t, func(o *Options) { o.MaxConcurrent = 2 })
	streaming := make(chan struct{})
	release := make(chan struct{})
	env.provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
			close(streaming)
			<-release
			return faux.AssistantMessage("slow"), nil
		}),
		faux.AssistantMessage("ok"), faux.AssistantMessage("ok"), faux.AssistantMessage("ok"),
	})

	hotDone := make(chan error, 3)
	go func() { hotDone <- env.proc.Handle(context.Background(), testMessage("ev-hot-0", "hot", "go")) }()
	select {
	case <-streaming:
	case <-time.After(5 * time.Second):
		t.Fatal("hot turn never started streaming")
	}
	for i := 1; i <= 2; i++ {
		go func(i int) {
			hotDone <- env.proc.Handle(context.Background(), testMessage(fmt.Sprintf("ev-hot-%d", i), "hot", "go"))
		}(i)
	}
	// Let the hot waiters reach the keyed lock before probing the cold key.
	time.Sleep(50 * time.Millisecond)

	coldDone := make(chan error, 1)
	go func() { coldDone <- env.proc.Handle(context.Background(), testMessage("ev-cold", "cold", "go")) }()
	select {
	case err := <-coldDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cold conversation starved while hot-key waiters queued")
	}
	close(release)
	for range 3 {
		if err := <-hotDone; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommandsShortCircuitAndDedupe(t *testing.T) {
	env := newTestEnv(t, nil)
	m := testMessage("ev-status", "chat-1", "/status")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	notices := env.adapter.delivery(t, 0).snapshotNotices()
	if len(notices) != 1 || !strings.Contains(notices[0], "model:") || !strings.Contains(notices[0], "cost:") {
		t.Fatalf("/status notice = %#v", notices)
	}
	if calls := env.provider.State().CallCount; calls != 0 {
		t.Fatalf("/status prompted the model (%d calls)", calls)
	}
	manager := env.sessions.manager(t, m.Key())
	if got := phasesOf(markersFor(t, manager, m.EventID)); len(got) != 2 || got[0] != "started" || got[1] != "delivered" {
		t.Fatalf("command phases = %v", got)
	}
	// Redelivery of the command is a no-op.
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if count := env.adapter.deliveryCount(); count != 1 {
		t.Fatalf("deliveries after duplicate command = %d, want 1", count)
	}

	compact := testMessage("ev-compact", "chat-1", "/compact")
	if err := env.proc.Handle(context.Background(), compact); err != nil {
		t.Fatal(err)
	}
	compactNotices := env.adapter.delivery(t, 1).snapshotNotices()
	if len(compactNotices) != 1 || !strings.Contains(compactNotices[0], "compact") {
		t.Fatalf("/compact notice = %#v", compactNotices)
	}
}

func TestNewCommandResetsSession(t *testing.T) {
	env := newTestEnv(t, nil)
	env.provider.SetResponses(responsesOf("first answer"))
	m := testMessage("ev-before-new", "chat-1", "hi")
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	manager := env.sessions.manager(t, m.Key())
	previousSession := manager.GetSessionID()

	newCmd := testMessage("ev-new", "chat-1", "/new")
	if err := env.proc.Handle(context.Background(), newCmd); err != nil {
		t.Fatal(err)
	}
	if notices := env.adapter.delivery(t, 1).snapshotNotices(); len(notices) != 1 || notices[0] != "started a new session" {
		t.Fatalf("/new notices = %#v", notices)
	}
	if manager.GetSessionID() == previousSession {
		t.Fatal("session id unchanged after /new")
	}
	// The fresh session holds only ledger markers: the carried delivered
	// marker of the pre-/new turn plus the /new delivered marker.
	entries := manager.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("fresh session entries = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Type != "custom" || entry.CustomType != turnCustomType {
			t.Fatalf("non-marker entry leaked into the fresh session: %+v", entry)
		}
	}

	// A late redelivery of the pre-/new event hits the carried delivered
	// marker: no re-prompt, no new delivery.
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls := env.provider.State().CallCount; calls != 1 {
		t.Fatalf("stream calls after redelivery across /new = %d, want 1", calls)
	}
	if count := env.adapter.deliveryCount(); count != 2 {
		t.Fatalf("deliveries after redelivery across /new = %d, want 2", count)
	}
}

func TestRecoverySettledSurvivesNewSession(t *testing.T) {
	// A turn settles but the queue ack is lost; the user runs /new; the queue
	// then redelivers the settled event. The carried settled marker must keep
	// the never-re-prompt guarantee and still deliver the recorded reply.
	env := newTestEnv(t, nil)
	m := testMessage("ev-settled-across-new", "chat-1", "hi")
	seedSettledTurn(t, env, m, "pv-3")

	if err := env.proc.Handle(context.Background(), testMessage("ev-new-2", "chat-1", "/new")); err != nil {
		t.Fatal(err)
	}
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls := env.provider.State().CallCount; calls != 0 {
		t.Fatalf("stream calls = %d, want 0 (settled turns must not re-prompt across /new)", calls)
	}
	delivery := env.adapter.delivery(t, 1)
	if delivery.resumePreviewID != "pv-3" {
		t.Fatalf("resumePreviewID = %q, want pv-3 (carried preview marker)", delivery.resumePreviewID)
	}
	want := "♻ recovered reply\n\nthe settled answer"
	if got := delivery.snapshotFinalized(); len(got) != 1 || got[0] != want {
		t.Fatalf("finalized = %#v, want %q", got, want)
	}
	markers := markersFor(t, env.sessions.manager(t, m.Key()), m.EventID)
	if phases := phasesOf(markers); phases[len(phases)-1] != "delivered" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestGroupAttributionAndAttachments(t *testing.T) {
	env := newTestEnv(t, nil)
	env.adapter.downloads = map[string]string{"file-1": "img-bytes"}
	env.provider.SetResponses(responsesOf("noted"))
	m := testMessage("ev-group", "group-9", "look at this")
	m.ChatType = "group"
	m.SenderID = "user-7"
	m.SenderName = "Alice"
	m.Attachments = []AttachmentRef{
		{Kind: "photo", ID: "file-1", MIME: "image/png"},
		{Kind: "document", ID: "file-2", Name: "spec.pdf", MIME: "application/pdf"},
	}
	if err := env.proc.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	manager := env.sessions.manager(t, m.Key())
	var userRaw string
	for _, entry := range manager.GetBranch() {
		if entry.Type == "message" && strings.Contains(string(entry.Message), `"role":"user"`) {
			userRaw = string(entry.Message)
		}
	}
	if !strings.Contains(userRaw, "Alice: look at this") {
		t.Fatalf("group attribution missing from user message: %s", userRaw)
	}
	if !strings.Contains(userRaw, base64.StdEncoding.EncodeToString([]byte("img-bytes"))) {
		t.Fatalf("photo attachment not inlined as image content: %s", userRaw)
	}
	if !strings.Contains(userRaw, "[attachment: document spec.pdf (application/pdf)]") {
		t.Fatalf("document note missing: %s", userRaw)
	}
}

func TestAttributionKeysOffChatType(t *testing.T) {
	env := newTestEnv(t, nil)
	m := testMessage("ev-attr", "chat-1", "hello")
	m.SenderName = "Alice"

	// Distinct sender and chat IDs alone (the old heuristic) do not attribute.
	m.SenderID = "user-7"
	if text, _ := env.proc.buildPrompt(context.Background(), env.adapter, m); text != "hello" {
		t.Fatalf("non-group prompt = %q, want no attribution", text)
	}

	// ChatType "group" attributes even when SenderID equals ChatID.
	m.SenderID = m.ChatID
	m.ChatType = "group"
	if text, _ := env.proc.buildPrompt(context.Background(), env.adapter, m); text != "Alice: hello" {
		t.Fatalf("group prompt = %q, want sender attribution", text)
	}
}

func TestUnauthorizedMessageRejected(t *testing.T) {
	env := newTestEnv(t, func(o *Options) {
		o.Authorize = func(m Message) error {
			if m.SenderID == "banned" {
				return fmt.Errorf("sender banned")
			}
			return nil
		}
	})
	m := testMessage("ev-banned", "chat-1", "hi")
	m.SenderID = "banned"
	if err := env.proc.Handle(context.Background(), m); err == nil {
		t.Fatal("banned sender was handled")
	}
	if count := env.adapter.deliveryCount(); count != 0 {
		t.Fatalf("deliveries for unauthorized message = %d", count)
	}
}

func TestParseCommandWhitespace(t *testing.T) {
	if got := parseCommand("\u2003/status\textra"); got != "/status" {
		t.Fatalf("parseCommand = %q", got)
	}
}

func TestConcurrentTurnsOverManyKeys(t *testing.T) {
	const keys = 100
	const perKey = 10
	env := newTestEnv(t, nil)
	steps := make([]faux.ResponseStep, 0, keys*perKey)
	for range keys * perKey {
		steps = append(steps, faux.AssistantMessage("ok"))
	}
	env.provider.SetResponses(steps)

	var wg sync.WaitGroup
	errs := make(chan error, keys*perKey)
	for k := range keys {
		for i := range perKey {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := testMessage(fmt.Sprintf("ev-%d-%d", k, i), fmt.Sprintf("chat-%d", k), "go")
				errs <- env.proc.Handle(context.Background(), m)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls := env.provider.State().CallCount; calls != keys*perKey {
		t.Fatalf("stream calls = %d, want %d", calls, keys*perKey)
	}
	if size := env.proc.locks.size(); size != 0 {
		t.Fatalf("keyed mutex residue: %d entries", size)
	}
	env.proc.activeMu.Lock()
	activeLen := len(env.proc.active)
	env.proc.activeMu.Unlock()
	if activeLen != 0 {
		t.Fatalf("active-turn registry residue: %d entries", activeLen)
	}
}

func TestIdleResidueIsZero(t *testing.T) {
	env := newTestEnv(t, nil)
	baseline := goruntime.NumGoroutine()
	env.provider.SetResponses(responsesOf("a", "b", "c", "d", "e", "f", "g", "h"))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := testMessage(fmt.Sprintf("ev-idle-%d", i), fmt.Sprintf("chat-%d", i%3), "hi")
			if err := env.proc.Handle(context.Background(), m); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if size := env.proc.locks.size(); size != 0 {
		t.Fatalf("keyed mutex map not empty: %d", size)
	}
	env.proc.activeMu.Lock()
	activeLen := len(env.proc.active)
	env.proc.activeMu.Unlock()
	if activeLen != 0 {
		t.Fatalf("active registry not empty: %d", activeLen)
	}
	waitUntil(t, 2*time.Second, "goroutine count to return to baseline", func() bool {
		return goruntime.NumGoroutine() <= baseline
	})
	if err := env.proc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForInFlightAndRejectsNew(t *testing.T) {
	env := newTestEnv(t, nil)
	streaming := make(chan struct{})
	release := make(chan struct{})
	env.provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
			close(streaming)
			<-release
			return faux.AssistantMessage("done"), nil
		}),
	})
	turnDone := make(chan error, 1)
	go func() { turnDone <- env.proc.Handle(context.Background(), testMessage("ev-close", "chat-1", "hi")) }()
	<-streaming

	closeDone := make(chan error, 1)
	go func() { closeDone <- env.proc.Close(context.Background()) }()
	waitUntil(t, time.Second, "processor to mark closed", func() bool {
		env.proc.closeMu.Lock()
		defer env.proc.closeMu.Unlock()
		return env.proc.closed
	})
	if err := env.proc.Handle(context.Background(), testMessage("ev-late", "chat-1", "hi")); err == nil {
		t.Fatal("closed processor accepted a message")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before in-flight turn finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestKeyedMutexRefcountsAndHonorsContext(t *testing.T) {
	locks := newKeyedMutex()
	if err := locks.Lock(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if size := locks.size(); size != 1 {
		t.Fatalf("size = %d", size)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() { waitErr <- locks.Lock(ctx, "k") }()
	cancel()
	if err := <-waitErr; err == nil {
		t.Fatal("cancelled waiter acquired the lock")
	}
	locks.Unlock("k")
	if size := locks.size(); size != 0 {
		t.Fatalf("residue after unlock: %d entries", size)
	}
}

func TestAdapterRoutingByAccount(t *testing.T) {
	specific := &fauxAdapter{account: "bot"}
	wildcard := &fauxAdapter{}
	env := newTestEnv(t, func(o *Options) {
		o.Adapters = []Adapter{specific, wildcard}
	})
	env.provider.SetResponses(responsesOf("for bot", "for wildcard"))

	if err := env.proc.Handle(context.Background(), testMessage("ev-acct-1", "chat-a", "hi")); err != nil {
		t.Fatal(err) // testMessage carries Account "bot" → the specific adapter
	}
	stranger := testMessage("ev-acct-2", "chat-b", "hi")
	stranger.Account = "other"
	if err := env.proc.Handle(context.Background(), stranger); err != nil {
		t.Fatal(err) // unclaimed account → the platform wildcard
	}
	if got := specific.deliveryCount(); got != 1 {
		t.Fatalf("specific adapter deliveries = %d, want 1", got)
	}
	if got := wildcard.deliveryCount(); got != 1 {
		t.Fatalf("wildcard adapter deliveries = %d, want 1", got)
	}
	if got := specific.delivery(t, 0).snapshotFinalized(); len(got) != 1 || got[0] != "for bot" {
		t.Fatalf("specific finalized = %#v", got)
	}
}

func TestHandleRejectsUnclaimedAccountWithoutWildcard(t *testing.T) {
	env := newTestEnv(t, func(o *Options) {
		o.Adapters = []Adapter{&fauxAdapter{account: "bot"}}
	})
	m := testMessage("ev-acct-3", "chat-c", "hi")
	m.Account = "stranger"
	if err := env.proc.Handle(context.Background(), m); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestNewRejectsDuplicatePlatformAccountPairs(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	_, err := New(Options{
		Sessions:  newFauxSessions(t, provider),
		Adapters:  []Adapter{&fauxAdapter{account: "x"}, &fauxAdapter{account: "x"}},
		Authorize: AllowAll,
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate adapter") {
		t.Fatalf("err = %v, want duplicate adapter error", err)
	}
}
