package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// Receipt records the platform message ids produced by a finalized delivery.
type Receipt struct {
	MessageIDs []string  `json:"messageIds"`
	At         time.Time `json:"at"`
}

// Delivery is one turn's output surface, created by the [Adapter]. Calls are
// serialized — never concurrent, with happens-before edges between them — but
// not single-goroutine: Preview and PreviewID arrive from the per-turn
// preview renderer goroutine, while Typing, Finalize, and Notify run on the
// turn goroutine. Adapters must not assume goroutine identity across calls.
type Delivery interface {
	// Typing signals a best-effort, repeatable typing indicator.
	Typing(ctx context.Context) error
	// Preview creates or edits the persistent preview message.
	Preview(ctx context.Context, text string) error
	// PreviewID returns the platform id of the preview message, or "" until
	// a preview exists.
	PreviewID() string
	// Finalize delivers the final text: it edits the preview when possible
	// and chunks long text into follow-up messages.
	Finalize(ctx context.Context, text string) (Receipt, error)
	// Notify sends a small out-of-band notice (/status output, errors).
	Notify(ctx context.Context, text string) error
}

// Adapter binds one platform (Telegram, WhatsApp, ...) to the processor.
type Adapter interface {
	// Platform returns the platform name matched against [Message.Platform].
	Platform() string
	// Account returns the bot/business account identity matched against
	// [Message.Account]. An empty string makes the adapter the wildcard for
	// its platform: it receives every message no more specific adapter
	// claims. Registering several adapters for one platform requires
	// distinct accounts.
	Account() string
	// NewDelivery creates the output surface for one turn. A non-empty
	// resumePreviewID signals crash recovery: Finalize must edit that
	// message instead of sending a new one.
	NewDelivery(key ConversationKey, replyTo string, resumePreviewID string) Delivery
	// Download resolves an attachment reference to its content and MIME type.
	Download(ctx context.Context, ref AttachmentRef) (io.ReadCloser, string, error)
}

// Platform is a service `orb chat` can run an agent on. Its package registers
// it, so `orb chat` knows only the platforms linked into the binary. A
// platform either brings messages in for the processor to route (Open) or
// drives the agent's sessions itself as an ACP client (Front).
type Platform struct {
	// Help is the platform's environment, as lines of `orb chat --help`.
	Help string
	// Open builds the platform's adapter, and the ingress that feeds it
	// messages until ctx ends, from the environment.
	Open func() (Adapter, func(ctx context.Context, publish func(Message) error) error, error)
	// Front runs the platform until ctx ends; serve runs the agent's ACP
	// server on one connection, and log is the agent's diagnostics.
	Front func(ctx context.Context, serve func(ctx context.Context, in io.Reader, out io.Writer) error, log io.Writer) error
}

var platforms = map[string]Platform{}

// Register makes a platform available to `orb chat` under name.
func Register(name string, platform Platform) { platforms[name] = platform }

// LookupPlatform returns the platform registered under name.
func LookupPlatform(name string) (Platform, bool) {
	platform, ok := platforms[name]
	return platform, ok
}

// PlatformNames returns the registered platforms' names, sorted.
func PlatformNames() []string {
	names := make([]string, 0, len(platforms))
	for name := range platforms {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// WebhookIngress serves a platform's webhook on ORB_CHAT_LISTEN (default
// 127.0.0.1:8080) at ORB_CHAT_PATH (default /<platform>) until ctx ends.
func WebhookIngress(
	platform string,
	webhook func(func(Message) error) http.Handler,
) func(context.Context, func(Message) error) error {
	return func(ctx context.Context, publish func(Message) error) error {
		listen := strings.TrimSpace(os.Getenv("ORB_CHAT_LISTEN"))
		if listen == "" {
			listen = "127.0.0.1:8080"
		}
		webhookPath := strings.TrimSpace(os.Getenv("ORB_CHAT_PATH"))
		if webhookPath == "" {
			webhookPath = "/" + platform
		}
		if !strings.HasPrefix(webhookPath, "/") || strings.ContainsAny(webhookPath, "{} \t\r\n") {
			return errors.New("ORB_CHAT_PATH must be a literal path starting with /")
		}
		mux := http.NewServeMux()
		mux.Handle(webhookPath, webhook(publish))
		// ponytail: one stdlib webhook server per process; terminate TLS and
		// multiplex public routes in the deployment's reverse proxy.
		server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		result := make(chan error, 1)
		go func() { result <- server.ListenAndServe() }()
		select {
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}
