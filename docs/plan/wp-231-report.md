# WP-231 — Mistral conversations + Azure OpenAI responses

The offline implementation targets upstream `3da591ab74ab9ab407e72ed882600b2c851fae21`. Mistral uses a pure-Go REST/SSE adapter with no product dependency; Azure reuses the Responses conversion and stream processor while supplying Azure endpoint, deployment, API-version, API-key, and retry behavior.

## Coverage

- The F2 extractor covers rich Mistral messages, images, tools, prompt caching, foreign tool-ID normalization, Azure environment and explicit endpoint configuration, deployment mapping, cache-key/token clamps, reasoning controls, and the OpenAI SDK's proxy-query replacement behavior.
- Recorded streams cover Mistral thinking/text/tool interleaving, partial JSON, cache usage and cost accounting, Azure encrypted-reasoning backfill, terminal events, provider HTTP errors, and Mistral's empty-error-body status/content-type message.
- Focused Go tests cover Mistral tool IDs, cache field variants, and simple reasoning selection; Azure base-URL normalization, config precedence, deployment mapping, retries, and reasoning-off behavior; provider tests cover metadata and copy isolation.
- `go.mod` is unchanged. The extractor requires the pinned, development-only `@mistralai/mistralai@2.2.6` package; the shipped Go product remains pure Go.

## Upstream behavior note

The phase plan mentions Azure key or token authentication, but the pinned upstream adapter requires `apiKey` and constructs `AzureOpenAI` without an Azure AD token provider. The port keeps that observable behavior instead of adding an unsupported authentication path.

The OpenAI SDK preserves a query on a non-Azure proxy while normalizing the configured base URL, then replaces that query with `api-version` when it builds a Responses request. If the base URL already contains a query, the appended `/responses` path is discarded with it; the F2 fixture records this quirk.

## Verification

- `make fixtures` and `make fixtures-check` pass, including byte-exact Mistral/Azure requests and streams plus the existing cross-runtime checks.
- `make build test lint` passes with a CGO-disabled product build, the complete race suite, vet, and zero golangci-lint issues.
- CGO-disabled builds pass for linux and darwin on amd64 and arm64; `go mod verify`, `go mod tidy -diff`, and `git diff --check` are clean.
- The Tier-2 Mistral live test is credential-gated by missing `MISTRAL_API_KEY`.
- The Tier-2 Azure live test is credential-gated by missing `AZURE_OPENAI_API_KEY` plus `AZURE_OPENAI_BASE_URL` or `AZURE_OPENAI_RESOURCE_NAME`.

WP-231 remains `in progress` only for those two credential-gated live round trips.
