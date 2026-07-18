# WP-170 skeleton gate report

Status: **complete**. The required local matrix, live OpenAI tool loop, cross-language session read,
size, cold-start, and both recorded dogfood criteria pass.

## Revisions under test

- pi-go: `3be40ac47b43788e5b798c43666077d62c10931a` (WP-160 parent) + WP-170 candidate
- upstream: pi `0.80.10`, `3da591ab74ab9ab407e72ed882600b2c851fae21`
- toolchain: Go `1.26.2`, Node `24.15.0`, hyperfine `1.18.0`
- host: Linux `6.8.0-88-generic`, amd64, AMD Ryzen 5 PRO 3600, 12 logical CPUs

## Integrated skeleton

The binary now connects the OpenAI Responses and Completions adapters, the agent loop, all phase-1
tools (`read`, `write`, `edit`, `ls`, `grep`, `find`, and `bash`), settings and prompt
assembly, print-mode argument/input processing, and v3 session persistence. Print mode records and
continues sessions, preserves coding-agent messages in state, and converts them only at the model
boundary.

Expected phase-1 limits remain explicit: only the OpenAI provider is selectable, print mode emits
text only, and interactive TUI, provider breadth, OAuth, compaction orchestration, RPC/JSON modes,
skills, extensions, MCP, and image support belong to later scheduled WPs.

## Integration fallout fixed

- `json.RawMessage` clones retained the underlying bytes but lost their named type, so a resumed
  branch summary encoded as a base64 JSON string. The clone now preserves the named type, with an
  aliasing and dynamic-type regression test plus the full TypeScript-session CLI resume test.
- v1/v2 session rewrites now use JavaScript `JSON.stringify` number spelling, integer-key order,
  whitespace, escaping, and lone-surrogate behavior instead of Go encoder defaults.
- Session reads use Node's streaming UTF-8 replacement semantics, and AI/coding-agent string fields
  preserve lone surrogates as WTF-8. Upstream-generated F1 and F6 fixtures cover both behaviors.
- Print-mode validation now matches upstream's ordering for parser errors, version/help, unknown
  long flags, empty selection values, API keys, and model restoration. Custom messages persist as
  `custom_message` session entries instead of being forced into the standard message union.
- Print mode owns only upstream's SIGTERM/SIGHUP cleanup path. SIGINT remains process-level, and
  detached children are killed before agent cancellation.
- Terminal detection uses `golang.org/x/term`; the former filename heuristic did not match actual
  pipe and terminal behavior.

## Conformance and build evidence

The following commands pass on the revision above:

```text
make fixtures-check
PI_GO_F6_TS_VERIFY=1 go test ./conformance/runner -run '^TestF6' -count=1 -v
OPENAI_API_KEY=<redacted> PI_GO_LIVE_TESTS=1 PI_GO_OPENAI_MODEL=gpt-4o-mini \
  go test ./ai/api -run '^TestOpenAIResponsesLiveToolCallRoundTrip$' -count=1 -v
make build test lint
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build ./...
done
```

This covers F1, F2 (OpenAI), F3, F4, F5, F6, and F9. Fixture regeneration is clean; F6 includes
upstream-TS-written sessions read by Go and a Go-written session opened by the pinned upstream
`SessionManager`. The final bounded live adapter smoke completed a real two-turn Responses API
exchange with an `echo` tool in 2.05 seconds. The full Linux gate and all four CGO-free cross-build
targets are green. This repository has only the local `gb-local` remote, so hosted GitHub Actions
and a native macOS test job were unavailable and are not claimed here.

## Size and cold start

Both artifacts were built from the clean revision with `CGO_ENABLED=0` and `-buildvcs=false`, so
their hashes are reproducible instead of depending on GitButler's internal snapshot revision:

| Artifact | Build flags | Bytes | MiB | SHA-256 |
|---|---|---:|---:|---|
| default | `go build -trimpath -buildvcs=false` | 20,742,813 | 19.782 | `ea6a1936065090e004d52238eaa09ae464666e50b0577859b69ac9984fd811b7` |
| stripped | `go build -trimpath -buildvcs=false -ldflags='-s -w'` | 14,581,922 | 13.906 | `eb1c56cdec73fb977aa777aa705ae570610e4e14af717f12481fe3465ca149c7` |

