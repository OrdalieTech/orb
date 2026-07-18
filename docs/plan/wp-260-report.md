# WP-260 — pi-messages wire shape

Implemented the generic `pi-messages` client from pinned upstream
`packages/ai/src/api/pi-messages.ts` without adding a provider, Radius, OAuth,
or a dependency.

## Delivered

- `ai/api/pimessages.go` sends the exact ordered
  `{model,context,options}` body to `<baseUrl>/messages`, carries request-scoped
  bearer auth and headers, preserves the debug query, payload/response hooks,
  and the legacy `PI_CACHE_RETENTION=long` fallback, then converts serialized
  SSE events into the shared assistant-message event stream.
- The event converter handles text, thinking, signatures, redaction, streaming
  tool JSON, terminal usage/response IDs, gateway rewrite diagnostics, backend
  error events, missing terminal events, structured non-2xx diagnostics, and
  context cancellation. It retains upstream's one-event lookahead of mutable
  partial messages at a response-chunk boundary.
- `ai/api/pimessages_test.go` exercises a complete request and rich streamed
  response through a loopback Go server plus hooks, header overrides,
  structured HTTP errors, backend error events, missing credentials, missing
  terminal events, and cancellation.
- `ai/api/pimessages_live_test.go` provides the Tier-2 streamed tool-call
  round-trip behind `PI_GO_LIVE_TESTS=1` and the generic gateway variables
  `PI_GO_PI_MESSAGES_BASE_URL`, `PI_GO_PI_MESSAGES_API_KEY`, and
  `PI_GO_PI_MESSAGES_MODEL`.
- `conformance/extract/f2-pi-messages.ts` executes the pinned TypeScript
  adapter and generates `pi-messages-requests.json` and
  `pi-messages-streams.json`. A clean second extraction produced no diff; both
  goldens are also byte-identical to the independently completed historical
  WP-260 commit `a53c129`.

The upstream header behavior is intentionally retained: model-level headers
are ignored by this API shape, request headers can override the default
authorization/accept/content-type values, and `null` request headers do not
delete defaults. `[DONE]` is ignored rather than treated as terminal, so a
stream without a serialized `done` or `error` event fails.

## Verification

- `make fixtures` and `make fixtures-check` are green, including byte-exact pi-messages request and stream fixtures plus all existing cross-runtime checks.
- `make build test lint` is green with the CGO-disabled product build, complete race suite, vet, and zero golangci-lint issues.
- CGO-disabled linux/darwin builds pass on amd64 and arm64; `go mod verify`, `go mod tidy -diff`, and `git diff --check` are clean.
- The opt-in live smoke was not run because no generic pi-messages endpoint,
  API key, and model credentials are configured in this environment.

No dependency or `go.mod` change belongs to WP-260.

WP-260 remains `in progress` only for the credential-gated generic gateway live round trip.
