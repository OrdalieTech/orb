# catalog source snapshots

`api.json` starts from `https://models.dev/api.json`, fetched on 2026-07-26 UTC. To preserve the 0.82.1 extraction state after models.dev moved ahead, `amazon-bedrock/anthropic.claude-opus-5` was removed and `fireworks-ai/accounts/fireworks/models/minimax-m3` input was reduced to `["text"]`. On 2026-10-22 the `anthropic` entries `claude-fable-5-1`, `claude-haiku-5-5`, `claude-opus-5-5` and `claude-sonnet-5-5` were copied in verbatim from that day's models.dev; nothing else moved. Its SHA-256 is `522eaeaa255c90d5fd0dc41effc7a998d88295979ce501b49b4b22e1ab4c2564`.

The provider listings back the NVIDIA intersection and the OpenRouter/Vercel catalogs (upstream generate-models.ts), all captured by 2026-10-03T18:21:06Z:

NVIDIA/Vercel and the published pi 0.82.1 OpenRouter parity input stay frozen at their 2026-07-26 captures:

- `nvidia-nim.json`: `https://integrate.api.nvidia.com/v1/models`, SHA-256 `7ad635d700d284b37a4c62e66345c9767837a91739880765a470e31ea4d606bf`
- `openrouter.json`: `https://openrouter.ai/api/v1/models`, SHA-256 `52fed51d57bd9a6aedc25fed3fa9584c6af93195888f7fdfd17ce745455ba6e6`
- `vercel.json`: `https://ai-gateway.vercel.sh/v1/models`, SHA-256 `d0566c2fd293fc1ccdb4b46b0456ed46aafc6de7b89ef92402321852afc5619f`
- `openrouter-current.json`: `https://openrouter.ai/api/v1/models`, captured 2026-10-03T18:21:06Z, SHA-256 `991b8191bebb44c1b2340aeb0ddd111c2ae680623dd1aa97ebb51c48706ae4fa`

The build uses `openrouter-current.json`; `openrouter.json` remains the immutable published-release parity input for the generator tests.

Regenerate `../generated.go` deterministically with `go generate ./ai/models`. Updating a snapshot is a deliberate catalog-sync change (also bump `-generated-at` in `doc.go`); generated Go is never edited by hand.
