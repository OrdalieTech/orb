// Package discord implements the Discord adapter for the pigo chat
// gateway: Gateway (WebSocket) ingress over the internal RFC 6455 client —
// hello/heartbeat/identify, READY capture, resume-first reconnects — plus
// REST delivery with a refreshed typing indicator, streamed preview edits,
// chunked finalization with mass-mention suppression, and pre-signed
// attachment download. It plugs into the chat processor via [chat.Adapter];
// ingress runs through [Adapter.Run].
package discord

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// DefaultBaseURL is the production REST API endpoint.
const DefaultBaseURL = "https://discord.com/api/v10"

const platformName = "discord"

// Options configures [New].
type Options struct {
	// Token is the bot token from the Developer Portal. Required.
	Token string
	// BaseURL overrides the REST endpoint (tests). Default [DefaultBaseURL].
	BaseURL string
	// BotUserID pre-seeds the bot identity used for [Adapter.Account], echo
	// filtering, and mention gating. When empty it is derived from the bot
	// id embedded in the token and confirmed by the gateway READY event.
	BotUserID string
	// HTTPClient overrides the REST client (tests). Default: 30s timeout.
	HTTPClient *http.Client
	// TypingInterval is the typing-indicator refresh cadence (the indicator
	// expires after ~10s). Default 4s.
	TypingInterval time.Duration
	// PreviewMinInterval rate-limits preview edits per delivery. Default
	// 1.5s (safely under the ~5/5s per-channel message bucket).
	PreviewMinInterval time.Duration
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
}

// Adapter is the Discord implementation of [chat.Adapter].
type Adapter struct {
	client  *restClient
	account string
	logger  *slog.Logger

	typingInterval     time.Duration
	previewMinInterval time.Duration
	gatewayDialTimeout time.Duration

	// sleep and jitter are seams for tests: sleep paces reconnect backoff,
	// invalid-session waits, and identify-budget waits; jitter feeds the
	// first-heartbeat delay and the invalid-session wait.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64

	identityMu sync.Mutex
	botUserID  string
}

var _ chat.Adapter = (*Adapter)(nil)

// New creates a Discord adapter. It performs no network calls; the gateway
// session starts with [Adapter.Run].
func New(opts Options) (*Adapter, error) {
	if opts.Token == "" {
		return nil, errors.New("discord: Options.Token is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.TypingInterval <= 0 {
		opts.TypingInterval = 4 * time.Second
	}
	if opts.PreviewMinInterval <= 0 {
		opts.PreviewMinInterval = 1500 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	account := opts.BotUserID
	if account == "" {
		account = accountFromToken(opts.Token)
	}
	return &Adapter{
		client: &restClient{
			baseURL: strings.TrimRight(opts.BaseURL, "/"),
			token:   opts.Token,
			http:    httpClient,
			sleep:   sleepContext,
		},
		account:            account,
		logger:             opts.Logger,
		typingInterval:     opts.TypingInterval,
		previewMinInterval: opts.PreviewMinInterval,
		gatewayDialTimeout: 30 * time.Second,
		sleep:              sleepContext,
		jitter:             rand.Float64,
		botUserID:          account,
	}, nil
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return platformName }

// Account returns the bot user id this adapter serves, matching the Account
// set on every normalized message. It is fixed at construction — from
// Options.BotUserID or the bot id embedded in the token — because the
// processor registers it before the gateway connects; the READY event
// confirms it and logs a mismatch.
func (a *Adapter) Account() string { return a.account }

// Download implements [chat.Adapter]. ref.ID is the pre-signed CDN URL of
// the inbound attachment, fetched with a plain GET — signed links reject
// Authorization headers.
//
// ponytail: signed URLs expire (~24h); turns run promptly after ingress so
// the link is fresh — a much later replay 404s instead of re-resolving a
// fresh signed URL through the message endpoint.
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(ref.ID, "https://") && !strings.HasPrefix(ref.ID, "http://") {
		return nil, "", fmt.Errorf("discord: attachment %q: ref is not a download URL", ref.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ID, nil)
	if err != nil {
		return nil, "", fmt.Errorf("discord: download attachment %q: %w", ref.Name, err)
	}
	resp, err := a.client.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("discord: download attachment %q: %w", ref.Name, a.client.redact(err))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("discord: download attachment %q: http %d (signed attachment links expire)",
			ref.Name, resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = ref.MIME
	}
	return resp.Body, mime, nil
}

func (a *Adapter) setIdentity(userID string) {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	if a.botUserID != "" && userID != "" && a.botUserID != userID {
		a.logger.Warn("discord: READY bot user id differs from configured account",
			"configured", a.botUserID, "ready", userID)
	}
	if userID != "" {
		a.botUserID = userID
	}
}

func (a *Adapter) identity() string {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	return a.botUserID
}

// Discord exposes the public bot id in the token's first segment; the secret
// segments are never decoded or logged.
func accountFromToken(token string) string {
	first, _, ok := strings.Cut(token, ".")
	if !ok {
		return ""
	}
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding} {
		decoded, err := enc.DecodeString(first)
		if err != nil {
			continue
		}
		id := string(decoded)
		if id != "" && strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return id
		}
	}
	return ""
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
