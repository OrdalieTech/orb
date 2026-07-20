package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// DefaultBaseURL is the production Web API endpoint.
const DefaultBaseURL = "https://slack.com"

const maxResponseBytes = 4 << 20

const maxCallAttempts = 5

// APIError is a decoded Web API failure: either an HTTP 429 or an
// ok:false envelope. Code carries the Slack error token ("ratelimited",
// "edit_window_closed", ...).
type APIError struct {
	Method string
	Status int
	Code   string
	// RetryAfter is the server-requested pause (Retry-After header on 429,
	// a default 1s on a body-level "ratelimited"), zero otherwise.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("slack: %s: %s", e.Method, e.Code)
	if hint := errorHint(e.Code); hint != "" {
		msg += " (" + hint + ")"
	}
	return msg
}

func errorHint(code string) string {
	switch code {
	case "invalid_auth", "token_revoked", "account_inactive", "missing_scope", "not_authed":
		return "bot token invalid or missing a required scope"
	case "channel_not_found", "not_in_channel", "is_archived":
		return "channel unavailable; the bot may need to be invited"
	case "edit_window_closed", "cant_update_message":
		return "message can no longer be edited"
	case "msg_too_long":
		return "message text exceeds Slack's limit"
	case "ratelimited":
		return "rate limited"
	}
	return ""
}

func isEditExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "edit_window_closed" || apiErr.Code == "cant_update_message"
}

type client struct {
	baseURL string
	token   string
	http    *http.Client
	// sleep is the rate-limit pause; a seam for tests.
	sleep func(ctx context.Context, d time.Duration) error
}

type apiEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (c *client) call(ctx context.Context, method string, params, out any) error {
	payload, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("slack: %s: encode params: %w", method, err)
	}
	for attempt := 1; ; attempt++ {
		err := c.doCall(ctx, method, payload, out)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && attempt < maxCallAttempts {
			if sleepErr := c.sleep(ctx, apiErr.RetryAfter); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		return err
	}
}

func (c *client) doCall(ctx context.Context, method string, payload []byte, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/"+method, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("slack: %s: build request: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("slack: %s: %w", method, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("slack: %s: read response: %w", method, err)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		retryAfter := time.Second
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			retryAfter = time.Duration(seconds) * time.Second
		}
		return &APIError{Method: method, Status: response.StatusCode, Code: "ratelimited", RetryAfter: retryAfter}
	}
	// ponytail: no retry on 5xx/transport errors — the processor's delivery
	// retry loop and Slack's event redelivery are the safety net.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("slack: %s: http %d", method, response.StatusCode)
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("slack: %s: decode response: %w", method, err)
	}
	if !envelope.OK {
		apiErr := &APIError{Method: method, Status: response.StatusCode, Code: envelope.Error}
		if apiErr.Code == "" {
			apiErr.Code = "unknown_error"
		}
		if apiErr.Code == "ratelimited" {
			apiErr.RetryAfter = time.Second
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("slack: %s: decode result: %w", method, err)
		}
	}
	return nil
}

type authTestResponse struct {
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
	Team   string `json:"team"`
}

func (c *client) authTest(ctx context.Context) (*authTestResponse, error) {
	var identity authTestResponse
	if err := c.call(ctx, "auth.test", struct{}{}, &identity); err != nil {
		return nil, err
	}
	return &identity, nil
}

type postMessageParams struct {
	Channel     string `json:"channel"`
	Text        string `json:"text"`
	ThreadTS    string `json:"thread_ts,omitempty"`
	UnfurlLinks bool   `json:"unfurl_links"`
}

type messageResponse struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

func (c *client) postMessage(ctx context.Context, params postMessageParams) (string, error) {
	var message messageResponse
	if err := c.call(ctx, "chat.postMessage", params, &message); err != nil {
		return "", err
	}
	return message.TS, nil
}

type updateMessageParams struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Text    string `json:"text"`
}

func (c *client) updateMessage(ctx context.Context, params updateMessageParams) error {
	return c.call(ctx, "chat.update", params, nil)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
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
