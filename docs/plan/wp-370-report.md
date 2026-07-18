# Historical WP-370 SDK integration report

Status: **integrated in Sprint 0; Sprint 1 SDK parity remains red**.

## Integrated surface

The historical side ref contributed the public Go `NewAgentSession` facade, SDK documentation,
and 13 offline examples. The integration keeps the already-landed request-auth seam and shares its
default resolver with the CLI, without advancing or claiming verification of the Sprint 3
Codex/Copilot/xAI expansion.

The facade currently covers session construction, persisted-message restoration, model and
thinking selection, built-in/custom tool selection, resource injection, native extension binding,
provider settings, event subscription, and synchronous prompting. Follow-up integration fixes made
CWD and provider session IDs explicit, preserved persisted compaction summaries in provider
context, propagated queue/transport/retry settings, and mapped extension reload starts to
`resources_discover: reload` like upstream.

## Current evidence

- All 13 example packages compile and run to completion against the faux provider when the agent
  directory is isolated.
- The SDK and CLI focused tests are green, including persisted-message projection, provider option
  threading, request-auth resolution, CWD normalization, event-channel race regressions, and
  extension start/discovery reasons.
- `make build test lint`, `make fixtures-check`, the pinned upstream RPC suite (27/27), module
  verification/tidy diff, and all four CGO-free Linux/Darwin amd64/arm64 builds are green on the
  consolidated candidate.
- Examples 06 and 12 no longer import an internal package, so their custom-tool declarations can be
  copied into an external module; an isolated consumer module with a local `replace` builds both.

## Sprint 1 red surface

- Upstream `AgentSessionRuntime` replacement orchestration (`new`, switch/resume, fork/import,
  diagnostics, and recreated cwd-bound services) has no public Go counterpart yet. Example 13 only
  demonstrates manual `SessionRuntime` assembly.
- The examples are runnable SDK sketches, not yet behavior-by-behavior ports of all upstream
  examples. In particular, the auth, session-management, resource-discovery, settings, and runtime
  examples cover narrower paths.
- Default Go resource discovery does not yet include the complete upstream settings/trust/package
  and extension-loading behavior.
- Provider option threading still lacks the upstream HTTP-idle timeout, WebSocket connect timeout,
  and thinking-budget settings.
- `SubscribeChan` is a Go convenience that currently drops events when its buffer is full; upstream
  event subscribers are lossless and ordered.
- The required committed external-module `go get`/build/run smoke and automated execution of all 13
  examples remain open.
- TS-versus-Go SDK/example conformance evidence belongs in `docs/compare/sprint-1.md` and has not
  been produced yet.

These items keep the Sprint 1 SDK and M2 criteria unchecked; this report is integration provenance,
not milestone-completion evidence.
