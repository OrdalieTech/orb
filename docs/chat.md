# Chat gateway

The `chat` package (D27) turns the pi-go SDK into a multi-user messaging agent: a synchronous,
at-least-once turn processor around `codingagent.AgentSession` with normalized platform messages,
a `SessionProvider` ownership seam, and platform adapters in `chat/telegram`, `chat/whatsapp`,
and — since wave 2 (D28) — `chat/slack`, `chat/teams`, `chat/discord`, `chat/messenger`, and
`chat/googlechat` (see [Platforms](#platforms-wave-2)).
Dependency direction is strictly `chat → codingagent`; nothing in the SDK imports `chat`.

## Quick start — local Telegram bot

`chat/examples/localbot` is a complete runnable gateway: long-poll ingress, the durable local
spool, and per-conversation sessions under a data directory.

```bash
TELEGRAM_BOT_TOKEN=<token from @BotFather> \
TELEGRAM_ALLOWED_SENDERS=<your telegram user id> \
go run ./chat/examples/localbot
```

The wiring, in full:

```go
provider, err := chat.NewLocalProvider(filepath.Join(dataDir, "sessions"))
adapter, err := telegram.New(telegram.Options{Token: token})
processor, err := chat.New(chat.Options{
    Sessions:  provider,
    Adapters:  []chat.Adapter{adapter},
    Authorize: chat.AllowAll, // explicit opt-out; use an allowlist in production
})
local, err := chat.NewLocal(processor, filepath.Join(dataDir, "spool.jsonl"))
err = adapter.Poll(ctx, local.Publish)
```

Without `TELEGRAM_ALLOWED_SENDERS`, every Telegram user who can reach the bot may drive your
agent session and spend your tokens. Set the allowlist in production.

## Processor

```go
func New(opts Options) (*Processor, error)
func (p *Processor) Handle(ctx context.Context, m Message) error
func (p *Processor) Close(ctx context.Context) error
```

`Handle` is synchronous: it returns only when the turn is delivered, deduplicated, or failed.
The external queue acks after it returns, so the queue itself is the buffer. Per-conversation
serialization is a refcounted keyed mutex — messages on one `ConversationKey` are strictly FIFO
turns, and an idle conversation retains zero goroutines and zero map entries. A global semaphore
bounds concurrent turns. `Close` rejects new messages and waits for in-flight turns.

### Options

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Sessions` | `SessionProvider` | — | Exclusive conversation ownership. Required. |
| `Adapters` | `[]Adapter` | — | Required. Registered by `(Platform(), Account())` — run many accounts of one platform as separate adapter instances. An exact account match wins; an adapter with an empty `Account()` is its platform's wildcard. Duplicate pairs are rejected. |
| `Authorize` | `func(Message) error` | — | Gates every inbound message. Required; pass `chat.AllowAll` to opt out explicitly. |
| `MaxConcurrent` | `int` | 64 | Global concurrent-turn bound. |
| `PreviewInterval` | `time.Duration` | 1s | Preview edit cadence. |
| `TurnTimeout` | `time.Duration` | 10m | Per-turn context guard. |
| `Logger` | `*slog.Logger` | discard | Non-fatal diagnostics. |

`Message` is fully JSON-serializable so it can cross a durable queue. `Message.EventID` is
platform-unique and stable across redelivery — it is the dedupe key for the turn ledger.
`Message.Key()` returns the `ConversationKey` (`Tenant`/`Platform`/`Account`/`ChatID`/`ThreadID`);
`ConversationKey.String()` is an injective, filesystem-safe join used for session directories,
lock keys, and partitioning.

## SessionProvider

```go
type SessionProvider interface {
    Acquire(ctx context.Context, key ConversationKey) (*Conversation, error)
}

type Conversation struct {
    Session *codingagent.AgentSession
    Manager *sessionstore.SessionManager // ledger writes + raw entry reads
    Close   func(ctx context.Context) error // exactly once
}
```

`Acquire` hands out **exclusive ownership** of one hydrated agent session; the processor holds it
for the duration of a turn and releases it through `Close`. `NewLocalProvider(root, opts...)`
maps each key to a sanitized session directory under `root`, resumes the most recent session file
for the key or creates one, and shares one `ModelRegistry` and one `SettingsManager` across all
conversations. It refuses double acquisition within the process.

The local JSONL provider is **single-process only**. Its in-memory in-use map cannot coordinate
writers across processes, and per-write file locks cannot either. Cluster deployments must supply
their own `SessionProvider` with partitioned or fenced conversation ownership (e.g. one owner per
key partition, with lease fencing) — that is the entire seam.

## Tools are off by default

Sessions from `NewLocalProvider` are constructed with `NoTools: "all"`: the model answers from
context only. Anything else means chat users execute tools on the gateway host, so enabling them
must be an explicit, isolated decision:

```go
provider, err := chat.NewLocalProvider(root,
    chat.WithSessionOptions(func(key chat.ConversationKey, o *codingagent.AgentSessionOptions) {
        o.NoTools = ""
        o.CWD = workspaceFor(key)          // never a shared host directory
        o.ToolOptions = &tools.ToolsOptions{ // optional: inject sandboxed operations
            Read: &tools.ReadToolOptions{Operations: vfsRead},
            Bash: &tools.BashToolOptions{Operations: sandboxBash},
        }
    }))
```

`WithSessionOptions` is the sanctioned hook for tools, models, and stream backends; it runs per
conversation before session construction. `AgentSessionOptions.ToolOptions` (the D27 SDK
divergence) injects per-tool construction options — including `Operations` backends for
VFS/sandboxed execution — into the built-in tools; nil fields keep defaults, and the overrides
survive tool rebuilds. `WithAgentDir` overrides the global agent config directory (default
`~/.pi/agent`).

## Turn ledger and at-least-once semantics

Delivery state lives in the session JSONL itself, as `type:"custom"` entries with custom type
`pigo.chat.turn`, appended via `SessionManager.AppendCustomEntry` (never
`AppendCustomMessageEntry`, which would inject into model context). One marker per phase and
event: `started` (before the user message enters the session), `preview` (with the platform
preview message id), `settled` (outcome + assistant entry id), `delivered` (receipt). Recovery
reads **raw session entries** — compaction hides pre-cut entries from the built context but never
from disk.

On every `Handle`, the ledger for the `EventID` decides the path:

- `delivered` present → duplicate redelivery, no-op.
- `settled` without `delivered` → the reply exists but may not have been sent. The recorded
  assistant text is delivered **without re-prompting**, prefixed `"♻ recovered reply"` — an
  honest possible-duplicate marker. A recorded preview id is resumed so the reply edits the
  existing message instead of sending a new one.
- `started` without `settled` → crashed mid-turn. The partial turn is orphaned via
  `Manager.Branch(startedEntryID)`, the live agent resynced, and the turn re-runs without a
  second `started` marker.

So the guarantee is at-least-once with visible honesty: a crash can duplicate a reply (marked as
recovered), never silently lose one, and never bill a second model call for a settled turn.

**Durability caveat**: the session store persists lazily — on a brand-new conversation, nothing
is written to disk until the first assistant message exists (upstream session-manager parity).
A `started` marker for the very first turn of a fresh conversation is therefore only durable
once the model has replied at least once; ledger guarantees begin there. Every subsequent turn's
markers append durably as they are written.

## Queue seam

The processor consumes messages through a callback, `func(chat.Message) error` — adapters'
ingress publishes into it and treats an error as "redeliver". `chat.NewLocal(handler, spoolPath)`
is the bundled single-process implementation: a durable spool (fsynced JSONL of
`{"m":...}`/`{"ack":...}` lines) plus a keyed FIFO dispatcher with a worker pool — per-key
ordering, at most one in-flight `Handle` per key, retry with capped backoff, replay of unacked
messages and compaction on boot. `*Processor` satisfies the `Handler` interface directly.

In clustered deployments, replace `Local` with a broker: publish normalized `Message` JSON to a
partitioned queue (partition by `Message.Key().String()` to preserve per-conversation FIFO), call
`Processor.Handle` from the consumer, and ack on nil return. The ledger makes redelivery safe.

## Commands

Recognized leading a message: `/stop`, `/new`, `/status`, `/compact` (Telegram `/cmd@botname` is
normalized to `/cmd`).

- `/stop` — preempted in `Handle` before the keyed lock: aborts the key's active run via the
  active-turn registry, notifies "stopped" (or "nothing to stop"). No ledger write.
- `/new` — `Manager.NewSession()`: resets to a fresh session file, prior file untouched.
  Providers backed by harness storage return a notice that `/new` is unavailable.
- `/status` — model, context usage (tokens/window/percent), and summed assistant cost on the
  current branch.
- `/compact` — `Session.Compact` with a tokens-before/after notice.

Commands are events like any other: the same `EventID` dedupe applies. They write only the
`delivered` marker (no `settled`), so a crash between execution and the marker re-runs the
command on redelivery — command turns are **at-least-once**, and a duplicate `/new` or
`/compact` is possible after a crash.

## Telegram (`chat/telegram`)

`telegram.New(telegram.Options{...})` — requires `Token`; `BaseURL`, `HTTPClient`, and the
interval knobs exist for tests and tuning. The client is stdlib `net/http`; every call decodes
the `ok/description/parameters` envelope, honors 429 `parameters.retry_after`, and treats
"message is not modified" as success.

**Ingress** — two mutually exclusive modes, both publishing through the queue callback:

- `Webhook(publish) http.Handler` — rejects requests whose
  `X-Telegram-Bot-Api-Secret-Token` header fails a constant-time compare against
  `Options.Secret` (403); a publish failure answers non-2xx so Telegram redelivers.
- `Poll(ctx, publish) error` — calls `deleteWebhook` first, then a `getUpdates` long-poll loop
  (default 30s hold, `allowed_updates: ["message"]` — edited messages are deliberately ignored).
  The offset advances only after every message of a batch has been published, so a crash before
  the durable enqueue leaves the batch for redelivery. Run one `Poll` per bot account.

Normalization: `EventID` is `tg:<chat_id>:<message_id>` (message identity, not the
delivery-scoped update_id). Albums are buffered per `chat_id:media_group_id` and merged into one
message after a 1.2s window. In groups, only messages that @mention the bot (by entity, with the
mention stripped) or reply to it are published; DMs always trigger. Photos keep only the largest
size; captions become the text.

**Delivery** — `Typing` starts a `sendChatAction` refresher (every 4.5s; the indicator shows ≤5s
per call). `Preview` sends a plain-text message on first render, then edits it in place, skipping
unchanged text and self-rate-limiting per chat (`PreviewMinInterval`, default 1s). `Finalize`
renders markdown to Telegram HTML via a goldmark AST walk (`<>&` escaped, fences to
`<pre><code>`, inline code/bold/italic/strikethrough/links), chunks at 4096 UTF-16 code units at
paragraph and fence boundaries (closing and reopening `<pre>` across splits), edits the preview
with the first chunk and sends the rest; a 400 "can't parse entities" resends that chunk with no
parse mode. Only the first chunk replies to the inbound message; link previews are disabled.
Media downloads go through `getFile` and the file URL (the token is never logged).

## WhatsApp (`chat/whatsapp`)

`whatsapp.New(whatsapp.Options{...})` speaks only the official Business Cloud API (Graph
`v23.0`, BYO credentials) and **refuses to construct** without `Token`, `PhoneNumberID`,
`AppSecret`, and `VerifyToken` — unsigned webhooks are not an option.

**Ingress** — `Webhook(publish) http.Handler`. GET answers the subscribe handshake (echoes
`hub.challenge` iff `hub.mode=subscribe` and the verify token matches, constant time). POST
verifies `X-Hub-Signature-256` (HMAC-SHA256 over the raw body, `hmac.Equal`) **before parsing**,
403 on mismatch; then iterates `entry[].changes[]` with `field == "messages"`. `EventID` is the
wamid — Meta redelivers on non-2xx, and the ledger dedupes. `statuses[]` entries go to
`Options.OnStatus` (`Status` carries wamid, status, timestamp, recipient, errors); callbacks
arrive out of order, so reduce with `StatusRank` (failed > read > delivered > sent).

**Delivery** — final-message-only. The Cloud API cannot edit sent messages, so `Preview` is a
no-op and `PreviewID` returns "" (the processor tolerates preview-less adapters — the coalescer
simply never renders). `Typing` piggybacks on a mark-as-read of the inbound wamid with a
`typing_indicator` (shows ≤25s or until the reply), at most once per turn. `Finalize` converts
markdown to WhatsApp markup (`**b**` → `*b*`, headings to bold, fences kept as monospace
blocks), chunks at 4096 characters, threads the first chunk onto the inbound wamid via
`context.message_id`, and records each response wamid in the receipt.

Error policy: 131047 (outside the 24h customer-service window — free-form messages are refused
until the user writes again; template messages are not implemented) and 190 (bad token) surface
immediately and are never retried; 130429/131048 (rate limits) get bounded backoff retries;
100/131026/131051 fail fast with a clear error. Media downloads resolve the media id to a
short-lived URL and fetch it with the Bearer token, refetching the URL once on expiry.

## Platforms (wave 2)

Sprint 6 (D28) adds five adapters on the same frozen `chat` contracts. All of them speak only
official platform APIs with caller-supplied credentials, drop the bot's own echoes before
publishing, populate `Message.Account` to match `Account()` (multi-account routing relies on
the pair), gate groups on an explicit mention with the mention stripped from `Text` (DMs always
trigger), and normalize `/cmd` commands like the Telegram adapter. Streamed previews land where
Sprint 6 enables them (Slack and Discord); Teams, Messenger, and Google Chat are final-only.

### Slack (`chat/slack`)

`slack.New(slack.Options{...})` requires `Token` (bot `xoxb-`) and `SigningSecret`; the bot
user id used for echo filtering and mention gating is resolved via `auth.test` at startup
unless pre-seeded (`BotUserID`). Ingress is the Events API over `Webhook(publish)`: v0 request
signing with constant-time compare and a replay window (`ReplayWindow`, default 5m), the
`url_verification` challenge, bot-echo drops. Slack requires a 2xx **within 3 seconds**, so the
handler publishes to the durable queue and acks — it never waits on turn processing. `EventID`
is `sl:<channel>:<ts>`, which also collapses the app_mention/message.channels double delivery
of one mention (their event_ids differ; channel+ts do not). Delivery streams: a preview message
is posted and then edited via `chat.update` (`PreviewMinInterval`, default 1s per channel);
once Slack refuses edits (`edit_window_closed`, `cant_update_message`) the turn falls back to
posting new messages. `FormatText` transcodes markdown to mrkdwn and `ChunkText` splits
fence-aware at 4,000 characters (the `msg_too_long` ceiling). Slack has no typing indicator for
bot messages — `Typing` is a no-op; the streamed preview is the activity signal.

### Microsoft Teams (`chat/teams`)

`teams.New(teams.Options{...})` requires `AppID` (outbound OAuth client id, inbound token
audience, and the adapter's `Account()`) and `AppPassword`; `TenantID` selects the
single-tenant token endpoint. Ingress is the Bot Framework webhook over `Webhook(publish)`.
Inbound JWT validation is the trust boundary and is never skippable: RS256 against the cached
JWKS (endorsements filtered), issuer, audience, a 5-minute skew window, and the `serviceUrl`
claim matching `activity.serviceUrl` byte for byte — `New` refuses any configuration that would
disable it. `EventID` is the activity id; `ChatID` is `conversation.id` **verbatim** (the
`;messageid=N` suffix is the thread). Delivery is final-only in every conversation type, with
typing activities refreshed at `TypingInterval` while the turn runs. Final markdown is chunked
at 28,000 UTF-16 code units, paced per conversation, and halved recursively when the connector
returns 413 `MessageSizeTooBig`.

### Discord (`chat/discord`)

`discord.New(discord.Options{Token})`; the bot identity is derived from the token and confirmed
by the gateway READY event. Ingress is not a webhook: `Run(ctx, publish)` maintains a Gateway
websocket session over `chat/internal/wsclient` — hello/heartbeat/identify, READY capture, and
resume-first reconnects with capped backoff (a fresh IDENTIFY is budgeted 1000/day). The
identify intents include MESSAGE_CONTENT and DIRECT_MESSAGES; MESSAGE_CONTENT is privileged,
and close code 4014 surfaces an actionable "enable the Message Content Intent" error
(4004/4012/4013/4014 are fatal, other closes reconnect). `EventID` is
`dc:<channel_id>:<message_id>`; a missing `guild_id` is the DM detector. Delivery: typing POST
refreshed every `TypingInterval` (default 4s; the indicator expires after ~10s), preview edits
via PATCH at `PreviewMinInterval` (default 1.5s), chunking at the 2,000-rune content cap.
Markdown is sent verbatim (Discord renders it natively), and every outbound payload carries
`allowed_mentions: {"parse": []}` so model output can never ping @everyone. REST honors 429
`retry_after`. Group gating uses Discord's structured mention token/list or a reply to the bot;
plain `@username` text is not treated as a mention.

### Facebook Messenger (`chat/messenger`)

`messenger.New(messenger.Options{...})` refuses to construct without `Token` (Page Access
Token), `PageID`, `AppSecret`, and `VerifyToken`. Ingress is the Graph "page" webhook over
`Webhook(publish)`: the hub.challenge handshake and `X-Hub-Signature-256` raw-body HMAC ride
`chat/internal/graphhook` (the WhatsApp idiom), `is_echo` events are dropped. Events only flow
after subscribing **both ways** — the app dashboard webhook fields *and*
`POST /{page-id}/subscribed_apps` with the page token; a missing page subscription is the usual
cause of a silently dead webhook (documented on `New`). Messenger is 1:1 only: every
conversation is a DM and PSIDs are page-scoped, so the key is the (page id, PSID) pair as
Account + ChatID; `EventID` is the message mid. Delivery is final-only — there is no edit API,
`Preview` is a no-op — with typing sustained by `sender_action: typing_on` re-fired every
`TypingInterval` (default 15s) and chunks cut at 1,900 runes under the Send API's 2,000-char
cap. The 24-hour standard-messaging window applies: code 10/2018278 (window expired) and
551/1545041 (person unavailable) surface immediately and are never retried. Delivery/read
watermarks go to `Options.OnWatermark` as `Watermark` values.

### Google Chat (`chat/googlechat`)

`googlechat.New(googlechat.Options{...})` refuses to construct without `ProjectNumber` (the
**numeric** project number — the required audience of every inbound event JWT) and
`CredentialsJSON` (the service-account key that signs the outbound JWT-bearer assertion for the
`chat.bot` scope; RS256 built on the stdlib). Ingress is an HTTP-endpoint Chat app over
`Webhook(publish)`: the inbound bearer JWT is verified against Google's
`chat@system.gserviceaccount.com` JWKS before parsing. The handler acks empty and replies async
via `spaces.messages.create`, avoiding the synchronous endpoint's 30-second/one-message limit.
`argumentText` (mention-stripped) is preferred over `text`; in a SPACE the platform itself only
delivers messages that @mention the app. `EventID` is `message.name`. Delivery is final-only:
`Preview` and `Typing` are no-ops because D28 selects no edit stream and Chat has no typing API.
Final chunks use deterministic client-assigned message ids (≤63 chars, derived from the inbound
event), so a crash retry's create conflict degrades to `PATCH updateMask=text` rather than
duplicating a message. Writes are serialized at 1/s per space, `FormatText` transcodes to the
Chat dialect, and `ChunkText` cuts at 4,000 under the 4,096-character cap.

### Internal helpers

`chat/internal/wsclient` is a hand-rolled RFC 6455 WebSocket client on the standard library
alone — client role only, no extensions, no compression, no subprotocol negotiation: `Dial`,
`Conn.ReadMessage` (exactly one reader; pings are auto-ponged, fragments reassembled, close
frames return a `*CloseError`), and `Conn.WriteText`/`Conn.WriteClose` safe from any goroutine.
Heartbeats are the caller's job — the layer is protocol-only, and after any error the `Conn` is
dead; callers reconnect rather than recover. `chat/internal/graphhook` is the Meta Graph
webhook plumbing shared by WhatsApp and Messenger — `HandleVerify` (the one-time hub.challenge
subscribe handshake) and `ValidSignature` (`X-Hub-Signature-256` raw-body HMAC), both
constant-time via `hmac.Equal` — extracted from `chat/whatsapp` with zero behavior change (its
existing tests pass unmodified). `chat/internal/runechunk` holds the identical readable-boundary
splitter shared by Discord and Messenger, keeping their platform-specific limits in the adapters.
