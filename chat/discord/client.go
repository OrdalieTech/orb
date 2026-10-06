package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/OrdalieTech/orb/chat/internal/httpjson"
)

const maxCallAttempts = 3

// APIError is a decoded Discord REST failure.
type APIError struct {
	// Method and Path identify the failed call.
	Method, Path string
	// Status is the HTTP status code.
	Status int
	// Code is the Discord JSON error code (e.g. 10008 Unknown Message), 0
	// when the body carried none.
	Code int
	// Message is the error text from the response body.
	Message string
	// RetryAfter is the server-requested pause from a 429 response.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("discord: %s %s: %s (code %d, http %d)",
		e.Method, e.Path, e.Message, e.Code, e.Status)
}

type restClient struct {
	baseURL string
	token   string
	http    *http.Client
	// sleep is the rate-limit pause; a seam for tests.
	sleep func(ctx context.Context, d time.Duration) error
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

func noMentions() allowedMentions { return allowedMentions{Parse: []string{}} }

type messageReference struct {
	MessageID       string `json:"message_id"`
	FailIfNotExists bool   `json:"fail_if_not_exists"`
}

type createMessageParams struct {
	Content          string            `json:"content"`
	MessageReference *messageReference `json:"message_reference,omitempty"`
	AllowedMentions  allowedMentions   `json:"allowed_mentions"`
}

type editMessageParams struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type apiMessage struct {
	ID string `json:"id"`
}

type gatewayBotResponse struct {
	URL               string `json:"url"`
	SessionStartLimit struct {
		Remaining  int   `json:"remaining"`
		ResetAfter int64 `json:"reset_after"` // milliseconds
	} `json:"session_start_limit"`
}

// call performs one REST request, retrying after the server-requested pause
// when Discord answers 429. Nothing else is ever retried here — in
// particular 401/403 surface immediately, so an auth failure can never
// hot-loop into Cloudflare's invalid-request ban.
//
// ponytail: no proactive rate-limit bucket tracking (X-RateLimit-* headers
// are ignored) — at one bot's send cadence the reactive 429 path suffices.
func (c *restClient) call(ctx context.Context, method, path string, payload, out any) error {
	return httpjson.Retry(ctx, maxCallAttempts, c.sleep, func() error {
		resp, err := httpjson.Do(ctx, c.http, method, c.baseURL+path, payload, out, "Authorization", "Bot "+c.token)
		if err != nil {
			return fmt.Errorf("discord: %s %s: %w", method, path, httpjson.Redact(err, c.token))
		}
		if !resp.OK() {
			return apiErrorFrom(method, path, resp)
		}
		return nil
	}, func(err error, _ int) (time.Duration, bool) {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
			return 0, false
		}
		if apiErr.RetryAfter <= 0 {
			return time.Second, true
		}
		return apiErr.RetryAfter, true
	})
}

func apiErrorFrom(method, path string, resp *httpjson.Response) *APIError {
	apiErr := &APIError{Method: method, Path: path, Status: resp.Status}
	var envelope struct {
		Message    string  `json:"message"`
		Code       int     `json:"code"`
		RetryAfter float64 `json:"retry_after"` // seconds, fractional
	}
	if err := json.Unmarshal(resp.Body, &envelope); err == nil && envelope.Message != "" {
		apiErr.Message = envelope.Message
		apiErr.Code = envelope.Code
		apiErr.RetryAfter = time.Duration(envelope.RetryAfter * float64(time.Second))
	} else {
		apiErr.Message = httpjson.Snippet(resp.Body)
	}
	if apiErr.Status == http.StatusTooManyRequests && apiErr.RetryAfter <= 0 {
		apiErr.RetryAfter = httpjson.RetryAfter(resp.Header)
	}
	return apiErr
}

func (c *restClient) getGatewayBot(ctx context.Context) (*gatewayBotResponse, error) {
	var out gatewayBotResponse
	if err := c.call(ctx, http.MethodGet, "/gateway/bot", nil, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, errors.New("discord: /gateway/bot returned no url")
	}
	return &out, nil
}

func (c *restClient) createMessage(ctx context.Context, channelID string, params createMessageParams) (string, error) {
	var out apiMessage
	path := "/channels/" + url.PathEscape(channelID) + "/messages"
	if err := c.call(ctx, http.MethodPost, path, params, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *restClient) editMessage(ctx context.Context, channelID, messageID string, params editMessageParams) error {
	path := "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID)
	return c.call(ctx, http.MethodPatch, path, params, nil)
}

func (c *restClient) triggerTyping(ctx context.Context, channelID string) error {
	path := "/channels/" + url.PathEscape(channelID) + "/typing"
	return c.call(ctx, http.MethodPost, path, nil, nil)
}
