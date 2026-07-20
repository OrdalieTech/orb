// Package googlechat implements the Google Chat adapter for the pi-go chat
// gateway: an HTTP-endpoint Chat app with bearer-JWT-verified ingress,
// service-account authenticated delivery, and authenticated media download.
//
// Inbound interaction events are verified against Google's published JWKS
// (issuer chat@system.gserviceaccount.com, audience = the numeric project
// number) before parsing; [New] refuses to construct without the audience or
// the service-account key, so neither trust boundary is skippable. Replies
// are always asynchronous via spaces.messages.create — the synchronous
// 200-body reply is deliberately never used. Final messages use
// client-assigned ids derived from the inbound event, making delivery
// idempotent and crash-safe. Google Chat has no typing indicator, so Typing
// is a no-op; D28 also keeps Preview final-only.
package googlechat

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

const platformName = "googlechat"

// DefaultBaseURL is the production Chat API endpoint.
const DefaultBaseURL = "https://chat.googleapis.com"

// DefaultTokenURL is the production OAuth2 token endpoint.
const DefaultTokenURL = "https://oauth2.googleapis.com/token"

// DefaultCertURL serves the JWKS Google signs inbound app events with.
const DefaultCertURL = "https://www.googleapis.com/service_accounts/v1/jwk/chat@system.gserviceaccount.com"

const maxResponseBytes = 4 << 20

const maxSpaceWriteCache = 1024

// Options configures [New].
type Options struct {
	// ProjectNumber is the numeric GCP project number (NOT the project id).
	// It is the required audience of every inbound event JWT. Required.
	ProjectNumber string
	// CredentialsJSON is the downloaded service-account key file
	// (client_email + private_key). It signs the outbound JWT-bearer
	// assertion for the chat.bot scope. Required.
	CredentialsJSON []byte
	// BaseURL overrides the Chat API endpoint (tests). Default
	// [DefaultBaseURL].
	BaseURL string
	// TokenURL overrides the OAuth2 token endpoint (tests). It is also the
	// aud claim of the signed assertion. Default [DefaultTokenURL].
	TokenURL string
	// CertURL overrides where the inbound-event JWKS is fetched from
	// (tests). Default [DefaultCertURL].
	CertURL string
	// HTTPClient overrides the HTTP client. Default: 30s timeout.
	HTTPClient *http.Client
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
}

type serviceAccountKey struct {
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
}

// Adapter is the Google Chat implementation of [chat.Adapter]. Final and
// notification writes are serialized per space at the Chat API's 1/s quota.
type Adapter struct {
	projectNumber string
	baseURL       string
	tokenURL      string
	client        *http.Client
	logger        *slog.Logger

	clientEmail string
	keyID       string
	key         *rsa.PrivateKey

	verifier *verifier

	spaceWriteMu   sync.Mutex
	lastSpaceWrite map[string]time.Time

	// maxAttempts and backoff govern retries of retryable Chat API errors
	// (429/500/503); tests shrink the backoff.
	maxAttempts int
	backoff     func(attempt int) time.Duration
	sleep       func(ctx context.Context, d time.Duration) error
	now         func() time.Time

	tokenMu     sync.Mutex
	tokenValue  string
	tokenExpiry time.Time
}

var _ chat.Adapter = (*Adapter)(nil)

