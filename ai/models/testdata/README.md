# models.dev snapshot

`api.json` is the response from `https://models.dev/api.json` fetched on 2026-07-18 UTC for WP-250. Its SHA-256 is `e38484e40478b751cf89099c336ef05fcab66d4313cf47865d639855c6f277ec`.

Regenerate `../generated.go` deterministically with `go generate ./ai/models`. Updating the snapshot is a deliberate catalog-sync change; generated Go is never edited by hand.
