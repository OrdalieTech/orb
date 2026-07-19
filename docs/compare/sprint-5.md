# Sprint 5 comparison — chat gateway (D27)

Status: **GREEN for the D27 reference cross-check**. Unlike sprints 1–4, this sprint has no
upstream TS-pi counterpart to byte-compare — `chat/` is a pi-go addition (divergence ledger,
D27). The comparison baseline is the two reference implementations named by the sprint:
earendil-works/pi-chat (TypeScript pi extension, Telegram + Discord) and the Hermes messaging
gateway (behavior per its published docs, fetched 2026-07-19). Every adoption, improvement, and
deliberate difference below is recorded against those references.

## Revisions and method

- pi-go base: `d0cdeab` (Sprint 4 closure, `v0.1.0`); candidate: the Sprint 5 closure containing
  this report.
- References: pi-chat source readout and Hermes gateway documentation, both captured 2026-07-19.
- Verification is plain `go test` under `chat/` with `httptest` fake platform servers and a faux
  scripted-stream provider — never `conformance/` (F-families are upstream-extraction-only by
  contract), and no live network in the default suite.

## Adopted from pi-chat

| Behavior | pi-go form |
|---|---|
| Typing-indicator refresh during the turn (pi-chat: every 4s) | `chat/telegram/delivery.go` — `sendChatAction` refresher every 4.5s until Finalize/Notify (indicator shows ≤5s per call) |
| Media-group (album) debounce merge, 1200 ms per `chat_id:media_group_id` | `chat/telegram/ingress.go` `mediaGroups` — same 1.2s default window, parts merged into one `Message` with every attachment and the first caption |
| Offset semantics: never re-fetch acknowledged updates; durable enqueue before advancing | `Poll` advances `offset = last update_id + 1` only after every message of the batch (albums included) has been published to the durable queue; a crash before that redelivers the batch |
| deleteWebhook before long-polling (pi-chat only does this in its setup TUI, flagged in its own code as worth moving into connect) | `Poll` calls `deleteWebhook` first, every time |
| Largest photo size only; captions as text; `getFile` → file-URL download | `chat/telegram/ingress.go` `attachmentsOf`, `chat/telegram/telegram.go` `Download` |
| Group gating: DMs always trigger, groups require addressing the bot | Adopted, but by Telegram **entity** offsets (`mention`/`bot_command`), not pi-chat's text regex (which misses mention entities and matches inside code spans) |

pi-chat behaviors examined and **not** adopted: arm-after-catchup backlog suppression (the
durable spool + ledger dedupe make backlog replay safe instead), the per-conversation tmux/VM
process model (one process, keyed mutexes), `[uid:ID]` transcript convention (plain
`Name: text` attribution for group senders), the secrets side-channel, and outbound file
attachment (`chat_attach`) — tools are off by default in v1.

## Adopted from Hermes

| Behavior | pi-go form |
|---|---|
| "Honest at-least-once": durable delivery ledger, crash-recovered sends visibly marked | Turn ledger (`pigo.chat.turn` markers) + `"♻ recovered reply"` prefix on any settled-but-not-delivered replay (`chat/processor.go` `recoveredReplyPrefix`) |
| Control-command bypass of the busy guard | `/stop` is intercepted in `Handle` before the keyed lock and aborts the active run via the active-turn registry; no ledger write |
| Refuse unsigned webhooks outright (Hermes Cloud API: 503 without App Secret) | `whatsapp.New` returns an error without `AppSecret`; POSTs failing `X-Hub-Signature-256` verification get 403 before parsing |
| WhatsApp Cloud is final-message-only (no edit streaming) | `Preview` is a no-op returning `""`; typing rides a mark-as-read of the inbound wamid; one final chunked send with `context.message_id` threading |
| 24h-window and token errors surface immediately, never retried | Graph 131047/190 fail fast with operator-readable hints; only 130429/131048 get bounded backoff (`chat/whatsapp/whatsapp.go` `retryable`, `errorHint`) |
| Markdown → WhatsApp markup (`**b**` → `*b*`), 4096-char chunking | `chat/whatsapp/format.go` `FormatText`, `ChunkText` |

