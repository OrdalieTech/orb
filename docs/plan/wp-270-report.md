# WP-270 compat-family enablement report

Status: **complete**. The Go registry contains every pinned upstream built-in provider except Radius,
all 35 providers resolve to catalog models, and generated metadata matches the pinned provider
constructors and catalog generator for deterministic inputs.

## Implemented surface

The provider registry preserves upstream order, names, base URLs, supported API shapes, OAuth
availability, and API-key environment lookup order. Model availability now uses that registry
instead of a second hand-maintained environment map, so provider-specific names such as `HF_TOKEN`,
`CLOUDFLARE_API_KEY`, and the three Xiaomi token-plan keys resolve exactly as upstream does.

The fixed models.dev snapshot is transformed with upstream's provider corrections, compatibility
flags, thinking-level maps, request headers, costs, context limits, and provider-specific model
filters. Static provider catalogs compare semantically equal to the pinned TypeScript generator.
OpenRouter and Vercel are intentionally generated from the fixed snapshot rather than live external
catalog calls, which makes regeneration deterministic while retaining their full model lists.

Cloudflare's upstream-only request seam is ported directly: account and gateway placeholders are
resolved from provider environment values, and AI Gateway moves the credential into
`cf-aig-authorization` while suppressing the generic authorization headers. The implementation
clones the model and options before applying those changes.

## Conformance evidence

`conformance/extract/f2-providers.ts` imports the real pinned provider constructors, invokes their
auth resolvers with a recording environment, and emits registry plus Cloudflare resolver goldens.
`f2-compat-models.ts` runs the pinned catalog generator over the committed models.dev snapshot and
extracts the representative Together, Z.AI, and Fireworks models. Request fixtures then exercise
those three distinct compatibility classes through the real upstream adapters, while Go tests
compare the resulting registry, model metadata, Cloudflare preparation, and request shapes.

The final candidate passes:

```text
make fixtures-check
make build
make test
make lint
go test -race ./...
go vet ./...
go mod verify
go mod tidy -diff
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```

## Acceptance status

| Criterion | Status | Evidence |
|---|---|---|
| Every upstream provider except Radius resolves in `--list-models` | Passed | exact 35-provider pinned registry fixture; generated catalog contains 1,070 models and at least one model for every registered provider |
| Provider registry parity at the pin | Passed | generated provider constructor/auth fixture compared field-for-field in Go |
| Three representative compat-provider F2 checks | Passed | Together and Z.AI OpenAI-completions requests plus Fireworks Anthropic request/cache behavior generated from upstream |
| Upstream-only provider quirks are mirrored | Passed | Cloudflare URL/auth transformation and catalog metadata/corrections have direct MIRROR rows and pinned fixtures |

No golden was edited by hand, Radius remains excluded by D4, and this WP adds no dependency.
