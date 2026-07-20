// Package messenger implements the Facebook Messenger (Meta Messenger
// Platform for Pages) adapter for the pi-go chat gateway: webhook ingress
// over the Graph "page" object (hub handshake + HMAC-signed
// entry[].messaging[] events), final-message delivery via the Send API,
// sender-action typing, and direct-URL attachment download.
//
// The adapter speaks only the official Graph API with caller-supplied
// credentials. Unsigned webhooks are refused by construction: [New] errors
// without an AppSecret. Messenger has no message editing, so Preview is a
// no-op and only the final reply is sent (no fake streaming); typing is
// sustained with re-fired typing_on sender actions instead.
//
// Messenger is 1:1 only — every conversation is a DM, and PSIDs are scoped
// per page: the conversation key is the (page id, PSID) pair carried as
// Account + ChatID.
package messenger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// GraphVersion is the pinned Graph API version used for every call.
const GraphVersion = "v23.0"

const platformName = "messenger"

const maxResponseBytes = 4 << 20

// Options configures the Messenger adapter.
type Options struct {
	// Token is the long-lived Page Access Token (Bearer). Required.
	Token string
	// PageID is the Facebook Page the token belongs to. It becomes the
	// adapter account identity; PSIDs are scoped to it. Required.
	PageID string
	// AppSecret signs inbound webhooks (X-Hub-Signature-256). Required:
	// unsigned webhooks are not an option.
	AppSecret string
	// VerifyToken answers the one-time GET subscribe handshake. Required.
	VerifyToken string
	// BaseURL overrides the Graph endpoint (tests). Default
	// https://graph.facebook.com.
	BaseURL string
	// HTTPClient overrides the HTTP client. Default: 30s timeout.
	HTTPClient *http.Client
	// OnWatermark receives delivery/read watermark callbacks from the
	// message_deliveries and message_reads webhook fields. Default: no-op.
	// Watermarks are telemetry, not per-message state: everything the page
	// sent before the watermark timestamp is delivered/read.
	OnWatermark func(Watermark)
	// TypingInterval is the typing_on re-fire cadence while a turn is
	// generating (the indicator auto-expires after ~20s). Default 15s.
	TypingInterval time.Duration
	// Logger receives non-fatal diagnostics (failed sender actions).
	// Optional.
	Logger *slog.Logger
}

// Adapter is a chat.Adapter for the Messenger Platform Send API.
type Adapter struct {
	opts    Options
	client  *http.Client
	baseURL string
	logger  *slog.Logger

	// maxAttempts and backoff govern retries of retryable Graph errors
	// (613/4/32/80006 throttling, 1200 transient failure); tests shrink the
	// backoff.
	maxAttempts int
	backoff     func(attempt int) time.Duration
	// maxRetryWait caps how long one retry may wait. A throttle whose
	// X-Business-Use-Case-Usage regain hint exceeds it surfaces immediately.
	maxRetryWait time.Duration

	typingInterval time.Duration
}

var _ chat.Adapter = (*Adapter)(nil)

