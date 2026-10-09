# Models

Orb ships a built-in model catalog from models.dev and provider catalogs and
lets you add or override models through `models.json`. List what is available with:

```sh
orb --list-models            # all models
orb --list-models anthropic  # filter by provider or substring
```

Select a model with `--model` on the command line or `/model` in the interactive TUI. Once a
provider is authenticated (see [providers.md](providers.md)), its models appear in the list.

## Adding models

For the native CLI, import a model configuration into the database:

```sh
orb storage config import models.json /path/to/models.json
```

File-backed SDK sessions and `--pi-files` mode instead read `~/.orb/agent/models.json`
(or the agent directory selected by `ORB_AGENT_DIR`). Editing that file after native
cutover does not update the database. Each provider entry names an API shape and a
base URL; each model names its provider, context window, and pricing:

```json
{
  "providers": {
    "my-openai-compatible": {
      "api": "openai-completions",
      "baseUrl": "http://127.0.0.1:8099/v1",
      "apiKey": "sk-local",
      "models": [
        {
          "id": "my-model",
          "name": "My Model",
          "reasoning": false,
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        }
      ]
    }
  }
}
```

Supported `api` shapes include `openai-completions`, `openai-responses`, `anthropic-messages`,
`google-generative-ai`, `google-vertex`, `amazon-bedrock`, and the other shapes carried from
upstream pi. Custom `models.json` providers merge over the built-in catalog, so you can override a
built-in model's endpoint or pricing by reusing its id.

## Extension-registered models

Extensions may register providers and models at runtime via `pi.registerProvider(...)`. Those
providers participate in `--list-models` and `/model` exactly like configured ones.

## Offline / cached catalogs

Authenticated providers may refresh a newer catalog and cache it in the selected store
(SQLite for the native CLI, under `~/.orb/agent` for file-backed sessions), so recently
seen models remain listable without a network round trip.
