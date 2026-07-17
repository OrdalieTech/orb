# Phase 2 — Provider breadth

WPs here are independent of each other unless noted — good parallel lane. Every provider WP:
request-shaping F2 fixtures + recorded-SSE stream fixtures + opt-in live smoke test, and registry
metadata so `--provider/--model` selection works.

## WP-210 — Anthropic messages + prompt caching

**Upstream refs:** `packages/ai/src/api/anthropic-messages.ts`; caching breakpoints logic;
`PI_CACHE_RETENTION`.

**Scope:** `ai/api/anthropicmessages.go` on `anthropic-sdk-go`: system/tools/messages mapping,
thinking blocks + signatures, image inputs, tool use, `cache_control` ephemeral breakpoints
(system/tools/last-user; short/long TTL), usage extraction incl. cacheRead/cacheWrite/cacheWrite1h,
stop-reason mapping, streaming adaptation.

**Acceptance:** F2 anthropic matrix green (incl. cache-breakpoint placement goldens); thinking
round-trip preserves signatures across turns (F3 scenario on faux-anthropic recording).

## WP-211 — Auth storage + Anthropic OAuth + headless login

**Upstream refs:** `packages/ai/src/auth/` (oauth/anthropic, credential-store),
`packages/coding-agent/src/core/auth-storage.ts`, migrations from legacy `oauth.json`.

**Scope:** `codingagent/config/auth.go` (`auth.json`, 0600, flock, legacy migration; `$ENV` /
`!command` apiKey interpolation), `ai/auth/oauth`: PKCE flow for Claude Pro/Max (authorize URL,
localhost :53692 callback server, manual code-paste fallback), token refresh; headless `pi login
anthropic` / `pi logout` (TUI `/login` arrives Phase 4 on the same core).

**Acceptance:** auth.json written by Go is readable by TS pi and vice versa (fixture: cross-read);
full OAuth flow manually verified once and documented; refresh path unit-tested with a fake token server.

## WP-221 — Gemini (+ Vertex) — gate G2

**Upstream refs:** `packages/ai/src/api/google-generative-ai.ts`, `google-vertex.ts`;
`StringEnum` schema compat helper.

**Scope:** **Gate G2 first**: evaluate `google.golang.org/genai` — if its dependency tree drags
(cloud.google.com/go, websocket, sentencepiece) unacceptably, hand-roll the generative-ai REST/SSE
shape instead (it is a clean JSON API). Record decision in PR + dep table. Then: request shaping
(contents/parts, tools with Google-compatible schemas, thoughtSignature handling, thinking budgets),
streaming adaptation, Vertex variant (auth via google auth libs or ADC only if genai adopted;
otherwise defer Vertex to a follow-up WP and say so).

**Acceptance:** F2 google matrix green; schema conversion handles the StringEnum pattern; G2
decision recorded with measured dep/binary impact.

## WP-231 — Mistral conversations + Azure OpenAI responses

**Upstream refs:** `packages/ai/src/api/mistral-conversations.ts`, `azure-openai-responses.ts`.

**Scope:** Mistral: hand-rolled REST/SSE (no sound official Go SDK) implementing the conversations
shape upstream uses. Azure: parameterization of the responses shape (endpoint/deployment/api-version,
key or token auth) reusing WP-120's implementation.

**Acceptance:** F2 matrices for both green; Azure verified against a live deployment (Ordalie has
one) behind the live-test flag.

## WP-232 — Bedrock converse-stream

**Upstream refs:** `packages/ai/src/api/bedrock-converse-stream.ts`.

**Scope:** `aws-sdk-go-v2` bedrockruntime ConverseStream: credential chain, request shaping
(system/tools/messages, images), stream adaptation, usage/stop-reason mapping. Keep the AWS SDK
import surface confined to this file.

**Acceptance:** F2 bedrock matrix green (request shaping tested without AWS via the SDK's
serializer or recorded HTTP); live smoke behind flag.

## WP-241 — Codex + ChatGPT OAuth; Copilot; xAI

**Upstream refs:** `packages/ai/src/api/openai-codex-responses.ts`,
`auth/oauth/{openai-codex,github-copilot,xai,device-code}.ts`.

**Scope:** codex-responses shape (responses variant + ChatGPT-plan OAuth), github-copilot device-code
flow + its token exchange + copilot API headers, xai OAuth. All flows on the shared device-code/PKCE
helpers from WP-211.

**Acceptance:** F2 codex matrix green; device-code flows unit-tested against fake servers; one
manual end-to-end verification each, documented.

## WP-250 — Model catalog + models.json

**Upstream refs:** `packages/ai/scripts/generate-models.ts`, `src/models-store.ts`,
`packages/coding-agent/docs/models.md`; per-provider `.models.ts` structure.

**Scope:** `go:generate` fetcher: models.dev `api.json` → `ai/models/generated.go` (+ hand-corrections
file, mirroring upstream's correction mechanism); runtime `pi update --models` refresh from
models.dev into `~/.pi/agent` cache; `models.json` overlay (custom providers/models, compat flags,
per-model overrides of built-ins, hot reload semantics); model pattern matching
(`provider/id:thinkinglevel`, `--models` cycling patterns, `--list-models`).

**Fixtures:** catalog snapshot committed as testdata (generation is reproducible from it); pattern-
matching cases extracted from upstream tests.

**Acceptance:** `--list-models` output matches upstream for the same catalog data; a models.json
from upstream docs examples loads identically; regeneration is deterministic given a fixed api.json.

## WP-260 — pi-messages wire shape

**Upstream refs:** `packages/ai/src/api/pi-messages.ts` (POST {model, context, options} → SSE of
serialized assistant-message events).

**Scope:** hand-rolled client for the generic gateway shape (usable via `models.json "api":
"pi-messages"` against any conforming backend — e.g. a future Ordalie gateway). No Radius provider,
no Radius OAuth (ledger).

**Acceptance:** F2 + stream fixtures green against recorded transcripts; round-trips through a tiny
in-test Go server speaking the shape.

## WP-270 — Compat-family enablement

**Upstream refs:** `packages/ai/src/providers/*.ts` for groq, cerebras, xai, deepseek, openrouter,
fireworks, together, huggingface, nvidia, vercel-ai-gateway, cloudflare, zai(+cn), minimax(+cn),
moonshotai(+cn), kimi-coding, github-copilot models, opencode, etc.

**Scope:** data + flags only — registry entries (baseURL, auth kind, compat flags, quirk
corrections) riding the existing shapes; per-provider quirk handling ONLY where upstream has code
for it (port that code, note it in MIRROR.md). Provider list parity check against upstream's
registry at the pin.

**Acceptance:** every upstream provider except Radius resolves in `--list-models`; spot F2 checks
for three representative compat providers (one per quirk class).
