package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

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
	Shards            int    `json:"shards"`
	SessionStartLimit struct {
		Total          int   `json:"total"`
		Remaining      int   `json:"remaining"`
		ResetAfter     int64 `json:"reset_after"` // milliseconds
		MaxConcurrency int   `json:"max_concurrency"`
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
	for attempt := 1; ; attempt++ {
		err := c.do(ctx, method, path, payload, out)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests && attempt < maxCallAttempts {
			delay := apiErr.RetryAfter
			if delay <= 0 {
				delay = time.Second
			}
			if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		return err
	}
}

func (c *restClient) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("discord: %s %s: encode request: %w", method, path, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("discord: %s %s: build request: %w", method, path, c.redact(err))
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("discord: %s %s: %w", method, path, c.redact(err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("discord: %s %s: read response: %w", method, path, c.redact(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFrom(method, path, resp, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("discord: %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

func apiErrorFrom(method, path string, resp *http.Response, body []byte) *APIError {
	apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}
	var envelope struct {
		Message    string  `json:"message"`
		Code       int     `json:"code"`
		RetryAfter float64 `json:"retry_after"` // seconds, fractional
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Message != "" {
		apiErr.Message = envelope.Message
		apiErr.Code = envelope.Code
		apiErr.RetryAfter = time.Duration(envelope.RetryAfter * float64(time.Second))
	} else {
		const maxSnippet = 256
		snippet := string(body)
		if len(snippet) > maxSnippet {
			snippet = snippet[:maxSnippet]
		}
		apiErr.Message = snippet
	}
	if apiErr.Status == http.StatusTooManyRequests && apiErr.RetryAfter <= 0 {
		if seconds, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil {
			apiErr.RetryAfter = time.Duration(seconds * float64(time.Second))
		}
	}
	return apiErr
}

func (c *restClient) redact(err error) error {
	if err == nil || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
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