// New builds the adapter. It refuses to construct without ProjectNumber
// (inbound events could not be verified) or a parseable service-account key
// (outbound calls could not be authenticated). It performs no network calls.
func New(opts Options) (*Adapter, error) {
	if opts.ProjectNumber == "" {
		return nil, errors.New("googlechat: Options.ProjectNumber is required (inbound event JWTs cannot be verified without an audience)")
	}
	if len(opts.CredentialsJSON) == 0 {
		return nil, errors.New("googlechat: Options.CredentialsJSON is required")
	}
	var key serviceAccountKey
	if err := json.Unmarshal(opts.CredentialsJSON, &key); err != nil {
		return nil, fmt.Errorf("googlechat: parse credentials: %w", err)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, errors.New("googlechat: credentials need client_email and private_key")
	}
	privateKey, err := parsePrivateKey(key.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("googlechat: parse credentials private_key: %w", err)
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.TokenURL == "" {
		opts.TokenURL = DefaultTokenURL
	}
	if opts.CertURL == "" {
		opts.CertURL = DefaultCertURL
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{
		projectNumber: opts.ProjectNumber,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		tokenURL:      opts.TokenURL,
		client:        client,
		logger:        opts.Logger,
		clientEmail:   key.ClientEmail,
		keyID:         key.PrivateKeyID,
		key:           privateKey,
		verifier: &verifier{
			certURL:      opts.CertURL,
			audience:     opts.ProjectNumber,
			client:       client,
			now:          time.Now,
			refetchFloor: defaultRefetchFloor,
		},
		lastSpaceWrite: map[string]time.Time{},
		maxAttempts:    4,
		backoff:        defaultBackoff,
		sleep:          sleepContext,
		now:            time.Now,
	}, nil
}

// Reservations are atomic per space, so concurrent turns cannot claim the
// same quota slot.
func (a *Adapter) waitSpaceWrite(ctx context.Context, space string, interval time.Duration) error {
	now := a.now()
	a.spaceWriteMu.Lock()
	at := now
	if next := a.lastSpaceWrite[space].Add(interval); next.After(at) {
		at = next
	}
	a.recordSpaceWriteLocked(space, at)
	a.spaceWriteMu.Unlock()
	if wait := at.Sub(now); wait > 0 {
		return a.sleep(ctx, wait)
	}
	return nil
}

func (a *Adapter) recordSpaceWriteLocked(space string, at time.Time) {
	if _, exists := a.lastSpaceWrite[space]; !exists && len(a.lastSpaceWrite) >= maxSpaceWriteCache {
		var oldestSpace string
		var oldest time.Time
		for candidate, wroteAt := range a.lastSpaceWrite {
			if oldestSpace == "" || wroteAt.Before(oldest) {
				oldestSpace, oldest = candidate, wroteAt
			}
		}
		delete(a.lastSpaceWrite, oldestSpace)
	}
	a.lastSpaceWrite[space] = at
}

// defaultBackoff is truncated exponential backoff: 1s, 2s, 4s, ... capped at
// 64s. ponytail: no jitter — a single-process sender per space cannot
// thundering-herd itself, and deterministic waits keep tests honest.
func defaultBackoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	if d > 64*time.Second {
		d = 64 * time.Second
	}
	return d
}

// Current service-account downloads are PKCS#8; legacy PKCS#1 is omitted.
func parsePrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("key is not PKCS#8")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return platformName }

// Account returns the numeric project number — the Chat app's identity —
// matching the Account set on every normalized message.
func (a *Adapter) Account() string { return a.projectNumber }

// Download implements [chat.Adapter]: an authenticated
// GET /v1/media/{resourceName}?alt=media for UPLOADED_CONTENT attachments
// (the only kind ingress emits refs for; Drive-sourced files are not
// downloadable with the chat.bot scope and are surfaced as text notes).
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if ref.ID == "" {
		return nil, "", errors.New("googlechat: attachment has no media resource name")
	}
	token, err := a.bearer(ctx)
	if err != nil {
		return nil, "", err
	}
	mediaURL := a.baseURL + "/v1/media/" + ref.ID + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("googlechat: build media request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("googlechat: download media: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		return nil, "", apiErrorFrom(resp, data)
	}
	return resp.Body, ref.MIME, nil
}

