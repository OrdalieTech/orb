// Package slack implements the Slack adapter for the pigo chat gateway:
// Events API ingress over HTTP (v0 request signing, url_verification
// handshake, echo filtering), Web API delivery with a bot token (streamed
// preview edits via chat.update, mrkdwn finalization with chunking), and
// authenticated file download.
//
// The adapter speaks the Events API webhook, not Socket Mode, so it needs a
// public endpoint plus the app's signing secret. Slack has no typing
// indicator for bot messages: the streamed preview message is the activity
// signal and Typing is a no-op.
package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

const platformName = "slack"

// Options configures [New].
type Options struct {
	// Token is the bot token (xoxb-...). Required.
	Token string
	// SigningSecret verifies inbound Events API requests (v0 signature).
	// Required: unsigned webhooks are not an option.
	SigningSecret string
	// BaseURL overrides the Web API endpoint (tests). Default
	// https://slack.com.
	BaseURL string
	// BotUserID pre-seeds the bot user identity used for echo filtering and
	// mention gating; resolved via auth.test in [New] when empty.
	BotUserID string
	// HTTPClient overrides the HTTP client. Default: 30s timeout.
	HTTPClient *http.Client
	// PreviewMinInterval rate-limits chat.update preview edits per channel.
	// Default 1s (chat.update is Tier 3, ~1/sec sustained).
	PreviewMinInterval time.Duration
	// ReplayWindow bounds |now - X-Slack-Request-Timestamp| on inbound
	// events. Default 5m (Slack's documented replay window).
	ReplayWindow time.Duration
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
}

// Adapter is the Slack implementation of [chat.Adapter].
type Adapter struct {
	client   *client
	download *http.Client
	logger   *slog.Logger

	signingSecret      string
	botUserID          string
	mentionRe          *regexp.Regexp
	previewMinInterval time.Duration
	replayWindow       time.Duration

	// now is the clock used by the replay-window check; a seam for tests.
	now func() time.Time
}

var _ chat.Adapter = (*Adapter)(nil)

// New builds the adapter. It refuses to construct without Token and
// SigningSecret. When Options.BotUserID is empty, the bot identity is
// resolved (and cached for the adapter's lifetime) via one auth.test call —
// the id drives echo filtering, so the adapter cannot run without it.
func New(opts Options) (*Adapter, error) {
	switch {
	case opts.Token == "":
		return nil, errors.New("slack: Options.Token is required")
	case opts.SigningSecret == "":
		return nil, errors.New("slack: Options.SigningSecret is required (unsigned webhooks are not supported)")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.PreviewMinInterval <= 0 {
		opts.PreviewMinInterval = time.Second
	}
	if opts.ReplayWindow <= 0 {
		opts.ReplayWindow = 5 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	c := &client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		token:   opts.Token,
		http:    httpClient,
		sleep:   sleepContext,
	}
	if opts.BotUserID == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		identity, err := c.authTest(ctx)
		if err != nil {
			return nil, fmt.Errorf("slack: resolve bot identity: %w", err)
		}
		if identity.UserID == "" {
			return nil, errors.New("slack: auth.test returned no user_id")
		}
		opts.BotUserID = identity.UserID
	}
	return &Adapter{
		client:             c,
		download:           downloadClient(httpClient, opts.Token),
		logger:             opts.Logger,
		signingSecret:      opts.SigningSecret,
		botUserID:          opts.BotUserID,
		mentionRe:          regexp.MustCompile(`<@` + regexp.QuoteMeta(opts.BotUserID) + `(?:\|[^>]*)?> ?`),
		previewMinInterval: opts.PreviewMinInterval,
		replayWindow:       opts.ReplayWindow,
		now:                time.Now,
	}, nil
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return platformName }

// Account returns the bot user id (auth.test user_id), matching the Account
// set on every normalized message.
func (a *Adapter) Account() string { return a.botUserID }

func downloadClient(base *http.Client, token string) *http.Client {
	return &http.Client{
		Transport: base.Transport,
		Timeout:   base.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("slack: too many redirects downloading file")
			}
			if trustedSlackHost(req.URL.Hostname()) {
				req.Header.Set("Authorization", "Bearer "+token)
			} else {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func trustedSlackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "slack.com" || strings.HasSuffix(host, ".slack.com")
}

// Download implements [chat.Adapter]: the attachment id is the file's
// url_private_download, fetched with the Bearer token (kept across the
// slack.com → files.slack.com redirect). The MIME type is the one carried on
// the ref (the download response is generic).
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(ref.ID, "https://") && !strings.HasPrefix(ref.ID, "http://") {
		return nil, "", fmt.Errorf("slack: attachment id is not a download url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ID, nil)
	if err != nil {
		return nil, "", fmt.Errorf("slack: build download request: %w", err)
	}
	if trustedSlackHost(req.URL.Hostname()) {
		req.Header.Set("Authorization", "Bearer "+a.client.token)
	}
	resp, err := a.download.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("slack: download file: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("slack: file download returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, ref.MIME, nil
}
