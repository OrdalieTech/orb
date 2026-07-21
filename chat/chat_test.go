package chat

// Shared faux test infrastructure: an adapter recording every call, and a
// session provider building real session runtimes over a scripted faux
// stream backend (no network, deterministic).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

// fauxDelivery records every Delivery call.
type fauxDelivery struct {
	mu              sync.Mutex
	key             ConversationKey
	replyTo         string
	resumePreviewID string
	previewID       string
	typings         int
	previews        []string
	finalized       []string
	notices         []string
	finalizeFails   int
	notifyFails     int
	previewPanics   int
}

func (d *fauxDelivery) Typing(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.typings++
	return nil
}

func (d *fauxDelivery) Preview(_ context.Context, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.previewPanics > 0 {
		d.previewPanics--
		panic("preview panic")
	}
	d.previews = append(d.previews, text)
	if d.previewID == "" {
		d.previewID = "pv-1"
	}
	return nil
}

func (d *fauxDelivery) PreviewID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.previewID
}

func (d *fauxDelivery) Finalize(_ context.Context, text string) (Receipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finalizeFails > 0 {
		d.finalizeFails--
		return Receipt{}, fmt.Errorf("finalize refused")
	}
	d.finalized = append(d.finalized, text)
	return Receipt{MessageIDs: []string{"fin-1"}, At: time.Now().UTC()}, nil
}

func (d *fauxDelivery) Notify(_ context.Context, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.notifyFails > 0 {
		d.notifyFails--
		return fmt.Errorf("notify refused")
	}
	d.notices = append(d.notices, text)
	return nil
}

func (d *fauxDelivery) snapshotFinalized() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.finalized...)
}

func (d *fauxDelivery) snapshotNotices() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.notices...)
}

func (d *fauxDelivery) snapshotPreviews() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.previews...)
}

// fauxAdapter records every created delivery.
type fauxAdapter struct {
	// account is the identity reported to the processor; "" registers the
	// adapter as its platform's wildcard.
	account    string
	mu         sync.Mutex
	deliveries []*fauxDelivery
	// prepare tweaks each new delivery before it is returned.
	prepare func(*fauxDelivery)
	// downloads maps attachment id to content; missing ids fail.
	downloads map[string]string
	// downloadStarted, when set, receives one signal per Download call.
	downloadStarted chan struct{}
	// downloadGate, when set, blocks Download until closed or ctx ends.
	downloadGate chan struct{}
}

func (a *fauxAdapter) Platform() string { return "faux" }

func (a *fauxAdapter) Account() string { return a.account }

func (a *fauxAdapter) NewDelivery(key ConversationKey, replyTo, resumePreviewID string) Delivery {
	delivery := &fauxDelivery{key: key, replyTo: replyTo, resumePreviewID: resumePreviewID}
	a.mu.Lock()
	prepare := a.prepare
	a.mu.Unlock()
	if prepare != nil {
		prepare(delivery)
	}
	a.mu.Lock()
	a.deliveries = append(a.deliveries, delivery)
	a.mu.Unlock()
	return delivery
}

func (a *fauxAdapter) Download(ctx context.Context, ref AttachmentRef) (io.ReadCloser, string, error) {
	a.mu.Lock()
	content, ok := a.downloads[ref.ID]
	started, gate := a.downloadStarted, a.downloadGate
	a.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if !ok {
		return nil, "", fmt.Errorf("no such attachment %q", ref.ID)
	}
	return io.NopCloser(bytes.NewReader([]byte(content))), ref.MIME, nil
}

func (a *fauxAdapter) delivery(t *testing.T, index int) *fauxDelivery {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if index >= len(a.deliveries) {
		t.Fatalf("delivery %d not created (have %d)", index, len(a.deliveries))
	}
	return a.deliveries[index]
}

func (a *fauxAdapter) deliveryCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.deliveries)
}

// fauxSessions is an in-memory SessionProvider whose runtimes stream through
// a scripted faux provider via NewSessionRuntime.
type fauxSessions struct {
	provider *faux.Provider
	settings *config.SettingsManager
	cwd      string

	mu       sync.Mutex
	managers map[string]*sessionstore.SessionManager
}

