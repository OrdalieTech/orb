# catalog source snapshots

`api.json` starts from `https://models.dev/api.json`, fetched on 2026-07-26 UTC. To preserve the 0.82.1 extraction state after models.dev moved ahead, `amazon-bedrock/anthropic.claude-opus-5` was removed and `fireworks-ai/accounts/fireworks/models/minimax-m3` input was reduced to `["text"]`. Its SHA-256 is `f062d5cd193bd3fcc0f37ef223db5e3fcf685c50f944452eccb69de18caca4d8`.

`v0.82.1-model-deltas.json` contains 12 representative normalized models extracted from the published `@earendil-works/pi-ai@0.82.1` package. The npm tarball SHA-256 is recorded inside the fixture; the fixture SHA-256 is `260ac7080813d9c0e6f2f9bba58dc1077f881717895bd49cd32416acbbc95ad6`.

`fireworks-ai/accounts/fireworks/models/minimax-m3` is excluded from the parity sample because the upstream source tree at the v0.82.1 tag records input `["text"]`, which F2 pins, while the published npm package records `["text","image"]`; upstream regenerated between tag and publish, and pigo follows the extraction contract.

The live provider listings back the NVIDIA intersection and the OpenRouter/Vercel catalogs (upstream generate-models.ts), all captured by 2026-07-26T13:35:01Z:

- `nvidia-nim.json`: `https://integrate.api.nvidia.com/v1/models`, SHA-256 `7ad635d700d284b37a4c62e66345c9767837a91739880765a470e31ea4d606bf`
- `openrouter.json`: `https://openrouter.ai/api/v1/models`, SHA-256 `52fed51d57bd9a6aedc25fed3fa9584c6af93195888f7fdfd17ce745455ba6e6`
- `vercel.json`: `https://ai-gateway.vercel.sh/v1/models`, SHA-256 `d0566c2fd293fc1ccdb4b46b0456ed46aafc6de7b89ef92402321852afc5619f`

Regenerate `../generated.go` deterministically with `go generate ./ai/models`. Updating a snapshot is a deliberate catalog-sync change (also bump `-generated-at` in `doc.go`); generated Go is never edited by hand.
