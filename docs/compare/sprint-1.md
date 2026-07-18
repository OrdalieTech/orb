# Sprint 1 comparison — core headless

Status: **RED — this is an audit record, not Sprint 1 closure evidence.** The committed headless
surface is green for F2, F6Harness, F7, F8, F9, F10, the retained native-extension fixtures, and
the M2 SDK smoke. Sprint 1 still lacks a pinned-upstream text-mode comparison, the retained
skills/extension/SDK fixture surfaces have documented gaps, trim pass #2 does not exist, and two
credential-dependent M2 checks have no completed run.

## Revisions and method

- pi-go: `91db31eaa9171174a07deaf9d8b6f570b2f6e74a` (`Sprint 1: land harness and headless lifecycle parity`).
- upstream: pi `0.80.10`, `3da591ab74ab9ab407e72ed882600b2c851fae21`, matching both
  `UPSTREAM.lock` and `.upstream/` at audit time.
- The focused reruns below used a `git archive HEAD` snapshot in `/tmp`, so the concurrent Sprint 2
  consolidation in the shared checkout could not change the result. `GOFLAGS=-buildvcs=false` was
  used only where a test-built binary otherwise tried to stamp the archive's intentionally absent
  `.git` directory.
- No live provider or OAuth request was made. Fixture inputs come from the pinned TypeScript
  extractors; the only networked audit command resolved dependencies for the external Go consumer
  smoke.

## Scripted mode comparison

| Mode | TypeScript side | Go side | Result |
|---|---|---|---|
| JSON | `conformance/extract/f3-session.ts` invokes pinned `runPrintMode` and a real upstream `AgentSession`/faux provider in JSON mode. It produces six scenarios and 121 JSONL records; only `session.timestamp` is declared canonicalized. | `TestJSONPrintModeMatchesUpstreamRunPrintModeFixtures` replays all six through Go and byte-compares the complete stream. `TestCLIJSONModeMatchesMultiplePromptFixture` also exercises the CLI route, canonicalizing only the generated header id/time/cwd. | **GREEN.** Multiple prompts, queueing, retry, compaction failure, assistant error, and abort all pass. |
| RPC | `conformance/extract/f7-rpc.ts` drives pinned RPC mode for 40 request steps and records 54 raw JSONL lines with no canonicalization. | `TestF7RPCTranscriptMatchesUpstream` replays the transcript in-process; `TestF7RPCTranscriptReplaysAgainstBinary` replays it through a real `pi-go --mode rpc` binary. | **GREEN.** Every line and LF/CRLF framing case passes. |
| RPC upstream suite | `conformance/extract/run-upstream-rpc-tests.ts` runs six pinned upstream test files unchanged through an adapter that substitutes the Go binary and a local Anthropic SSE endpoint. | Commit `91db31e` records `make upstream-rpc-tests`: 6 files, 27 tests passed. `docs/plan/wp-331-report.md` records 17 process cases that independently exercise Go and ten client/framing/process cases, with no excluded or modified upstream case. | **GREEN.** This audit did not rerun the adapter because it temporarily rewrites files in the shared `.upstream/`; the cited green commit is the local evidence. |
| Print text | Pinned `runPrintMode` prints the final assistant text blocks, or the final error, but no extractor records text-mode output. The current F3-session extractor hard-codes `mode: "json"`. | `TestRunPrintModeTextPromptsSeriallyAndPrintsTextBlocks` passes against a hand-written Go expectation. | **RED.** There is no identical scripted TS-vs-Go text-mode run, so `SPRINTS.md`'s print/json/rpc comparison requirement is not proved. |

Focused rerun output:

```text
$ go test ./cmd/pi -run '^Test(JSONPrintModeMatchesUpstreamRunPrintModeFixtures|CLIJSONModeMatchesMultiplePromptFixture)$' -count=1
ok  github.com/OrdalieTech/pi-go/cmd/pi  0.046s

$ GOFLAGS=-buildvcs=false go test ./conformance/runner -run '^TestF7' -count=1
ok  github.com/OrdalieTech/pi-go/conformance/runner  1.482s

$ go test ./codingagent/modes -run '^TestRunPrintModeTextPromptsSeriallyAndPrintsTextBlocks$' -count=1
ok  github.com/OrdalieTech/pi-go/codingagent/modes  0.016s
```

## M2 criterion audit

