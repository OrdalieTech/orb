# Providers

Orb supports Pi-compatible authentication and session formats without sharing Pi's files.
The native CLI stores credentials and sessions in `~/.orb/state/orb.db`; `ORB_STATE_HOME`
overrides its directory. File-backed SDK sessions and explicit `--pi-files` mode use
`~/.orb/agent/auth.json` (`ORB_AGENT_DIR` overrides the agent directory).
Transfer sessions through `orb storage export <id> <file.jsonl>` and
`orb storage import <file.jsonl>`, not by pointing Pi at Orb's live database.

## Subscriptions (OAuth)

Run `orb login` (headless) or `/login` in the interactive TUI, then pick a provider. OAuth-capable
providers:

- `anthropic` — Claude Pro/Max
- `openai-codex` — ChatGPT Plus/Pro (Codex)
- `github-copilot` — GitHub Copilot (press Enter for github.com, or enter an Enterprise domain)
- `xai` — Grok / X subscription
- `openrouter` — OpenRouter credits through a PKCE-minted, user-controlled API key
- `kimi-coding` — Kimi for Coding through device authorization

Tokens auto-refresh when they expire. OpenRouter instead mints a non-expiring API key. Clear
stored credentials with `orb logout` / `/logout`.

## API keys (environment variables)

Set the provider's key in the environment before launching orb:

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

For the native CLI, prefer `orb login`. To supply a credential document explicitly, save
this JSON in a private file and run `orb storage config import auth.json <file>`.
After native cutover, editing `~/.orb/agent/auth.json` does not update the database.
File-backed SDK sessions and `--pi-files` mode read that file directly (keep it mode 0600):

```json
{
  "anthropic":          { "type": "api_key", "key": "sk-ant-..." },
  "openai":             { "type": "api_key", "key": "sk-..." },
  "qwen-token-plan":    { "type": "api_key", "key": "sk-sp-..." },
  "qwen-token-plan-cn":  { "type": "api_key", "key": "sk-sp-..." }
}
```

OAuth entries carry `{ "type": "oauth", "access": "...", "refresh": "...", "expires": <ms> }` and
are managed by `orb login`.

## Custom providers

Add OpenAI-compatible or other API-shaped providers through `models.json` (see
[models.md](models.md)) or an extension's `pi.registerProvider(...)`. Custom providers can supply
their own `baseUrl`, headers, and auth resolver.

## Resolution order

For a selected model, credentials resolve as: explicit `--api-key` → stored credentials
(API key or OAuth, in SQLite or the selected file store) → environment variable. The first that yields a usable credential wins; if none do,
orb reports "No API key found" and points you back here.
