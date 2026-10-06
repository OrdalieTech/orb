package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/chat/internal/ctxsleep"
	"github.com/OrdalieTech/orb/chat/internal/httpjson"
)

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
		sleep:       ctxsleep.Sleep,
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
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
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
	var token string
	refreshed := false
	return httpjson.Retry(ctx, c.maxAttempts, c.sleep, func() error {
		var err error
		if token, err = c.tokens.token(ctx); err != nil {
			return err
		}
		response, err := httpjson.Do(ctx, c.http, method, callURL, payload, out, "Authorization", "Bearer "+token)
		if err != nil {
			// The token travels in a header, never the URL, so transport
			// errors cannot embed it.
			return fmt.Errorf("teams: %s %s: %w", method, callURL, err)
		}
		if response.OK() {
			return nil
		}
		return decodeAPIError(response)
	}, func(err error, attempt int) (time.Duration, bool) {
		apiErr, ok := asAPIError(err)
		switch {
		case !ok:
			return 0, false
		case apiErr.Status == http.StatusUnauthorized && !refreshed:
			refreshed = true
			c.tokens.invalidate(token)
			return 0, true
		case !retryableStatus(apiErr.Status):
			return 0, false
		case apiErr.RetryAfter > 0:
			return apiErr.RetryAfter, true
		}
		return c.backoff(attempt), true
	})
}

func decodeAPIError(response *httpjson.Response) *APIError {
	apiErr := &APIError{Status: response.Status, raw: httpjson.Snippet(response.Body), RetryAfter: httpjson.RetryAfter(response.Header)}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ErrorCode int `json:"errorCode"`
	}
	if json.Unmarshal(response.Body, &envelope) == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.ErrorCode = envelope.ErrorCode
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