| M2 criterion | Status | Local evidence |
|---|---|---|
| F2 for landed core shapes | **GREEN** | OpenAI Responses/Completions, Anthropic, Google, Vertex, Mistral, Azure, Bedrock, and pi-messages request/stream fixture tests pass. Bedrock request shaping initially hit the restricted sandbox's loopback denial; the permitted loopback rerun passed all four cases. Codex results are ignored here under D26. |
| Anthropic OAuth and `auth.json` cross-compat | **OWNER-BLOCKED** | `PI_GO_AUTH_TS_VERIFY=1 TestAuthStorageConformance` passes, including TS reading Go's file and cross-runtime lock behavior. `docs/plan/wp-211-auth-report.md` records that no subscribed Anthropic credential is available, so the required Pro/Max browser login plus streamed request has not run. |
| Harness `SessionRepo`/`FileSystem` parity | **GREEN** | Commit `91db31e` lands FileSystem/ExecutionEnv, storage, memory/JSONL repos, UUIDs, and rehydrate-from-bytes. All `TestF6Harness*` cases pass against `F6Harness`, including repo behavior, context projection, byte rehydration, and Node execution-environment observations. |
| Upstream RPC suite and F7 | **GREEN** | F7's 40 steps/54 lines pass in-process and against the binary; commit `91db31e` and `docs/plan/wp-331-report.md` record the unchanged upstream suite at 27/27 with no exclusions. |
| F8, F9, F10 | **GREEN for the committed fixtures** | Every `TestF8*`, `TestF9*`, and `TestF10*` case passes. This exact release checkbox is satisfied, although the Sprint-level resource surface remains incomplete below. |
| SDK examples and external consumer | **GREEN for the exact M2 wording** | All 13 `codingagent/examples/*` programs ran with isolated agent directories. A fresh external module ran `go get github.com/OrdalieTech/pi-go/codingagent` through a local replace and then `CGO_ENABLED=0 go build ./...`; both exited zero. |
| Nightly live suite wired and running | **OWNER-BLOCKED** | `.github/workflows/nightly-live.yml` and `TestNightlyLiveSuite` wire the exact three-task OpenAI/Anthropic corpus with a $0.25 cap and failure issue creation. There is no completed hosted run locally; `PROGRESS.md` records missing repository/API credentials and CI secrets. “Running” therefore is not proved. |
| Trim pass #2 | **RED** | `docs/trim/M2.md` and a Sprint 1 shrink diff are absent. The 0.84x harness LOC note in `91db31e` covers one package only and is not the seven-part binding trim checklist. |

The focused fixture and SDK commands reported:

```text
$ go test ./ai/api -run '<landed-core-F2-tests>' -count=1
ok  github.com/OrdalieTech/pi-go/ai/api  0.045s
$ go test ./ai/api -run '^TestF2BedrockRequestShaping$' -count=1
ok  github.com/OrdalieTech/pi-go/ai/api  0.016s
$ PI_GO_AUTH_TS_VERIFY=1 go test ./codingagent/config -run '^TestAuthStorageConformance$' -count=1
ok  github.com/OrdalieTech/pi-go/codingagent/config  0.511s
$ go test ./conformance/runner -run '^Test(F6Harness|F8|F9|F10)' -count=1
ok  github.com/OrdalieTech/pi-go/conformance/runner  0.094s
$ go test ./conformance/runner -run '^TestF11(Native|ExtensionWiring)' -count=1
ok  github.com/OrdalieTech/pi-go/conformance/runner  0.034s
```

The example loop printed `PASS` for `01_minimal` through `13_session_runtime`. The external smoke's
`go get` added `github.com/OrdalieTech/pi-go` and its required modules, and its final CGO-disabled
build had empty output and exit status zero.

## Remaining RED test surface

These gaps require upstream-derived tests before implementation, so green retained fixtures must
not be treated as full Sprint 1 parity:

1. **Add text-mode comparison.** Drive pinned TypeScript and the real Go CLI with the same faux
   scripted sessions, record stdout/stderr/exit status, and compare bytes. The current Go-only unit
   expectation cannot close the sprint report.
2. **Expand F8 before fixing resources.** The current `docs/plan/wp-340-report.md` still records
   missing resolved-resource metadata, direct-path precedence, command gating, dedupe/diagnostics,
   empty-description, collision, and harness substitution cases. Neither the F8 extractor nor that
   report has been superseded by a later parity commit.
3. **Expand native-extension conformance before fixing dispatch.** The retained F11-native and
   F11-wire tests pass, but `docs/plan/wp-350-report.md` and `wp-351-report.md` still record uncovered
   runner error/void semantics, trust context/order, provider registration types, input identity,
   panic/registration/nil cases, and several wire-through/lifecycle boundaries. Sprint 3 owns the
   JS bridge; these Go-native core seams belong to Sprint 1.
4. **Finish the SDK facade under a red public-API test.** `docs/plan/wp-370-report.md`, updated after
   runtime integration, still records missing prompt options, direct Agent access, custom/user
   message injection, active-tool get/set, service construction/reuse APIs, `ResourceLoader`, and
   native provider-registration wiring. The runnable-example M2 checkbox passes, but the broader
   Sprint 1 SDK-facade definition does not.
5. **Run trim #2 and obtain owner evidence.** The behavior-neutral shrink diff and `docs/trim/M2.md`
   must land after the code/test gaps are green. Anthropic Pro/Max login and the first hosted
   nightly run remain explicitly owner-blocked rather than waived.

M2 must remain unchecked until those RED items are resolved and the two owner-controlled runs are
recorded. The current objective count is five exact M2 boxes green, two owner-blocked, and one red;
the Sprint-level comparison and completeness gaps above add independent closure blockers.
