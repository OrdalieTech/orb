# Models

pigo ships a built-in model catalog (mirrored from upstream pi's generated model registry) and
lets you add or override models through `models.json`. List what is available with:

```sh
pigo --list-models            # all models
pigo --list-models anthropic  # filter by provider or substring
```

Select a model with `--model` on the command line or `/model` in the interactive TUI. Once a
provider is authenticated (see [providers.md](providers.md)), its models appear in the list.

## Adding models

Drop a `models.json` next to your settings (`~/.pi/agent/models.json`, or `.pi/models.json` in a
trusted project) to register custom providers and models. Each provider entry names an API shape
and a base URL; each model names its provider, context window, and pricing:

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

Authenticated providers may refresh a newer catalog and cache it under `~/.pi/agent`, so recently
seen models remain listable without a network round trip.
