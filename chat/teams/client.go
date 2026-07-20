package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

// APIError is a decoded connector failure.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the error.code string from the error envelope, when present.
	Code string
	// ErrorCode is the Teams-specific numeric errorCode (209 =
	// MessageWritesBlocked), when present.
	ErrorCode int
	// Message is the error message text, when present.
	Message string
	// RetryAfter is the server-requested pause from a Retry-After header,
	// when present (Teams does not guarantee one on 429).
	RetryAfter time.Duration

	// raw is a bounded body snippet used to match MessageWritesBlocked,
	// whose envelope nesting varies.
	raw string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("teams: connector http %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.ErrorCode != 0 {
		msg += fmt.Sprintf(" (errorCode %d)", e.ErrorCode)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	} else if e.raw != "" {
		msg += ": " + e.raw
	}
	return msg
}

func (e *APIError) writesBlocked() bool {
	return e.Status == http.StatusForbidden &&
		(e.ErrorCode == 209 || strings.Contains(e.raw, "MessageWritesBlocked"))
}

type client struct {
	tokens *tokenSource
	http   *http.Client

	maxAttempts int
	backoffBase time.Duration
	backoffCap  time.Duration
	// sleep and jitter are seams for tests.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func(d time.Duration) time.Duration
}

func newClient(tokens *tokenSource, httpClient *http.Client) *client {
	return &client{
		tokens:      tokens,
		http:        httpClient,
		maxAttempts: 4,
		backoffBase: 2 * time.Second,
		backoffCap:  20 * time.Second,
		sleep:       sleepContext,
		jitter: func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(d))) + 1
		},
	}
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusPreconditionFailed,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *client) backoff(attempt int) time.Duration {
	window := c.backoffBase << attempt
	if window > c.backoffCap || window <= 0 {
		window = c.backoffCap
	}
	return c.jitter(window)
}

func activitiesURL(serviceURL, conversationID, activityID string) string {
	joined := strings.TrimRight(serviceURL, "/") + "/v3/conversations/" + url.PathEscape(conversationID) + "/activities"
	if activityID != "" {
		joined += "/" + url.PathEscape(activityID)
	}
	return joined
}

type resourceResponse struct {
	ID string `json:"id"`
}

type channelAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	AADObjectID string `json:"aadObjectId,omitempty"`
}

type conversationAccount struct {
	ID string `json:"id"`
}

type outboundActivity struct {
	Type         string               `json:"type"`
	From         *channelAccount      `json:"from,omitempty"`
	Conversation *conversationAccount `json:"conversation,omitempty"`
	Text         string               `json:"text,omitempty"`
	TextFormat   string               `json:"textFormat,omitempty"`
	ReplyToID    string               `json:"replyToId,omitempty"`
}

func (c *client) createActivity(ctx context.Context, serviceURL, conversationID string, activity outboundActivity) (string, error) {
	var out resourceResponse
	if err := c.do(ctx, http.MethodPost, activitiesURL(serviceURL, conversationID, ""), activity, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *client) do(ctx context.Context, method, callURL string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("teams: encode activity: %w", err)
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		token, err := c.tokens.token(ctx)
		if err != nil {
			return err
		}
		status, data, header, err := c.roundTrip(ctx, method, callURL, body, token)
		if err != nil {
			return err
		}
		if status >= 200 && status < 300 {
			if out != nil && len(data) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("teams: decode connector response: %w", err)
				}
			}
			return nil
		}
		apiErr := decodeAPIError(status, data, header)
		if status == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.tokens.invalidate(token)
			continue
		}
		if retryableStatus(status) && attempt+1 < c.maxAttempts {
			delay := apiErr.RetryAfter
			if delay <= 0 {
				delay = c.backoff(attempt)
			}
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
}

func (c *client) roundTrip(ctx context.Context, method, callURL string, body []byte, token string) (int, []byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, method, callURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("teams: build connector request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.http.Do(request)
	if err != nil {
		// The token travels in a header, never the URL, so transport
		// errors cannot embed it.
		return 0, nil, nil, fmt.Errorf("teams: %s %s: %w", method, callURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("teams: read connector response: %w", err)
	}
	return response.StatusCode, data, response.Header, nil
}

func decodeAPIError(status int, body []byte, header http.Header) *APIError {
	apiErr := &APIError{Status: status, raw: snippet(body)}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ErrorCode int `json:"errorCode"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.ErrorCode = envelope.ErrorCode
	}
	if header != nil {
		if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds > 0 {
			apiErr.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return apiErr
}

func asAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