Cold start used the default artifact, a warm disk cache, no shell, 20 warmups, and 100 measured
runs. The command initializes the complete phase-1 runtime but supplies no prompt, so it performs
no network request:

```text
hyperfine -N --warmup 20 --runs 100 \
  "/usr/bin/env -C <empty-project> PI_CODING_AGENT_DIR=<empty-agent-dir> \
   <artifact-dir>/pi-default -p --no-session --model gpt-5.1"
```

Mean was **4.1 ms** (standard deviation 0.3 ms, range 3.8-5.3 ms).
The default binary is 19.782 MiB. Both measurements are comfortably inside the 50 ms and 25 MB
gate budgets.

## Sample-repo live tool loop

The clean default artifact ran in print mode against the real OpenAI Responses API with
`gpt-5-nano`, minimal thinking, a fresh agent directory, and only `read`, `edit`, and `bash` enabled.
The existing environment supplied the API key only to the process; neither the key nor an
authorization field appears in the recorded session. The bounded task was:

```text
WP-170 dogfood: You must use read to inspect main.go, then use edit to add exactly one useful Go
comment immediately above Add, then use bash to run gofmt on main.go and go test ./.... Report what
changed and the test result.
```

The model called `read`, then `edit`, then `bash`; `main.go` gained exactly this line and the sample
test passed:

```go
// Add returns the sum of left and right. This function is used in tests to verify basic arithmetic.
```

The first two bash attempts nested `bash -lc` inside the already shell-backed tool. The transcript
keeps that model behavior rather than cleaning it up; a constrained continuation then emitted the
exact argument `gofmt -w main.go && go test ./...`, which returned
`ok example.com/wp170sample (cached)`. Independent `gofmt -d main.go` produced no diff, and an
independent `go test ./...` passed.

Exactly one v3 session was written. The pinned upstream TypeScript `SessionManager` opened the same
file as session `019f72a7-f634-7725-9d56-962a4328553d`, reconstructed all 18 entries, selected leaf
`01eddb3a`, and observed a final assistant `stop`. The byte-for-byte
[dogfood transcript](artifacts/wp-170-dogfood.jsonl) is 16,943 bytes with SHA-256
`06977ac32232e5d6440d5456632079d1a61bc47d105b419b1fb40f3ec8f2813f`.

## Self-dogfood on WP-170

The candidate binary separately performed a real WP-170 task in this repository: it read this
report, edited its stale revision, size, hash, and cold-start evidence, and invoked targeted Go
tests. This is distinct from the sample-repo task above, which proves the required generic tool
loop but does not count as work on pi-go itself.

The model's first bash argument included a sentence-ending `.` as an extra root package. The three
requested packages passed, while the nonexistent root package made that combined invocation return
1. The transcript preserves the mistake. A constrained continuation in the same session then
called exactly `go test ./cmd/pi ./codingagent/session ./conformance/runner`; all three packages
passed.

The pinned upstream TypeScript `SessionManager` opened the resulting v3 session
`019f72b2-c4d5-738e-bf90-23aa8eab68f1`, reconstructed all 26 entries, selected leaf `1d4fa631`,
and observed a final assistant `stop`. The byte-for-byte
[self-dogfood transcript](artifacts/wp-170-self-dogfood.jsonl) is 59,321 bytes with SHA-256
`f81b323dc92f4b1a2367961e0eafb2d23edbcabaf5a9ddffd1e700c788acf497`. Neither transcript contains
an API key or authorization value.

## Acceptance status

| Criterion | Status | Evidence |
|---|---|---|
| Real OpenAI round-trip with tool calls | Passed | live adapter smoke plus `read`/`edit`/`bash` binary run |
| Recorded v3 session opens in TS pi | Passed | direct F6 verification plus the exact live session opened upstream |
| F1/F2/F3/F4/F5/F6/F9 and matrix | Passed locally | Linux race/vet/lint plus all four cross-builds; no hosted remote configured |
| Cold start < 50 ms; binary < 25 MB | Passed | 4.1 ms; 19.782 MiB default |
| One real pi-go WP dogfood transcript | Passed | WP-170 report correction, targeted tests, and raw self-dogfood v3 transcript |

No local criterion is waived; the unavailable hosted/native-macOS evidence is stated explicitly.
