# WP-222 Vertex report

WP-222 completes the G2 deferral with a stdlib REST/SSE Vertex adapter and pure-Go request-scoped
ADC. It adds no module, keeps `CGO_ENABLED=0`, and reuses the WP-221 message conversion and stream
processor while preserving the Vertex-specific SDK transformations and thinking rules.

## Fidelity surface

The generated F2 corpus runs the pinned TypeScript adapter and `@google/genai` request transformer,
then compares exact Go request bytes, selected headers, provider metadata, login and auth-resolution
results, and stream events. Its ten request cases cover API-key express mode, custom collection base
URLs and API-version suppression, signed-thought and tool replay, image and function-result parts,
StringEnum schemas, custom-header precedence, payload hooks, and the Vertex-only configuration
surface. Seven recorded streams cover text, thinking, signatures, tool calls, usage, finish reasons,
malformed chunks, and HTTP errors. Upstream-mapped Go unit cases separately cover ADC regional and
multi-region endpoints, placeholder keys, cached-content paths, and unsupported-field diagnostics.

The request transformer follows the pinned SDK's field insertion order and its asymmetric behavior:
Vertex retains fields that the Gemini endpoint strips, rejects the Vertex-unsupported tool/part
fields even when their values are `null`, moves configuration fields to the REST wire locations,
and uses Vertex's disabled-thinking and Gemini 2.5 Flash Lite budgets. Generated `{location}` base
URLs are deliberately ignored, custom collection URLs do not receive a project prefix, and API-key
cached-content names retain upstream's literal `projects/undefined/locations/undefined` quirk.

## Authentication

Explicit non-placeholder keys use the `X-Goog-Api-Key` express endpoint. The pure-Go ADC dispatcher
covers well-known and explicit files, authorized-user and external-authorized-user refresh tokens,
service-account RS256 JWT exchange, recursively impersonated service accounts, external workload and
workforce accounts (file, URL, X.509 certificate/mTLS, executable, and AWS sources), STS and optional
IAM service-account impersonation, plus Compute metadata discovery/token exchange. Fake transports,
temporary credential sources, and controlled executable processes verify exact token forms, JWT
header/claims/signatures, SigV4 requests, quota-project forwarding, metadata detection modes and
flavor headers, rotated refresh tokens, five-minute refresh boundaries, concurrent refresh
deduplication, retry status/backoff behavior, cancellation, and malformed credentials.

Provider login and resolution match the generated upstream fixture for API keys, `gcloud` ADC, and
service-account files, including lookup order and missing-configuration behavior. The CLI runtime now
carries request-scoped auth environment, headers, and base-URL overrides through both ordinary agent
requests and compaction/branch-summary completion calls without mutating process-global environment.

## Dependency and binary impact

Both binaries use Go 1.26 on linux/amd64 and the same command:
`CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' ./cmd/pi`.

| Variant | Binary bytes | Delta | Module entries | Compiled packages |
|---|---:|---:|---:|---:|
| Consolidated pre-WP-222 main (`f59b211`) | 18,063,522 | — | 67 | 294 |
| Vertex REST/SSE + pure-Go ADC | 18,456,738 | +393,216 (+2.177%) | 67 | 294 |
| Rejected official SDK probe from WP-221 | 26,374,306 | +8,310,784 vs pre-WP-222 | 102 | 477 |

The adapter adds no dependency or compiled package. The final binary remains 7,917,568 bytes smaller
than the rejected SDK probe, so WP-222 does not change the G2 decision.

## Verification

`make fixtures` and `make fixtures-check` reproduce cleanly. The focused adapter, provider, runtime,
agent, session-runtime, and configuration tests pass with `CGO_ENABLED=0`; deterministic ADC tests
also pass under the race detector. Repository-wide build, test, lint, race, cross-platform, module,
and fixture checks are recorded in the WP commit after the final audit.

The live tool-call round trip is behind `PI_GO_LIVE_TESTS=1`. This environment has neither
`GOOGLE_CLOUD_API_KEY` nor an ADC project/location/credential configuration, so the Tier-2 run is
credential-blocked and WP-222 remains `in progress`; every credential-independent acceptance check
is green.
