package chat

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
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

// Inbound is how a platform's messages reach the processor: it fetches them
// itself (Poll), or it is called and its host serves the handler (Webhook).
// Authorize, when set, admits the platform's senders in place of its host's
// shared allowlist.
type Inbound struct {
	Poll      func(ctx context.Context, publish func(Message) error) error
	Webhook   func(publish func(Message) error) http.Handler
	Authorize func(Message) error
}

// Extended is an [Adapter] whose platform gives the agent tools of its own:
// its host attaches Extension to every session of the agent.
type Extended interface {
	Extension(api extensions.API) error
}

// Identity is how an agent presents itself on its platforms.
type Identity struct {
	Name, About, Avatar string
}