// New builds the adapter. It refuses to construct without Token, PageID,
// AppSecret, and VerifyToken.
//
// Webhook events only flow after a one-time subscription BOTH ways: the app
// subscribes to the "page" webhook object (fields: messages,
// messaging_postbacks, message_deliveries, message_reads, message_echoes) in
// the app dashboard, AND the page must be subscribed to the app via
//
//	POST /{page-id}/subscribed_apps?subscribed_fields=messages,messaging_postbacks,message_deliveries,message_reads,message_echoes&access_token=<PAGE_TOKEN>
//
// which returns {"success":true}. A missing page subscription is the usual
// cause of a silently dead webhook.
func New(opts Options) (*Adapter, error) {
	switch {
	case opts.Token == "":
		return nil, fmt.Errorf("messenger: Options.Token is required")
	case opts.PageID == "":
		return nil, fmt.Errorf("messenger: Options.PageID is required")
	case opts.AppSecret == "":
		return nil, fmt.Errorf("messenger: Options.AppSecret is required (unsigned webhooks are not supported)")
	case opts.VerifyToken == "":
		return nil, fmt.Errorf("messenger: Options.VerifyToken is required")
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = "https://graph.facebook.com"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	typingInterval := opts.TypingInterval
	if typingInterval <= 0 {
		typingInterval = 15 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		opts:        opts,
		client:      client,
		baseURL:     baseURL,
		logger:      logger,
		maxAttempts: 3,
		backoff: func(attempt int) time.Duration {
			return time.Duration(1<<attempt) * time.Second
		},
		maxRetryWait:   60 * time.Second,
		typingInterval: typingInterval,
	}, nil
}

// Platform implements chat.Adapter.
func (a *Adapter) Platform() string { return platformName }

// Account returns the page id this adapter serves, matching the Account set
// on every normalized message. PSIDs are page-scoped, so the (page id, PSID)
// pair — Account + ChatID — is the conversation identity.
func (a *Adapter) Account() string { return a.opts.PageID }

func (a *Adapter) sendPath() string {
	return "/" + GraphVersion + "/me/messages"
}

func (a *Adapter) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("messenger: encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("messenger: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.opts.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("messenger: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("messenger: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphErrorFrom(resp.StatusCode, data, resp.Header.Get("X-Business-Use-Case-Usage"))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("messenger: decode response: %w", err)
		}
	}
	return nil
}

// GraphError is one Graph API error object from an HTTP error response
// envelope.
type GraphError struct {
	Code      int    `json:"code"`
	Subcode   int    `json:"error_subcode,omitempty"`
	Type      string `json:"type,omitempty"`
	Message   string `json:"message,omitempty"`
	FBTraceID string `json:"fbtrace_id,omitempty"`
	// RegainAfter is the throttle-recovery hint parsed from the
	// X-Business-Use-Case-Usage response header
	// (estimated_time_to_regain_access, reported in minutes); zero when the
	// header is absent or carries no hint.
	RegainAfter time.Duration `json:"-"`
}

// Error implements error.
func (e *GraphError) Error() string {
	msg := fmt.Sprintf("messenger: graph error %d", e.Code)
	if e.Subcode != 0 {
		msg += fmt.Sprintf("/%d", e.Subcode)
	}
	if hint := errorHint(e.Code, e.Subcode); hint != "" {
		msg += " (" + hint + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func errorHint(code, subcode int) string {
	switch code {
	case 10:
		switch subcode {
		case 2018278:
			return "outside the 24-hour messaging window; the user must message the page first"
		case 2018065, 2018108:
			return "user opted out or cannot receive messages; conversation is dead"
		}
		return "message not allowed by messaging policy"
	case 551:
		return "person unavailable (blocked the page or deactivated); conversation is permanently dead"
	case 190:
		return "page access token expired or invalid"
	case 100:
		if subcode == 2018001 {
			return "no matching user found; the PSID likely belongs to another page"
		}
		return "invalid parameter"
	case 200:
		return "permission missing (check pages_messaging / human_agent approval)"
	case 613, 4, 32, 80006:
		return "rate limited; backing off"
	case 1200:
		return "temporary send failure"
	}
	return ""
}

func retryable(code int) bool {
	switch code {
	case 613, 4, 32, 80006, 1200:
		return true
	}
	return false
}

func graphErrorFrom(status int, body []byte, usageHeader string) error {
	var envelope struct {
		Error GraphError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != 0 {
		envelope.Error.RegainAfter = regainAfter(usageHeader)
		return &envelope.Error
	}
	const maxSnippet = 256
	snippet := string(body)
	if len(snippet) > maxSnippet {
		snippet = snippet[:maxSnippet]
	}
	return fmt.Errorf("messenger: graph returned HTTP %d: %s", status, snippet)
}

func regainAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	var usage map[string][]struct {
		EstimatedTimeToRegainAccess float64 `json:"estimated_time_to_regain_access"`
	}
	if err := json.Unmarshal([]byte(header), &usage); err != nil {
		return 0
	}
	var minutes float64
	for _, entries := range usage {
		for _, entry := range entries {
			if entry.EstimatedTimeToRegainAccess > minutes {
				minutes = entry.EstimatedTimeToRegainAccess
			}
		}
	}
	return time.Duration(minutes * float64(time.Minute))
}
