package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type tokenSource struct {
	url          string
	clientID     string
	secret       string
	http         *http.Client
	now          func() time.Time
	refreshEarly time.Duration

	// mu guards the cached token state; fetchMu serializes actual fetches
	// so at most one token request is in flight.
	mu         sync.Mutex
	fetchMu    sync.Mutex
	cached     string
	expiry     time.Time
	refreshing bool
}

func (s *tokenSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	now := s.now()
	valid := s.cached != "" && now.Before(s.expiry)
	fresh := valid && now.Before(s.refreshDeadline())
	if fresh {
		token := s.cached
		s.mu.Unlock()
		return token, nil
	}
	if valid && s.refreshing {
		// Another goroutine is renewing: reuse the stale-but-valid token.
		token := s.cached
		s.mu.Unlock()
		return token, nil
	}
	s.refreshing = true
	s.mu.Unlock()

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	// A goroutine we waited on may have refreshed already.
	s.mu.Lock()
	if s.cached != "" && s.now().Before(s.refreshDeadline()) {
		token := s.cached
		s.refreshing = false
		s.mu.Unlock()
		return token, nil
	}
	s.mu.Unlock()

	token, expiry, err := s.fetch(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if err != nil {
		if s.cached != "" && s.now().Before(s.expiry) {
			// The early refresh failed but the old token is still valid.
			return s.cached, nil
		}
		return "", err
	}
	s.cached, s.expiry = token, expiry
	return token, nil
}

func (s *tokenSource) refreshDeadline() time.Time {
	early := s.refreshEarly
	if lifetime := s.expiry.Sub(s.now()); early >= lifetime {
		early = lifetime / 2
	}
	return s.expiry.Add(-early)
}

func (s *tokenSource) invalidate(old string) {
	s.mu.Lock()
	if s.cached == old {
		s.cached = ""
		s.expiry = time.Time{}
	}
	s.mu.Unlock()
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (s *tokenSource) fetch(ctx context.Context) (string, time.Time, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.secret},
		"scope":         {tokenScope},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("teams: build token request: %w", s.redact(err))
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.http.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("teams: token request: %w", s.redact(err))
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("teams: read token response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("teams: token endpoint http %d: %s", response.StatusCode, redactString(snippet(body), s.secret))
	}
	var decoded tokenResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", time.Time{}, fmt.Errorf("teams: decode token response: %w", err)
	}
	if decoded.AccessToken == "" {
		return "", time.Time{}, errors.New("teams: token endpoint returned no access_token")
	}
	lifetime := time.Duration(decoded.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Minute
	}
	return decoded.AccessToken, s.now().Add(lifetime), nil
}

func (s *tokenSource) redact(err error) error {
	if err == nil || !strings.Contains(err.Error(), s.secret) {
		return err
	}
	return errors.New(redactString(err.Error(), s.secret))
}

func redactString(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "<secret>")
}

func snippet(body []byte) string {
	const maxSnippet = 256
	if len(body) > maxSnippet {
		return string(body[:maxSnippet])
	}
	return string(body)
}
