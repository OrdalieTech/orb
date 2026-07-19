# Chat gateway

The `chat` package (D27) turns the pi-go SDK into a multi-user messaging agent: a synchronous,
at-least-once turn processor around `codingagent.AgentSession` with normalized platform messages,
a `SessionProvider` ownership seam, and platform adapters in `chat/telegram` and `chat/whatsapp`.
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
| `Adapters` | `[]Adapter` | — | One adapter per platform. Required, unique platforms. |
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
