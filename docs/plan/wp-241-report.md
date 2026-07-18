# WP-241 — Codex + ChatGPT OAuth; Copilot; xAI

The implementation follows pinned upstream `3da591ab74ab9ab407e72ed882600b2c851fae21` for the Codex Responses transport and the OpenAI Codex, GitHub Copilot, xAI, and shared device-code OAuth flows. No product dependency was added.

## Delivered

- `ai/api/openaicodexresponses.go` ports the Codex request shape, reasoning and tool conversion, account extraction, forced headers, retry/error behavior, service-tier pricing, SSE terminal handling, payload/response hooks, and upstream's empty-session cache-key behavior. The pure-Go client uses upstream's supported uncompressed fallback path because zstd is unavailable in the standard library and no compression dependency is permitted by `ARCHITECTURE.md` section 8.
- `ai/api/openaicodexresponses_websocket.go` and the stdlib WebSocket codec implement auto/WebSocket/cached transports, connection-limit retry, pre-stream SSE fallback, post-start failure diagnostics, cached-context deltas, and per-session resource cleanup. `SessionRuntime.Dispose` closes the matching cached transport.
- `ai/auth/oauth` ports shared RFC 8628 polling, OpenAI browser/device login and refresh, GitHub Enterprise-aware Copilot device login/token exchange/model enablement, and xAI device login/refresh. Credential JSON preserves upstream provider-specific member order and unknown metadata.
- Provider registration exposes request-scoped OAuth/API-key methods for `openai-codex`, `github-copilot`, and `xai`. Copilot uses Bearer authentication, dynamic initiator/intent/vision headers, token-derived base URLs, and credential-driven model filtering where missing or `null` availability leaves models unfiltered while `[]` filters all.
- `pi login <provider>` now accepts `anthropic`, `openai-codex`, `github-copilot`, and `xai`; logout remains provider-scoped.
- `conformance/extract/f2-codex.ts` invokes the pinned adapters and emits byte-exact request, recorded stream, WebSocket/cache, and provider-definition fixtures. Go consumers replay every generated fixture rather than merely checking extractor output.
- `ai/api/openai_codex_live_test.go` provides the opt-in streamed tool-call round trip behind `PI_GO_LIVE_TESTS=1`, `PI_GO_OPENAI_CODEX_ACCESS_TOKEN`, and an optional model/transport override.

## Upstream behavior notes

- A present `thinkingLevelMap` entry whose value is `null` omits reasoning; it does not fall back to the requested level. An explicitly empty session ID still emits `"prompt_cache_key":""` but emits no session headers.
- Copilot's `/models` fetch alone has a five-second deadline; the OAuth clients do not impose a blanket request timeout. A `proxy-ep` token field is recognized anywhere in the token string.
- xAI verification URLs follow WHATWG control-character normalization before the HTTPS trust check. Its interactive label remains `Sign in with SuperGrok or X Premium`; the Phase-4 selector can consume the concrete optional label without widening the base OAuth interface prematurely.
- The Codex SSE parser intentionally accepts LF-delimited events. The WebSocket path and recorded fixture cover the transport used by current Node upstream; CRLF-specific SSE parity remains characterized by ordinary adapter tests rather than changing the golden.

## Verification

- `make fixtures` followed by a clean `make fixtures-check` is green with the Codex extractor registered in F2.
- Focused API, auth, provider, model-registry, CLI, and session-resource tests pass; the Codex request and stream goldens are byte-/event-exact.
- `go vet ./ai/...`, the repository-pinned golangci-lint over the touched AI packages, and CGO-disabled linux/darwin builds on amd64 and arm64 are green.
- The full repository build/test/lint gate and exact-commit verification are recorded in the WP commit once the concurrent work-package edits are excluded.
- Manual end-to-end OAuth verification for OpenAI/ChatGPT, GitHub Copilot, and xAI, plus the Codex live tool-call smoke, could not run because this environment has no required subscriptions/tokens and interactive credentials. No live output was recorded into fixtures.

WP-241 remains `in progress` only for those credential-gated manual and live checks.