func newFauxSessions(t *testing.T, provider *faux.Provider) *fauxSessions {
	t.Helper()
	agentDir := t.TempDir()
	settingsJSON := `{"retry":{"enabled":false},"compaction":{"enabled":false}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	return &fauxSessions{
		provider: provider,
		settings: settings,
		cwd:      cwd,
		managers: map[string]*sessionstore.SessionManager{},
	}
}

// manager returns the durable per-key store, creating it on first use. Tests
// use it to pre-seed crash states.
func (s *fauxSessions) manager(t *testing.T, key ConversationKey) *sessionstore.SessionManager {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	manager, ok := s.managers[key.String()]
	if !ok {
		var err error
		manager, err = sessionstore.InMemory(s.cwd)
		if err != nil {
			t.Fatal(err)
		}
		s.managers[key.String()] = manager
	}
	return manager
}

func (s *fauxSessions) Acquire(_ context.Context, key ConversationKey) (*Conversation, error) {
	s.mu.Lock()
	manager, ok := s.managers[key.String()]
	if !ok {
		var err error
		manager, err = sessionstore.InMemory(s.cwd)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.managers[key.String()] = manager
	}
	s.mu.Unlock()

	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{SystemPrompt: "test", Model: s.provider.GetModel()}),
		agent.WithStreamFn(s.provider.StreamSimple),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
	)
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent:          created,
		SessionManager: manager,
		Settings:       s.settings,
		StreamFn:       s.provider.StreamSimple,
	})
	if err != nil {
		return nil, err
	}
	runtime.SyncMessagesFromSession()
	return &Conversation{
		Session: runtime,
		Manager: manager,
		Close: func(context.Context) error {
			runtime.Dispose()
			return nil
		},
	}, nil
}

// testEnv wires a Processor over the faux adapter + faux sessions.
type testEnv struct {
	provider *faux.Provider
	sessions *fauxSessions
	adapter  *fauxAdapter
	proc     *Processor
}

func newTestEnv(t *testing.T, tweak func(*Options), fauxOptions ...faux.Options) *testEnv {
	t.Helper()
	options := faux.Options{TokenSize: faux.FixedTokenSize(1000)}
	if len(fauxOptions) > 0 {
		options = fauxOptions[0]
	}
	provider := faux.New(options)
	sessions := newFauxSessions(t, provider)
	adapter := &fauxAdapter{}
	opts := Options{
		Sessions:        sessions,
		Adapters:        []Adapter{adapter},
		Authorize:       AllowAll,
		PreviewInterval: time.Hour, // renderer stays quiet unless a test opts in
		TurnTimeout:     time.Minute,
	}
	if tweak != nil {
		tweak(&opts)
	}
	proc, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{provider: provider, sessions: sessions, adapter: adapter, proc: proc}
}

func testMessage(eventID, chatID, text string) Message {
	return Message{
		EventID:  eventID,
		Platform: "faux",
		Account:  "bot",
		ChatID:   chatID,
		SenderID: chatID,
		Text:     text,
		SentAt:   time.Unix(1700000000, 0).UTC(),
	}
}

// markersFor scans raw entries and returns every ledger marker for eventID in
// append order.
func markersFor(t *testing.T, manager *sessionstore.SessionManager, eventID string) []turnMarker {
	t.Helper()
	var markers []turnMarker
	for _, entry := range manager.GetEntries() {
		if entry.Type != "custom" || entry.CustomType != turnCustomType {
			continue
		}
		var marker turnMarker
		if err := json.Unmarshal(entry.Data, &marker); err != nil {
			t.Fatal(err)
		}
		if marker.EventID == eventID {
			markers = append(markers, marker)
		}
	}
	return markers
}

func phasesOf(markers []turnMarker) []string {
	phases := make([]string, len(markers))
	for i, marker := range markers {
		phases[i] = marker.Phase
	}
	return phases
}

func waitUntil(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func mustAppendMarker(t *testing.T, manager *sessionstore.SessionManager, marker turnMarker) string {
	t.Helper()
	id, err := appendTurnMarker(manager, marker)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func responsesOf(texts ...string) []faux.ResponseStep {
	steps := make([]faux.ResponseStep, len(texts))
	for i, text := range texts {
		steps[i] = faux.AssistantMessage(text)
	}
	return steps
}
