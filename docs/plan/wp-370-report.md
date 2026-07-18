# Historical WP-370 SDK integration report

Status: **integrated in Sprint 0; Sprint 1 SDK parity remains red**.

## Integrated surface

The historical side ref contributed the public Go `NewAgentSession` facade, SDK documentation,
and 13 offline examples. The integration keeps the already-landed request-auth seam and shares its
default resolver with the CLI, without advancing or claiming verification of the Sprint 3
Codex/Copilot/xAI expansion.

The facade covers session construction, persisted-message restoration, model and thinking
selection, built-in/custom tool selection, resource injection, native extension binding,
provider settings, event subscription, synchronous prompting, and replaceable-session hosting.
Follow-up integration fixes made CWD and provider session IDs explicit, preserved persisted
compaction summaries in provider context, propagated queue/transport/retry/timeout/thinking-budget
settings, and mapped extension reload starts to `resources_discover: reload` like upstream.

## Current evidence

- All 13 example packages compile and run to completion against the faux provider when the agent
  directory is isolated; example 13 now exercises `AgentSessionRuntime` new/switch rebinding.
- The SDK and CLI focused tests are green, including persisted-message projection, provider option
  threading and precedence, request-auth resolution, CWD normalization, lossless event-channel
  saturation/race regressions, and extension start/discovery reasons.
- `AgentSessionRuntime` has characterization coverage for new/resume/fork/import/reload lifecycle,
  setup-before-start, fresh extension instances, stale captured contexts, post-replacement message
  delivery, nested replacement, persisted fork-before-root, and cross-CWD service recreation.
- The pinned TypeScript runtime now generates `WP370Runtime/lifecycle.json`; its new-session
  cancellation, shutdown/invalidation, factory, setup, rebind, `withSession`, and quit ordering is
  green against the Go host without hand-authored expected values.
- `make build test lint`, `make fixtures-check`, the pinned upstream RPC suite (27/27), module
  verification/tidy diff, and all four CGO-free Linux/Darwin amd64/arm64 builds are green on the
  consolidated candidate.
- Examples 06 and 12 no longer import an internal package, so their custom-tool declarations can be
  copied into an external module; an isolated consumer module with a local `replace` builds both.

## Sprint 1 red surface

- Public prompt options, direct agent access, custom/user-message injection, and active-tool
  get/set methods still lack the upstream SDK surface.
- `createAgentSessionServices` and `createAgentSessionFromServices` have no Go counterparts, so
  embedders cannot yet prebuild and reuse the complete cwd-bound service set.
- The examples are runnable SDK sketches, not yet behavior-by-behavior ports of all upstream
  examples. In particular, the auth, session-management, resource-discovery, settings, and runtime
  examples cover narrower paths.
- Default Go resource discovery does not yet include the complete upstream settings/trust/package
  and reloadable extension-loader behavior; the runtime exposes current services and diagnostics,
  but there is not yet a public `ResourceLoader` interface matching upstream.
- Native extension provider registration is not wired through `NewAgentSession`; that model-registry
  contract depends on the WP-520 provider runtime landing.
- The required committed external-module `go get`/build/run smoke and automated execution of all 13
  examples remain open.
- TS-versus-Go SDK/example conformance evidence belongs in `docs/compare/sprint-1.md` and has not
  been produced yet.

These items keep the Sprint 1 SDK and M2 criteria unchecked; this report is integration provenance,
not milestone-completion evidence.
