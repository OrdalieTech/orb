# catalog source snapshots

`api.json` is the models.dev v2 catalog, `https://models.dev/experimental/v2.0/api.json`, captured 2026-11-02T11:53Z (full capture SHA-256 `883e2d5215a682d37354c21301699f036fe2c8a1f4c57a6d8b9f4bb95f232cb1`). It is trimmed to the 34 providers pi 1.5.0's catalog generator reads, with every kept provider verbatim; pi's generator output is byte-identical from the full and the trimmed file. The trimmed file's SHA-256 is `a1cdf824d9b04413bb6640adb5d109361de7ed50390b66edcfe68c172b19e033`.

The provider listings back the NVIDIA intersection and the OpenRouter/Vercel catalogs (upstream catalog/sources). NVIDIA/Vercel and the published pi 0.82.1 OpenRouter parity input stay frozen at their 2026-07-26 captures:

- `nvidia-nim.json`: `https://integrate.api.nvidia.com/v1/models`, SHA-256 `7ad635d700d284b37a4c62e66345c9767837a91739880765a470e31ea4d606bf`
- `openrouter.json`: `https://openrouter.ai/api/v1/models`, SHA-256 `52fed51d57bd9a6aedc25fed3fa9584c6af93195888f7fdfd17ce745455ba6e6`
- `vercel.json`: `https://ai-gateway.vercel.sh/v1/models`, SHA-256 `d0566c2fd293fc1ccdb4b46b0456ed46aafc6de7b89ef92402321852afc5619f`
- `openrouter-current.json`: `https://openrouter.ai/api/v1/models`, captured 2026-10-03T18:21:06Z, SHA-256 `991b8191bebb44c1b2340aeb0ddd111c2ae680623dd1aa97ebb51c48706ae4fa`

The build uses `openrouter-current.json`; `openrouter.json` remains the immutable published-release parity input for the generator tests and the F2 extraction.

Regenerate `../generated.go` deterministically with `go generate ./ai/models`. Updating a snapshot is a deliberate catalog-sync change (also bump `-generated-at` in `doc.go`); generated Go is never edited by hand.
