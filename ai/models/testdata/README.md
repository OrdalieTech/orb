# catalog source snapshots

`api.json` starts from the response from `https://models.dev/api.json` fetched on 2026-07-18 UTC for WP-250. It includes the four Google/OpenCode source records for `gemini-3.5-flash-lite` and `gemini-3.6-flash` added by upstream v0.81.1; their normalized output is checked against the published `@earendil-works/pi-ai@0.81.1` package. Its SHA-256 is `0983dd51c963749583c8e9b19a5e6e3ea3e17983bd8393c8ac48e332a3935999`.

`v0.81.1-model-deltas.json` contains the ten affected normalized models extracted from that published package. The npm tarball SHA-256 is recorded inside the fixture; the fixture SHA-256 is `3904cd05581edc674f049ae4cfe2323049379a0765d67f67320a7ad2e6caa071`.

The live provider listings back the NVIDIA intersection and the OpenRouter/Vercel catalogs (upstream generate-models.ts), all captured by 2026-07-21T16:28:57.818287377Z:

- `nvidia-nim.json`: `https://integrate.api.nvidia.com/v1/models`, SHA-256 `557d2f7d9f3045867ff82728191d42b099effe1df00af9d393cd3ee821091171`
- `openrouter.json`: `https://openrouter.ai/api/v1/models`, SHA-256 `0168c119f424dd3c6ef135a9c55a99c4d56e9c74f75c2e90011abe2fe735987b`
- `vercel.json`: `https://ai-gateway.vercel.sh/v1/models`, SHA-256 `b802525bcb455bd2aeea54b7bf8ee9e6ead74b5f4672a75ba5f015a2c149238b`

Regenerate `../generated.go` deterministically with `go generate ./ai/models`. Updating a snapshot is a deliberate catalog-sync change (also bump `-generated-at` in `doc.go`); generated Go is never edited by hand.
