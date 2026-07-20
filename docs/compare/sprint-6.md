# Sprint 6 comparison — chat platform wave 2 (D28)

Status: **GREEN for the D28 reference cross-check**. Like Sprint 5, this sprint has no upstream
TS-pi counterpart — the five adapters and three `chat/internal/` helpers are pi-go additions
(divergence ledger, D28). The comparison baseline is the same two references: earendil-works/
pi-chat (TypeScript pi extension; its Discord implementation is the named reference for this
wave) and the Hermes messaging gateway docs, plus the per-platform wire briefs captured for the
sprint. Every adoption, improvement, and deliberate difference below is recorded against them.

## Revisions and method

- pi-go base: the Sprint 5 closure; candidate: the Sprint 6 closure containing this report.
- References inspected directly: pi-chat `9adbd29` (`src/live/discord.ts`,
  `src/render/{format,chunking,streaming}.ts`) and Hermes `9de9c25f6`
  (`plugins/platforms/{discord,slack,teams,google_chat}/adapter.py`), plus the five wire briefs.
- Verification is `go test -race ./chat/...` with `httptest` fake platform servers, a fake
  hijacked websocket server, and a scripted fake Discord gateway — never `conformance/`, no
  live network in the default suite. The graphhook extraction is proven by `chat/whatsapp`'s
  existing tests passing unmodified.

## Adopted per platform

| Platform | From the references | pi-go form |
|---|---|---|
| Slack | Hermes: streaming via message editing, one bubble edited in place | Preview message + `chat.update` edits ≥1s/channel (`chat/slack/delivery.go`); fallback to new messages on `edit_window_closed`/`cant_update_message` |
| Slack | Publish-and-ack ingress (Events API 3s deadline) | `Webhook` publishes to the durable queue synchronously and acks; a publish failure answers 500 so Slack redelivers |
| Teams | Bot Framework typing and message activities | Typing refresh while the turn runs, then final-only markdown in every conversation type, matching D28 |
| Discord | pi-chat: gateway ingress, mention-gated guilds, chunked sends, actionable 4014 hint | `chat/discord`: same posture over the hand-rolled gateway session; the "enable the Message Content Intent" error message idea is kept |
| Discord | Hermes: typing refresh + edit-in-place streaming | Typing POST every ~4s, preview PATCH edits at ≥1.5s |
| Messenger | Hermes WhatsApp-Cloud stance: refuse unsigned webhooks, final-only, policy errors never retried | `messenger.New` refuses without `AppSecret`; `Preview` no-op; 10/2018278 (24h window) and 551/1545041 fail fast, only rate limits get bounded backoff |
| Google Chat | Chat API idiom: async reply, `argumentText` over `text` | Sync 200-body reply never used (it can never be edited); replies via `spaces.messages.create`; mention-stripped `argumentText` preferred |
| All | Wave-1 lessons: sent-chunk progress tracking, token redaction, "not modified" as success | Every `Finalize` resumes at the first unsent chunk on processor retry; clients redact tokens from errors/logs |

## Deliberately improved over the references

