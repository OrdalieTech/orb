# WP-510 JavaScript bridge runtime report

Status: **complete**. TypeScript extensions now pass through an embedded esbuild pipeline and run
inside one isolated Sobek VM per extension, with all JavaScript access serialized through that
VM's owner goroutine.

## Behavior

Discovery preserves the pinned loader's project, global, configured, resolved-package, and CLI
order; handles direct JavaScript and TypeScript files, one-level index and package-manifest entry
points, symlinks, missing explicit paths, and first-path deduplication; and excludes project-local
automatic, settings, and package paths until trust is active. Package installation remains the
WP-360 responsibility, so this package accepts its resolved local paths without adding a second
installer.

Esbuild emits one CommonJS artifact for a Node-compatible resolution environment, targets ES2017,
lowers async generators, bundles extension-local pure-JavaScript dependencies, leaves pi and
typebox aliases for the bridge, and embeds source content and maps. Compile errors retain their
original TypeScript location, and Sobek consumes the inline map for runtime stacks. The cache hashes
every bundled source plus package and TypeScript resolution metadata omitted from esbuild's
metafile; unchanged reloads reuse the artifact, while edits to a source dependency or a package
entry point rebuild it.

Each loaded extension has a persistent VM and factory closure, so command and event state survives
within a session but cannot leak to another extension. Factories and callbacks may return
immediately settled promises, including ordinary `async` functions and promise microtasks. Load
failures stay attached to the failing path and do not prevent later extensions from loading.
`Loader.Reload` discards every old VM and registry, rebuilds changed artifacts, and returns a fresh
registry; callbacks retained from the old registry fail against the closed VM.

The bridge surface in this foundation is intentionally limited to the calls required by the two
acceptance examples: `registerTool`, `registerCommand`, `on`, headless `ui.notify`, `defineTool`,
and the `Type.String`/`Type.Object` schema forms used by `hello.ts`. WP-520 owns the complete
ExtensionAPI and typebox binding, while WP-530 owns host-backed asynchronous operations and Node
shims.

## Conformance evidence

`conformance/extract/f11-jsbridge.ts` calls the pinned upstream loader to generate discovery and
error expectations, then copies the upstream `hello.ts` and `pirate.ts` bytes into the fixture
family. The Go tests load those files without modification, execute the hello tool in print mode,
toggle the pirate command, and verify the resulting system-prompt middleware. Additional tests
cover trust exclusion, manifest and symlink discovery, syntax and runtime source mapping, async
factory completion, error isolation, per-extension globals, dependency bundling, cache hits, source
edits, package metadata edits, and fresh VMs on reload.

The final candidate passes:

```text
make fixtures-check
make build test lint
go mod verify
go mod tidy -diff
git diff --check
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```

## Acceptance status

| Criterion | Status | Evidence |
|---|---|---|
| Discovery, settings/package seams, CLI paths, and trust gating | Passed | pinned discovery fixture plus directory, manifest, symlink, deduplication, and untrusted-project tests |
| ES2017 single-artifact build with local dependencies and source maps | Passed | compiler inspection, local `node_modules` execution, and compile/runtime mapped-line tests |
| Isolated Sobek lifecycle and async factory invocation | Passed | per-extension global-state, immediate async factory, error isolation, and race coverage |
| Content-hash cache and fresh reload | Passed | unchanged hit, imported-source edit, package-main edit, and reset-global assertions |
| Unmodified hello and pirate examples function in print mode | Passed | byte-copied upstream fixtures executed through the native registry and runner |

The new direct dependencies are `github.com/grafana/sobek` and `github.com/evanw/esbuild`, both
explicitly approved for `jsbridge` in `ARCHITECTURE.md` section 8. No golden was edited by hand.
