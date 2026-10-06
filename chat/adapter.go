package chat

import (
	"context"
	"io"
	"net/http"
	"slices"
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
	// Env names the environment variables the platform reads, and About
	// says what it does, for `orb chat --help`.
	Env   []string
	About string
	// Open builds the platform's adapter, and how messages come in, from the
	// environment.
	Open func() (Adapter, Inbound, error)
	// Front runs the platform until ctx ends.
	Front func(ctx context.Context, agent Agent) error
	// Configure maps the platform's section of an agent's configuration file
	// onto the environment it reads, for a harness that sets agents up from
	// one file; nil when the platform takes no settings beyond its Env.
	Configure func(agent Identity, section map[string]any) (map[string]string, error)
	// Sidecar is a process the platform needs beside the agent, run as
	// another user so the agent's tools cannot reach it, such as an ACP client
	// holding the platform's identity: given relay, a command that connects
	// its stdio to the agent's ACP socket, it returns the process's command
	// and settings. It also gets the variables Env names.
	Sidecar func(relay []string) (command []string, env map[string]string)
}

// Identity is how an agent presents itself on its platforms.
type Identity struct {
	Name, About, Avatar string
}

// Inbound is how a platform's messages reach the processor: it fetches them
// itself (Poll), or it is called and `orb chat` serves its handler (Webhook).
type Inbound struct {
	Poll    func(ctx context.Context, publish func(Message) error) error
	Webhook func(publish func(Message) error) http.Handler
}

// Agent is the running agent as a platform's Front reaches it.
type Agent struct {
	// Serve runs the agent's ACP server on one connection until it closes.
	Serve func(ctx context.Context, in io.Reader, out io.Writer) error
	// Connect returns the command line of a process that relays its stdio to
	// the ACP socket at path, for a client that starts its agent as a command.
	Connect func(socket string) ([]string, error)
	// Log receives the platform's diagnostics.
	Log io.Writer
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
