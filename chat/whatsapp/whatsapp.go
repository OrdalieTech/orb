// Package whatsapp implements the WhatsApp Business Cloud API adapter for
// the pi-go chat gateway: webhook ingress (hub handshake + HMAC-signed
// events), final-message delivery with reply threading, and authenticated
// media download.
//
// The adapter speaks only the official Graph Cloud API with caller-supplied
// credentials. Unsigned webhooks are refused by construction: [New] errors
// without an AppSecret, matching the Hermes reference stance. The Cloud API
// has no message editing, so Preview is a no-op and only the final reply is
// sent (no fake streaming).
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// GraphVersion is the pinned Graph API version used for every call.
const GraphVersion = "v23.0"

// platformName matches chat.Message.Platform for this adapter.
const platformName = "whatsapp"

// maxResponseBytes bounds how much of a Graph response body is read.
const maxResponseBytes = 4 << 20

// Options configures the WhatsApp Cloud API adapter.
type Options struct {
	// Token is the Graph API access token (Bearer). Required.
	Token string
	// PhoneNumberID is the business phone number id calls are made on
	// behalf of. Required.
	PhoneNumberID string
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
	// OnStatus receives delivery status callbacks from the statuses[]
	// webhook array. Default: no-op. Statuses arrive out of order; keep the
	// highest [StatusRank] per message id.
	OnStatus func(Status)
}

// Adapter is a chat.Adapter for the WhatsApp Business Cloud API.
type Adapter struct {
	opts    Options
	client  *http.Client
	baseURL string

	// maxAttempts and backoff govern retries of retryable Graph errors
	// (130429/131048); tests shrink the backoff.
	maxAttempts int
	backoff     func(attempt int) time.Duration
}

var _ chat.Adapter = (*Adapter)(nil)

// New builds the adapter. It refuses to construct without Token,
// PhoneNumberID, AppSecret, and VerifyToken.
func New(opts Options) (*Adapter, error) {
	switch {
	case opts.Token == "":
		return nil, fmt.Errorf("whatsapp: Options.Token is required")
	case opts.PhoneNumberID == "":
		return nil, fmt.Errorf("whatsapp: Options.PhoneNumberID is required")
	case opts.AppSecret == "":
		return nil, fmt.Errorf("whatsapp: Options.AppSecret is required (unsigned webhooks are not supported)")
	case opts.VerifyToken == "":
		return nil, fmt.Errorf("whatsapp: Options.VerifyToken is required")
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = "https://graph.facebook.com"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{
		opts:        opts,
		client:      client,
		baseURL:     baseURL,
		maxAttempts: 3,
		backoff: func(attempt int) time.Duration {
			return time.Duration(1<<attempt) * 500 * time.Millisecond
		},
	}, nil
}

// Platform implements chat.Adapter.
func (a *Adapter) Platform() string { return platformName }

// Account returns the business phone number id this adapter serves,
// matching the Account set on every normalized message.
func (a *Adapter) Account() string { return a.opts.PhoneNumberID }

// messagesPath is the /<phone_number_id>/messages endpoint path.
func (a *Adapter) messagesPath() string {
	return "/" + GraphVersion + "/" + url.PathEscape(a.opts.PhoneNumberID) + "/messages"
}

// do performs one Graph call: JSON in (optional), JSON out (optional).
// Non-2xx responses decode into a *GraphError when the body carries one.
func (a *Adapter) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("whatsapp: encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("whatsapp: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.opts.Token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("whatsapp: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphErrorFrom(resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("whatsapp: decode response: %w", err)
		}
	}
	return nil
}

// GraphError is one Graph API error object, either from an HTTP error
// response envelope or from a statuses[].errors[] entry.
type GraphError struct {
	Code      int    `json:"code"`
	Subcode   int    `json:"error_subcode,omitempty"`
	Type      string `json:"type,omitempty"`
	Title     string `json:"title,omitempty"`
	Message   string `json:"message,omitempty"`
	ErrorData struct {
		Details string `json:"details,omitempty"`
	} `json:"error_data,omitempty"`
	FBTraceID string `json:"fbtrace_id,omitempty"`
}

// Error implements error.
func (e *GraphError) Error() string {
	msg := fmt.Sprintf("whatsapp: graph error %d", e.Code)
	if hint := errorHint(e.Code); hint != "" {
		msg += " (" + hint + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.ErrorData.Details != "" {
		msg += ": " + e.ErrorData.Details
	}
	return msg
}

// errorHint translates the Cloud API error codes the gateway handles
// explicitly into operator-readable text.
func errorHint(code int) string {
	switch code {
	case 131047:
		return "outside the 24-hour customer service window; a template message is required"
	case 190:
		return "access token expired or invalid"
	case 130429:
		return "throughput rate limit hit"
	case 131048:
		return "spam or pair rate limit hit"
	case 100:
		return "invalid parameter"
	case 131026:
		return "message undeliverable"
	case 131051:
		return "unsupported message type"
	}
	return ""
}

// retryable reports whether a Graph error code warrants bounded
// backoff-and-retry. Only the rate limits qualify; 131047 (24h window) and
// 190 (bad token) surface immediately, and 100/131026/131051 fail fast.
func retryable(code int) bool {
	return code == 130429 || code == 131048
}

// graphErrorFrom decodes an error response body, falling back to a plain
// wrapped error when the envelope is absent.
func graphErrorFrom(status int, body []byte) error {
	var envelope struct {
		Error GraphError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != 0 {
		return &envelope.Error
	}
	const maxSnippet = 256
	snippet := string(body)
	if len(snippet) > maxSnippet {
		snippet = snippet[:maxSnippet]
	}
	return fmt.Errorf("whatsapp: graph returned HTTP %d: %s", status, snippet)
}
