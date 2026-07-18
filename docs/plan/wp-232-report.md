# WP-232 — Bedrock converse-stream

WP-232 ports the pinned Bedrock adapter through the official Go v2 Bedrock Runtime client. The AWS import surface is confined to `ai/api/bedrockconversestream.go`; provider registration, credential discovery, model availability, and unified dispatch remain SDK-neutral.

## Acceptance evidence

- Generated F2 requests cover Claude system and user cache points, one-hour cache TTLs, text and image content, signed reasoning replay, grouped tool results, tool choice, request metadata, fixed and adaptive thinking, GovCloud display omission, and Nova's empty inference configuration. Recorded stream items cover thinking signatures, text, partial tool JSON, usage and cost, tool-use and context-length stops, an unknown stop reason, and an invalid message role.
- Offline transport tests cover static SigV4 credentials, bearer authentication, dummy SigV4 for `AWS_BEDROCK_SKIP_AUTH`, regional and ARN endpoint resolution, scoped proxy and `NO_PROXY` handling, HTTP/1 forcing, reserved custom headers, payload and response hooks, and raw gateway error bodies. The float-to-SDK boundary rejects fractional or out-of-int32 token counts instead of truncating them.
- Provider tests cover the pinned bearer-token, AWS-profile, and existing-credential-chain login flows. Resolution tests cover stored bearer/profile credentials and every ambient upstream source, with a stored profile propagated as request-scoped environment through the shared provider-auth resolver.
- The opt-in live test performs a real streamed two-turn tool-call round trip when `PI_GO_LIVE_TESTS=1`. On 2026-07-18 this shared workspace had no Bedrock bearer token, AWS profile, IAM key pair, ECS credential URI, or web-identity token file, so the required Tier-2 live call is credential-blocked and WP-232 remains `in progress`.

## Dependency and verification record

The approved dependency row now names the direct modules used by the official client: `aws-sdk-go-v2 v1.42.1`, `config v1.32.30`, `credentials v1.19.29`, `bedrockruntime v1.55.1`, and `smithy-go v1.27.3`. `go mod tidy` is clean, and no direct dependency outside `ARCHITECTURE.md` section 8 was added.

The reconciled shared workspace passed:

- `make fixtures` and `make fixtures-check`, including all Bedrock request, stream, provider-login, and cross-runtime checks.
- `make build test lint`, including the complete race suite, vet, and golangci-lint 2.7.2 with zero issues.
- `CGO_ENABLED=0 go build ./...` for linux/amd64, linux/arm64, darwin/amd64, and darwin/arm64.
- `go mod verify`, `go mod tidy -diff`, and `git diff --check`.