// APIError is one Chat API error envelope
// ({"error":{"code":...,"message":...,"status":...}}).
type APIError struct {
	// HTTPStatus is the HTTP status code of the response.
	HTTPStatus int
	// Code is the numeric code from the error envelope (matches HTTPStatus
	// in practice).
	Code int
	// Status is the canonical gRPC status name, e.g. "NOT_FOUND",
	// "ALREADY_EXISTS", "RESOURCE_EXHAUSTED".
	Status string
	// Message is the server-provided description.
	Message string
	// RetryAfter is the Retry-After header delay when the server sent one.
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("googlechat: api error HTTP %d", e.HTTPStatus)
	if e.Status != "" {
		msg += " " + e.Status
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func apiErrorFrom(resp *http.Response, body []byte) error {
	apiErr := &APIError{HTTPStatus: resp.StatusCode}
	if header := resp.Header.Get("Retry-After"); header != "" {
		if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
			apiErr.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != 0 {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		apiErr.Status = envelope.Error.Status
		return apiErr
	}
	const maxSnippet = 256
	snippet := string(body)
	if len(snippet) > maxSnippet {
		snippet = snippet[:maxSnippet]
	}
	apiErr.Message = snippet
	return apiErr
}

func retryable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.HTTPStatus {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return true
	}
	return false
}

func isStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.HTTPStatus == status
}

type apiMessage struct {
	Name   string     `json:"name,omitempty"`
	Text   string     `json:"text,omitempty"`
	Thread *apiThread `json:"thread,omitempty"`
}

type apiThread struct {
	Name string `json:"name,omitempty"`
}

func (a *Adapter) createMessage(ctx context.Context, space, messageID, text, thread string) (string, error) {
	query := url.Values{}
	if messageID != "" {
		query.Set("messageId", messageID)
	}
	if thread != "" {
		query.Set("messageReplyOption", "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
	}
	payload := apiMessage{Text: text}
	if thread != "" {
		payload.Thread = &apiThread{Name: thread}
	}
	var out apiMessage
	err := a.do(ctx, http.MethodPost, "/v1/"+space+"/messages", query, payload, &out)
	if err != nil {
		return "", err
	}
	if out.Name != "" {
		return out.Name, nil
	}
	if messageID != "" {
		return space + "/messages/" + messageID, nil
	}
	return "", nil
}

func (a *Adapter) updateMessage(ctx context.Context, name, text, thread string) error {
	query := url.Values{}
	query.Set("updateMask", "text")
	query.Set("allowMissing", "true")
	payload := apiMessage{Text: text}
	if thread != "" {
		payload.Thread = &apiThread{Name: thread}
	}
	return a.do(ctx, http.MethodPatch, "/v1/"+name, query, payload, nil)
}

func (a *Adapter) createOrUpdate(ctx context.Context, space, messageID, text, thread string) (string, error) {
	name, err := a.createMessage(ctx, space, messageID, text, thread)
	if err == nil {
		return name, nil
	}
	if !isStatus(err, http.StatusConflict) {
		return "", err
	}
	name = space + "/messages/" + messageID
	if err := a.updateMessage(ctx, name, text, thread); err != nil {
		return "", err
	}
	return name, nil
}

func (a *Adapter) do(ctx context.Context, method, path string, query url.Values, payload, out any) error {
	var body []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("googlechat: encode request: %w", err)
		}
		body = data
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		err := a.doOnce(ctx, method, path, query, body, out)
		if err == nil {
			return nil
		}
		if isStatus(err, http.StatusUnauthorized) && !refreshed {
			// Token expired or clock skew: mint a new assertion, retry once.
			refreshed = true
			a.invalidateToken()
			continue
		}
		if retryable(err) && attempt+1 < a.maxAttempts {
			delay := a.backoff(attempt)
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
				delay = min(apiErr.RetryAfter, 64*time.Second)
			}
			if sleepErr := a.sleep(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		return err
	}
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

func (a *Adapter) doOnce(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	token, err := a.bearer(ctx)
	if err != nil {
		return err
	}
	target := a.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("googlechat: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("googlechat: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("googlechat: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFrom(resp, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("googlechat: decode response: %w", err)
		}
	}
	return nil
}

func turnMessageID(replyTo string, key chat.ConversationKey) string {
	seed := replyTo
	if seed == "" {
		seed = key.String()
	}
	sum := sha256.Sum256([]byte(seed))
	return "client-" + hex.EncodeToString(sum[:])[:24]
}
