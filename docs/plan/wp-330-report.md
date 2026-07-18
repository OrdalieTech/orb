# WP-330 — JSON mode

WP-330 ports pinned upstream's JSON print mode as an LF-delimited, byte-compatible AgentSessionEvent stream. The CLI accepts `--mode json`, keeps stdout protocol-clean, reads piped stdin into the first prompt, processes later prompts serially, and retains upstream's zero exit status when the final assistant message reports `error` or `aborted`.

## Delivered

- `codingagent/modes/print.go` emits the session header first, subscribes before prompting, serializes every session event in callback order, and closes only after in-flight callbacks and queued writes finish. SIGINT/SIGTERM kill tracked detached children, abort the session, unsubscribe, and drain the serializer.
- `codingagent/session_events.go` covers the AgentSession-only wire events needed by JSON mode alongside the existing queue, retry, and compaction events. The runtime uses an injected millisecond clock for deterministic queued messages.
- `cmd/pi` routes JSON mode through the existing resume/fork/session and provider-auth paths. Help and model-list text move to stderr whenever stdout is the JSON protocol surface; RPC remains an explicit WP-331 error.
- The JSONL fixture reader requires LF framing through EOF, rejects blank/CRLF/trailing/multiple values, and returns the original line bytes for exact comparisons. Session headers are decoded with unknown fields forbidden, validated for version/order/encoding and millisecond UTC timestamps, and only CLI-generated ID/timestamp/cwd values are canonicalized.

## Conformance

`conformance/extract/f3-session.ts` invokes the pinned TypeScript `runPrintMode` over a real upstream `AgentSession` and faux provider. It generates six traces: multiple prompts, steering plus follow-up queues, provider retry, manual short-session compaction, assistant error, and assistant abort. IDs and cwd are fixed; only `session.timestamp` is canonicalized to the fixed extractor clock and that field is declared in the manifest.

The Go consumer replays every scenario through `SessionRuntime` and compares the complete JSONL stream byte for byte. A separate CLI replay compares the multiple-prompt trace after strict header validation, while ordinary integration tests cover piped stdin, resume/fork parent headers, help/model-list routing, serializer drain, and signal teardown.

The short-session fixture exposed a parity gap in `PrepareCompaction`: Go attempted an empty summary where upstream returns `Nothing to compact (session too small)`. The preparation step now returns no work when neither discarded history nor a split-turn prefix exists, matching the pinned source and golden without changing the fixture.

## Verification

- Direct pinned F3-session extraction completed successfully, and a fresh independently generated F3-session directory is byte-identical to the committed family.
- `go test -race ./agent/harness ./codingagent/modes ./codingagent ./conformance/runner ./cmd/pi -count=1` passes.
- `go vet ./...` passes with the repository-local Go caches, and golangci-lint reports `0 issues`.
- `CGO_ENABLED=0 go build ./...` passes for linux and darwin on amd64 and arm64.
- The full sandboxed repository race invocation reached all WP-330 packages green, but the repository-wide run was interrupted by sandbox-denied loopback listeners and concurrent model-catalog work; the root integration gate owns the clean repository-wide rerun.

No dependency or `go.mod` change belongs to WP-330.