## Deliberately improved over pi-chat

| pi-chat behavior | pi-go behavior |
|---|---|
| Legacy `parse_mode: "Markdown"` with **zero escaping** and no fallback — a bad-markdown 400 fails the whole job | goldmark AST walk to Telegram HTML with `<>&` escaping (`chat/telegram/format.go`); a 400 "can't parse entities" resends that chunk with no parse mode instead of failing the turn |
| Format-blind chunking — splits can land inside a code fence and break the parse mode | Fence-aware chunking at 4096 **UTF-16 code units** (the unit Telegram counts), splitting at paragraph/fence boundaries and closing/reopening `<pre>` across splits, with golden tests under `chat/telegram/testdata/` |
| Full edit-in-place preview stack exists but is dead code — `syncPreview` is never called; live behavior is typing + one final send | Preview streaming is actually wired: the coalescer holds the latest snapshot, a per-turn renderer edits the preview message on a cadence, and Finalize edits the preview with the final first chunk |
| No 429/`retry_after` handling anywhere; any Telegram error is a thrown generic | Every Bot API call decodes `ok/description/parameters`, sleeps `parameters.retry_after` on 429, and treats "message is not modified" as success; preview edits are additionally self-rate-limited per chat |
| Crash mid-turn loses the trigger (in-memory pending queue, never re-queued from the log) | Durable spool replays unacked messages on boot; the turn ledger resumes crashed turns without re-prompting settled ones |

## Deliberate differences (both references)

| Difference | Rationale |
|---|---|
| FIFO turns per conversation key; no message merging into a waiting prompt | The keyed mutex + external queue is the whole concurrency model; simpler than pi-chat's job records or Hermes's two-level guard. `ponytail:` marker in `chat/processor.go`. |
| No steer-into-active-run (Hermes `interrupt`/`steer`/`queue` busy modes) | Only Hermes's `queue` semantics are kept; mid-turn injection is deferred until users demand it. |
| `edited_message` ignored (`allowed_updates: ["message"]`) | pi-chat re-ingests edits as fresh messages and can re-trigger; a stable `EventID` dedupe and edit-retriggering don't compose honestly. v1 drops them. |
| No voice STT/TTS (Hermes: whisper STT, native voice-bubble TTS) | Voice notes arrive as attachment notes to the prompt; no audio pipeline in scope. |
| No Baileys bridge — official WhatsApp Cloud API only | BYO-credential, ban-risk-free path only (Hermes itself calls Cloud the "production-grade path"); no Node subprocess, no QR pairing. |
| Ledger in the session JSONL, not SQLite (Hermes `state.db`) or a separate channel log (pi-chat `channel.jsonl`) | The session file is already the durable, branch-aware history; `AppendCustomEntry` markers keep one source of truth and zero new storage. Caveat documented in `docs/chat.md`: a brand-new session file only flushes from the first assistant message, so ledger durability begins there. |
| Command turns are at-least-once (only a `delivered` marker; no `settled`) | A crash between command execution and the marker re-runs the command on redelivery — honest duplicate over silent loss, same stance as recovered replies. |
| No reactions-as-status, no `/approve`/`/deny`/`/model`/pairing surface (Hermes) | Command surface is the minimal `/stop` `/new` `/status` `/compact`; authorization is a single injected `Authorize` func. |

## Verification

```text
CGO_ENABLED=0 go build ./...
go test -race ./chat/...        # processor crash-boundary replays, 1,000 concurrent turns over
                                # ~100 keys, idle-residue zero, fake Bot API / Graph API servers,
                                # chunk goldens, secret/HMAC rejection, offset redelivery
go test -race ./codingagent/    # ToolOptions hook: injected operations used + survive rebuild
gofmt -l . ; go vet ./...
```

Zero new `go.mod` entries (goldmark was already in the module); `conformance/` untouched.