| Reference behavior | pi-go behavior |
|---|---|
| pi-chat Discord sends model output verbatim with no `allowed_mentions` — a model-emitted `@everyone` pings the whole server | Every outbound payload carries `allowed_mentions: {"parse": []}` (`chat/discord/delivery.go`) — mass mentions are disarmed |
| pi-chat identifies with only Guilds + GuildMessages + MessageContent — DMs never arrive | Identify intents include DIRECT_MESSAGES (37377 total); DM ingress works and is tested |
| pi-chat's edit-in-place preview stack is dead code (`syncPreview` defined, never called); live behavior is typing + one final send | Edit streaming is wired for the two D28 preview platforms: Slack `chat.update` and Discord PATCH |
| pi-chat sends unescaped legacy markdown; Hermes documents no per-platform transcoding for these platforms | Per-dialect transcoding with golden tests: markdown → mrkdwn (`slack.FormatText`), → Teams markdown subset, → Chat dialect (`googlechat.FormatText`); Slack escapes the `&`/`<`/`>` mrkdwn control characters; Discord passes markdown verbatim because the platform renders it natively |
| pi-chat: no 429 handling anywhere | Discord REST honors 429 `retry_after`; Slack/Teams/Messenger/Google Chat apply the shared bounded-backoff policy for retryable codes |
| Hermes: no inbound dedupe documented | Platform-stable `EventID`s feed the turn ledger: `sl:<channel>:<ts>` (also collapses the app_mention/message.channels double delivery), `dc:<channel>:<message>`, activity id, mid, `message.name` |
| discord.js / Bot Framework SDK / googleapis dependencies in the references | Zero new go.mod dependencies: stdlib REST clients, `chat/internal/wsclient` (hand-rolled RFC 6455), stdlib-built RS256 for the Google service-account assertion and both JWT validators |

## Deliberate differences (both references)

| Difference | Rationale |
|---|---|
| Teams, Messenger, and Google Chat are final-only | This is D28's explicit scope. Teams and Messenger keep native typing signals; Google Chat has none. Google final chunks retain deterministic ids, so final-only does not sacrifice crash idempotence. |
| No typing where the platform lacks it: Slack and Google Chat `Typing` are no-ops | Slack has no bot typing API (the streamed preview is the signal); Google Chat has no typing indicator at all. No emulation. |
| Official APIs only — no bridges | No Socket Mode alternative transports beyond what each official API requires; no self-bot or scraping paths, matching D27's Baileys exclusion. |
| E2EE Matrix (and Signal/iMessage/personal WhatsApp) stay excluded | D27/D28: bridge-based and E2EE platforms are out; later official-API waves (Instagram DM, Line, Twilio, Mattermost, Rocket.Chat, Zulip, IRC) are recorded, not scheduled. |
| Google Chat final writes are serialized at 1/s/space | The API quota is shared across threads; deterministic client-assigned ids make create conflicts safely editable after a crash. |
| Discord REST retry policy is `retry_after`-only, no bucket tracking | Simple policy, `ponytail:` ceiling in the adapter; a single bot conversation flow never approaches bucket complexity. |
| Discord chunking ignores code fences (2,000-rune hard cuts inside a >2,000-rune fence) | `ponytail:` accepted ceiling — Discord renders the split fence tolerably and the cap is too small for fence gymnastics to pay. |
| pi-chat also treats plain `@username` text as a mention | pi-go accepts Discord's structured `<@id>` tokens, `mentions` array, and replies to the bot; plain text is not a platform mention, so the legacy username fallback and its mutable username state are absent. |
| Teams inbound JWT validation is constructor-enforced, never skippable | The references treat webhook auth as configuration; here a config that would disable the RS256/issuer/audience/serviceUrl chain is refused at `New`. Same stance for Google Chat's audience + service-account key. |
| Messenger `subscribed_apps` is documented, not automated | The one-time page subscription is an operator act with the page token; the constructor comment names it because a missing subscription is the usual silently-dead webhook. |

## Verification

```text
CGO_ENABLED=0 go build ./...
go test -race ./chat/...   # per-adapter fake servers: signature/JWT rejection matrices,
                           # ingress goldens (echo drops, mention stripping, ChatType, EventID),
                           # delivery sequences (typing → [Slack/Discord preview →] final,
                           # final-only adapters, chunk resume),
                           # rate-limit backoff, attachment auth; wsclient protocol suite
                           # (handshake, masking, 16/64-bit lengths, fragmentation, ping/pong,
                           # close, oversize) and the scripted gateway (hello→identify→READY→
                           # dispatch→ack-loss→resume); whatsapp tests unmodified post-graphhook
gofmt -l . ; go vet ./...
```

Zero new `go.mod` entries; `conformance/` untouched.
