// Package httpjson is the JSON-over-HTTP round trip and retry loop shared by
// the chat adapters' platform API clients.
package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

// Response is a completed exchange with its body read (at most 4 MiB).
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// ClientOr returns client, or a client with the adapters' default 30s timeout
// when it is nil.
func ClientOr(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return client
}

// OK reports a 2xx status.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Do sends payload as a JSON body (no body when nil, a reader as is) with the
// given header name/value pairs, which override the default Content-Type, and
// reads the response. On a 2xx status a non-empty body is decoded into out
// when out is non-nil. Errors name the failing stage; transport errors pass
// through.
func Do(ctx context.Context, client *http.Client, method, url string, payload, out any, header ...string) (*Response, error) {
	body, raw := payload.(io.Reader)
	if payload != nil && !raw {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
		header = append([]string{"Content-Type", "application/json"}, header...)
	}
	resp, err := send(ctx, client, method, url, body, header)
	if err != nil {
		return nil, err
	}
	r, err := readAll(resp)
	if err != nil {
		return nil, err
	}
	if out != nil && r.OK() && len(r.Body) > 0 {
		if err := json.Unmarshal(r.Body, out); err != nil {
			return r, fmt.Errorf("decode response: %w", err)
		}
	}
	return r, nil
}

// Get sends a GET with the given header pairs. A 200 OK returns the open
// response for the caller to stream and close; any other status is read,
// closed and returned as failed.
func Get(ctx context.Context, client *http.Client, url string, header ...string) (ok *http.Response, failed *Response, err error) {
	resp, err := send(ctx, client, http.MethodGet, url, nil, header)
	if err != nil || resp.StatusCode == http.StatusOK {
		return resp, nil, err
	}
	failed, _ = readAll(resp)
	return nil, failed, nil
}

func send(ctx context.Context, client *http.Client, method, url string, body io.Reader, header []string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	return client.Do(req)
}

// readAll reads (at most 4 MiB of) and closes resp's body; the Response is
// complete up to a read error.
func readAll(resp *http.Response) (*Response, error) {
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	r := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
	if err != nil {
		return r, fmt.Errorf("read response: %w", err)
	}
	return r, nil
}

// RetryAfter parses a Retry-After header given in seconds, at most an hour;
// zero when absent, malformed, not positive or not finite.
func RetryAfter(header http.Header) time.Duration {
	seconds, err := strconv.ParseFloat(header.Get("Retry-After"), 64)
	if err != nil || !(seconds > 0) || math.IsInf(seconds, 1) {
		return 0
	}
	return time.Duration(min(seconds, 3600) * float64(time.Second))
}

// Snippet bounds an error body quoted into an error message.
func Snippet(body []byte) string {
	const maxSnippet = 256
	if len(body) > maxSnippet {
		body = body[:maxSnippet]
	}
	return string(body)
}

// Redact replaces secret in err's text so a credential embedded in a request
// URL never reaches logs.
func Redact(err error, secret string) error {
	if err == nil || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "<token>"))
}

// Retry calls try up to attempts times. After a failure, pause reports
// whether to retry and how long to wait first, given the failed attempt's
// 0-based index; a non-positive wait retries at once.
func Retry(ctx context.Context, attempts int, sleep func(context.Context, time.Duration) error, try func() error, pause func(err error, attempt int) (time.Duration, bool)) error {
	for attempt := 0; ; attempt++ {
		err := try()
		if err == nil || attempt+1 >= attempts {
			return err
		}
		wait, ok := pause(err, attempt)
		if !ok {
			return err
		}
		if wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
}
