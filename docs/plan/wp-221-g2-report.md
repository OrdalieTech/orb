# WP-221 G2 report

G2 rejects `google.golang.org/genai` and selects a stdlib REST/SSE adapter for Gemini. The official
SDK crosses the existing binary budget before it provides any pi-go behavior, while the hand-rolled
shape stays within the dependency and size constraints and is byte-checked against upstream.

## Measurement

The probe used Go 1.26 on linux/amd64, `google.golang.org/genai@v1.64.0`, and the same stripped build
command for every binary: `CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' ./cmd/pi`.

| Variant | Binary bytes | Delta | Module entries | Compiled packages |
|---|---:|---:|---:|---:|
| Consolidated base (`813da39`) | 17,907,874 | — | 67 | 294 |
| Official SDK probe | 26,374,306 | +8,466,432 (+47.278%) | 102 | 477 |
| Hand-rolled Gemini adapter | 18,063,522 | +155,648 (+0.869%) | 67 | 294 |

The probe added 35 module entries and 183 compiled packages. Its graph includes the broad Google
Cloud/auth stack plus sentencepiece and websocket dependencies; that cost is disproportionate for
Gemini's JSON request and SSE response shape. The probe was removed, so `go.mod` and `go.sum` have no
WP-221 dependency change.

All three binaries were re-built with one correctly quoted `-ldflags='-s -w'` argument. Repeating
`-ldflags` would retain only the final value, so those incorrectly stripped probes are not used here.

## Consequence

Gemini request shaping, signed-thought replay, tool schemas, thinking controls, stream adaptation,
and provider registration are implemented directly. The phase plan had required a Vertex follow-up
without assigning it an identifier, so WP-221 resolves that gap by scheduling WP-222. That package
will reuse this adapter and implement the API-key/ADC surface without importing the auth stack G2
rejected. M2 still uses its explicit `vertex-or-deferred-per-G2` criterion until WP-222 lands.

The generated F2 corpus compares Google request bytes, selected headers, provider metadata, stream
events, usage/cost accounting, finish reasons, signatures, tool calls, and HTTP errors against the
pinned upstream commit. It includes custom-header precedence, hook-added generation fields,
slash-qualified and case-varied model IDs, UTF-16 tool IDs, nested Gemini 3 image results, empty
signatures, the pinned SDK's multiline-SSE parsing, root-routed safety settings, full post-hook Mldev
config and content-union transforms, `$schema` compatibility, empty tool arrays, SDK-normalized HTTP
errors, and JavaScript-coerced raw JSON error chunks inside successful HTTP streams. `StringEnum` is
covered by the generated request body and an ordinary Go regression test.

## Verification

The generated fixtures reproduce cleanly with `make fixtures-check`. `make build`, `make test`, and
`make lint` pass, including the repository-wide race suite, `go vet`, and golangci-lint. Explicit
`CGO_ENABLED=0 go build ./...` checks pass for linux and darwin on amd64 and arm64. `go mod verify`
and `go mod tidy -diff` are clean against the project module cache, and the focused
Google/provider/config tests pass.

The live tool-call round trip is available behind `PI_GO_LIVE_TESTS=1`; `GEMINI_API_KEY` is absent
from this environment, so the Tier-2 check is credential-blocked and WP-221 remains `in progress`
until it runs. All credential-independent acceptance checks are green.
