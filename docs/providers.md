# Providers

pi-go authenticates the same way as upstream pi and shares its on-disk layout: OAuth tokens and
API keys live in `~/.pi/agent/auth.json` (override the agent directory with `PI_CODING_AGENT_DIR`).
A session written by pi-go opens in TS pi and vice versa, so any provider you can reach from
upstream pi you can reach here.

## Subscriptions (OAuth)

Run `pi login` (headless) or `/login` in the interactive TUI, then pick a provider. OAuth-capable
providers:

- `anthropic` — Claude Pro/Max
- `openai-codex` — ChatGPT Plus/Pro (Codex)
- `github-copilot` — GitHub Copilot (press Enter for github.com, or enter an Enterprise domain)
- `xai` — Grok / X subscription

Tokens auto-refresh when they expire. Clear them with `pi logout` / `/logout`.

## API keys (environment variables)

Set the provider's key in the environment before launching pi:

```sh
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
export QWEN_TOKEN_PLAN_API_KEY=sk-sp-...
export QWEN_TOKEN_PLAN_CN_API_KEY=sk-sp-...
```

Every provider in the built-in catalog has a `<PROVIDER>_API_KEY` variable (e.g. `MISTRAL_API_KEY`,
`CEREBRAS_API_KEY`, `OPENROUTER_API_KEY`, `GROQ_API_KEY`). Cloud providers use their native
credential variables — Azure OpenAI (`AZURE_OPENAI_API_KEY`), Amazon Bedrock (`AWS_*`), Google
Vertex (application-default credentials). The full mapping matches upstream pi's
`env-api-keys` table.

## Auth file

Keys and OAuth tokens can also be written directly to `~/.pi/agent/auth.json`:

```json
{
  "anthropic":          { "type": "api_key", "key": "sk-ant-..." },
  "openai":             { "type": "api_key", "key": "sk-..." },
  "qwen-token-plan":    { "type": "api_key", "key": "sk-sp-..." },
  "qwen-token-plan-cn":  { "type": "api_key", "key": "sk-sp-..." }
}
```

OAuth entries carry `{ "type": "oauth", "access": "...", "refresh": "...", "expires": <ms> }` and
are managed by `pi login`.

## Custom providers

Add OpenAI-compatible or other API-shaped providers through `models.json` (see
[models.md](models.md)) or an extension's `pi.registerProvider(...)`. Custom providers can supply
their own `baseUrl`, headers, and auth resolver.

## Resolution order

For a selected model, credentials resolve as: explicit `--api-key` → `auth.json` (API key or
OAuth) → environment variable. The first that yields a usable credential wins; if none do,
pi reports "No API key found" and points you back here.
