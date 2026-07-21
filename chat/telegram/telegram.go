package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// Options configures [New].
type Options struct {
	// Token is the bot token from @BotFather. Required.
	Token string
	// BaseURL overrides the Bot API endpoint (tests). Default [DefaultBaseURL].
	BaseURL string
	// Secret is the webhook secret token compared (constant-time) against the
	// X-Telegram-Bot-Api-Secret-Token header. Required for Webhook ingress
	// (an empty secret would accept every POST); unused by Poll.
	Secret string
	// BotUsername pre-seeds the bot identity used for group mention gating
	// and /cmd@botname normalization; resolved via getMe when empty.
	BotUsername string
	// HTTPClient overrides both API clients, including the long-poll one
	// (tests). When nil, the poll client timeout is PollTimeout + 10s.
	HTTPClient *http.Client
	// PollTimeout is the getUpdates long-poll hold. Default 30s.
	PollTimeout time.Duration
	// PreviewMinInterval rate-limits preview edits per chat. Default 1s.
	PreviewMinInterval time.Duration
	// TypingInterval is the sendChatAction refresh cadence. Default 4.5s.
	TypingInterval time.Duration
	// MediaGroupDelay is the album buffering window. Default 1.2s.
	MediaGroupDelay time.Duration
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
}

// Adapter is the Telegram implementation of [chat.Adapter].
type Adapter struct {
	client  *client
	account string
	logger  *slog.Logger

	secret             string
	pollTimeout        time.Duration
	previewMinInterval time.Duration
	typingInterval     time.Duration
	mediaGroupDelay    time.Duration

	identityMu  sync.Mutex
	botUsername string
}

var _ chat.Adapter = (*Adapter)(nil)

// New creates a Telegram adapter. It performs no network calls; the bot
// identity is resolved lazily via getMe when needed.
func New(opts Options) (*Adapter, error) {
	if opts.Token == "" {
		return nil, errors.New("telegram: Options.Token is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 30 * time.Second
	}
	if opts.PreviewMinInterval <= 0 {
		opts.PreviewMinInterval = time.Second
	}
	if opts.TypingInterval <= 0 {
		opts.TypingInterval = 4500 * time.Millisecond
	}
	if opts.MediaGroupDelay <= 0 {
		opts.MediaGroupDelay = 1200 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	httpClient := opts.HTTPClient
	pollClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
		pollClient = &http.Client{Timeout: opts.PollTimeout + 10*time.Second}
	}
	return &Adapter{
		client: &client{
			baseURL:  strings.TrimRight(opts.BaseURL, "/"),
			token:    opts.Token,
			http:     httpClient,
			pollHTTP: pollClient,
			sleep:    sleepContext,
		},
		account:            accountFromToken(opts.Token),
		logger:             opts.Logger,
		secret:             opts.Secret,
		pollTimeout:        opts.PollTimeout,
		previewMinInterval: opts.PreviewMinInterval,
		typingInterval:     opts.TypingInterval,
		mediaGroupDelay:    opts.MediaGroupDelay,
		botUsername:        opts.BotUsername,
	}, nil
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return "telegram" }

// Account returns the bot's numeric id — the public prefix of the token —
// matching the Account set on every normalized message.
func (a *Adapter) Account() string { return a.account }

// Download implements [chat.Adapter]: getFile resolves the file path, then
// the file URL is streamed. The MIME type is the one carried on the ref
// (getFile does not report one).
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	file, err := a.client.getFile(ctx, ref.ID)
	if err != nil {
		return nil, "", err
	}
	if file.FilePath == "" {
		return nil, "", errors.New("telegram: getFile returned no file_path")
	}
	body, err := a.client.downloadFile(ctx, file.FilePath)
	if err != nil {
		return nil, "", err
	}
	return body, ref.MIME, nil
}

// ensureIdentity resolves and caches the bot username via getMe.
func (a *Adapter) ensureIdentity(ctx context.Context) error {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	if a.botUsername != "" {
		return nil
	}
	me, err := a.client.getMe(ctx)
	if err != nil {
		return err
	}
	if me.Username == "" {
		return errors.New("telegram: getMe returned no username")
	}
	a.botUsername = me.Username
	return nil
}

// username returns the cached bot username, or "" when unresolved.
func (a *Adapter) username() string {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	return a.botUsername
}

// accountFromToken derives the account id from the public numeric prefix of
// the bot token (the part before the colon is the bot id, not a secret).
func accountFromToken(token string) string {
	if i := strings.IndexByte(token, ':'); i > 0 {
		return token[:i]
	}
	return "bot"
}
