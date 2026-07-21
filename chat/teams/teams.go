// Package teams implements the Microsoft Teams (Bot Framework) adapter for
// the pigo chat gateway, speaking the raw connector REST API with no SDK:
// JWT-validated webhook ingress, typing plus final-only delivery,
// markdown-subset formatting with UTF-16 chunking, and authenticated
// attachment download. It plugs into the chat processor via [chat.Adapter].
//
// Inbound JWT validation is the trust boundary and is never skippable: [New]
// refuses to construct without the app id (the token audience) and secret,
// and the webhook rejects every request whose Bot Framework token fails the
// full check chain — RS256 signature against the cached JWKS (with the
// endorsements filter), issuer, audience, validity window with 5-minute
// skew, and the serviceUrl claim matching the activity's serviceUrl byte
// for byte. The endpoint overrides in [Options] redirect validation at test
// servers; no configuration disables it.
package teams

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// DefaultOpenIDMetadataURL is the Bot Framework OpenID metadata document
// locating the JWKS for inbound token validation.
const DefaultOpenIDMetadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"

// DefaultIssuer is the issuer of inbound Bot Framework connector tokens.
const DefaultIssuer = "https://api.botframework.com"

const platformName = "teams"

const tokenScope = "https://api.botframework.com/.default"

const maxConversationCache = 1024

// Options configures [New].
type Options struct {
	// AppID is the Azure bot application (client) id. Required: it is the
	// outbound OAuth client id, the inbound token audience — validation
	// cannot run without it — and the adapter [Adapter.Account] identity.
	AppID string
	// AppPassword is the app client secret used by the client-credentials
	// grant. Required.
	AppPassword string
	// TenantID selects the single-tenant token endpoint
	// (login.microsoftonline.com/{tenant}); empty means the multi-tenant
	// "botframework.com" endpoint.
	TenantID string
	// TokenURL overrides the OAuth token endpoint (tests).
	TokenURL string
	// OpenIDMetadataURL overrides the OpenID metadata document used to
	// locate the JWKS (tests). Default [DefaultOpenIDMetadataURL].
	OpenIDMetadataURL string
	// Issuer overrides the expected inbound token issuer (tests). Default
	// [DefaultIssuer].
	Issuer string
	// HTTPClient overrides the HTTP client. Default: 30s timeout.
	HTTPClient *http.Client
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
	// TypingInterval is the typing-activity refresh cadence (the indicator
	// is transient). Default 3.5s.
	TypingInterval time.Duration
	// ChunkDelay paces consecutive Finalize chunk sends. Default 350ms.
	ChunkDelay time.Duration
}

type convInfo struct {
	serviceURL string
}

// Adapter is the Microsoft Teams implementation of [chat.Adapter].
type Adapter struct {
	appID     string
	logger    *slog.Logger
	client    *client
	validator *jwtValidator

	typingInterval time.Duration
	chunkDelay     time.Duration
	chunkLimit     int

	// mu guards the per-conversation serviceUrl store and the dead set.
	//
	// ponytail: in-memory stores; conversations rediscover their serviceUrl
	// from the next inbound activity after a restart, and proactive sends
	// across restarts would need external persistence.
	mu    sync.Mutex
	convs map[string]convInfo
	dead  map[string]struct{}
	order []string
}

var _ chat.Adapter = (*Adapter)(nil)

// New creates a Teams adapter. It refuses to construct without AppID and
// AppPassword: the app id is the inbound token audience, so inbound
// validation — the trust boundary — could not run without it.
func New(opts Options) (*Adapter, error) {
	if opts.AppID == "" {
		return nil, errors.New("teams: Options.AppID is required (it is the inbound token audience; validation cannot run without it)")
	}
	if opts.AppPassword == "" {
		return nil, errors.New("teams: Options.AppPassword is required")
	}
	if opts.OpenIDMetadataURL == "" {
		opts.OpenIDMetadataURL = DefaultOpenIDMetadataURL
	}
	if opts.Issuer == "" {
		opts.Issuer = DefaultIssuer
	}
	if opts.TokenURL == "" {
		tenant := opts.TenantID
		if tenant == "" {
			tenant = "botframework.com"
		}
		opts.TokenURL = "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	}
	if opts.TypingInterval <= 0 {
		opts.TypingInterval = 3500 * time.Millisecond
	}
	if opts.ChunkDelay <= 0 {
		opts.ChunkDelay = 350 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{
		appID:  opts.AppID,
		logger: opts.Logger,
		client: newClient(&tokenSource{
			url:          opts.TokenURL,
			clientID:     opts.AppID,
			secret:       opts.AppPassword,
			http:         httpClient,
			now:          time.Now,
			refreshEarly: 5 * time.Minute,
		}, httpClient),
		validator: &jwtValidator{
			metadataURL:  opts.OpenIDMetadataURL,
			issuer:       opts.Issuer,
			audience:     opts.AppID,
			http:         httpClient,
			now:          time.Now,
			ttl:          24 * time.Hour,
			refetchFloor: 30 * time.Second,
		},
		typingInterval: opts.TypingInterval,
		chunkDelay:     opts.ChunkDelay,
		chunkLimit:     chunkLimit,
		convs:          map[string]convInfo{},
		dead:           map[string]struct{}{},
	}, nil
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return platformName }

// Account returns the bot app id, matching the Account set on every
// normalized message.
func (a *Adapter) Account() string { return a.appID }

func (a *Adapter) rememberConversation(chatID, serviceURL string) {
	if chatID == "" || serviceURL == "" {
		return
	}
	a.mu.Lock()
	if _, exists := a.convs[chatID]; !exists {
		if len(a.convs) >= maxConversationCache {
			oldest := a.order[0]
			a.order = a.order[1:]
			delete(a.convs, oldest)
			delete(a.dead, oldest)
		}
		a.order = append(a.order, chatID)
	}
	a.convs[chatID] = convInfo{serviceURL: serviceURL}
	delete(a.dead, chatID)
	a.mu.Unlock()
}

func (a *Adapter) conversation(chatID string) (convInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.convs[chatID]
	return info, ok
}

func (a *Adapter) markDead(chatID string) {
	a.mu.Lock()
	if _, exists := a.convs[chatID]; exists {
		a.dead[chatID] = struct{}{}
	}
	a.mu.Unlock()
}

func (a *Adapter) isDead(chatID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, dead := a.dead[chatID]
	return dead
}

func (a *Adapter) trustedHost(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, info := range a.convs {
		if u, err := url.Parse(info.serviceURL); err == nil && u.Host == host {
			return true
		}
	}
	return false
}

// Download implements [chat.Adapter]: the attachment contentUrl is fetched
// directly, with the connector token attached only for known connector
// hosts (Teams file contentUrls on smba.* need it; sending it anywhere else
// would leak the token to whoever controls the URL).
//
// ponytail: inline images and connector-served files only; SharePoint
// attachment URLs need Graph credentials the adapter does not hold.
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ID, nil)
	if err != nil {
		return nil, "", fmt.Errorf("teams: build attachment request: %w", err)
	}
	if a.trustedHost(request.URL.Host) {
		token, err := a.client.tokens.token(ctx)
		if err != nil {
			return nil, "", err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.client.http.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("teams: download attachment: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, "", fmt.Errorf("teams: download attachment: http %d", response.StatusCode)
	}
	mime := ref.MIME
	if ct := response.Header.Get("Content-Type"); ct != "" {
		mime = ct
	}
	return response.Body, mime, nil
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
