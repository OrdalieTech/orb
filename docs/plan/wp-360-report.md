# WP-360 pi packages + project trust report

Status: **integrated with its headless acceptance surface green**. `pi install/remove/update/list/config`
manage `npm:`/`git:`/local package sources, packages contribute extensions/skills/prompts/themes
into resolution, and project trust (`trust.json`, `defaultProjectTrust`, `--approve`/`--no-approve`)
gates project settings, project package storage, and project resources. Early loading of
`project_trust` extension handlers remains a resource-loader/JS-bridge integration seam.

## Behavior

`codingagent.PackageManager` is a faithful port of upstream `DefaultPackageManager`: source
parsing (npm specs, the full git URL grammar, local paths), package identity across SSH/HTTPS
forms, global/project dedupe with `autoload:false` deltas, settings persistence with
scope-relative local paths and ref replacement that preserves filters, managed install layouts
(`~/.pi/agent/{npm,git}`, project `.pi/{npm,git}`, hashed 0700 temp dirs for `-e` sources, escape
rejection), git install/reconcile/update/remove via the system git binary (pinned-ref fetch +
reset + `clean -fdx`, `@{upstream}`/`origin/HEAD` targets, empty-parent pruning, `.gitignore`
markers, cloud-sync xattrs), `PI_OFFLINE`, progress events, and the concurrency-4 update/check
pipelines. `Resolve()` reproduces the whole resource machinery — pi manifests with globs and
override patterns, convention directories, package filters (`!`, `+`, `-`, empty-array disable),
ignore files, SKILL.md folder semantics, multi-file extension discovery (`index.ts`, per-directory
manifests), top-level settings entries, trusted/untrusted auto-discovery including `.agents/skills`
ancestors, precedence ranking, and canonical-path dedupe.

npm sources are fetched natively from the registry: abbreviated packument, version selection
(exact / range via the stdlib-only `internal/semver` npm subset / `latest` dist-tag), tarball
download with SRI sha512 (sha1/shasum fallback) verification, and sandboxed extraction stripping
the `package/` prefix. This replaces upstream's npm/bun/pnpm subprocesses per the WP scope — see
divergences below.

Trust is ported from `trust-manager.ts`/`project-trust.ts`: `config.ProjectTrustStore` writes the
exact upstream `trust.json` (sorted keys, two-space indent, trailing newline) under the settings
mkdir-lock protocol shared with TS pi, nearest-ancestor decisions with null deletion,
`GetProjectTrustOptions` (Trust / Trust parent / session-only / Do not trust),
`HasTrustRequiringProjectResources`, and `codingagent.ResolveProjectTrusted` (override → optional
WP-350 runner → saved store → `defaultProjectTrust` → prompt, with headless mode untrusted).
`config.SettingsManager` now carries upstream's `projectTrusted` gating: untrusted managers load
and write no project settings. The CLI (`pi -p`, json, rpc) resolves trust at startup, resolves
packages, and feeds enabled package skills/prompts into resource loading; `pi update` uses saved
trust only, and `install/remove -l` refuse untrusted projects.

## Acceptance checks

- Installing a pi package yields the same resources TS pi sees: the `WP360` fixture family drives
  the pinned `DefaultPackageManager.resolve()` over 13 scenario trees (manifest, convention,
  filtered, npm- and git-installed packages, auto-discovery, symlinks) and the Go runner matches
  the goldens exactly, including enabled flags and `source`/`scope`/`origin`/`baseDir` metadata.
- Trust prompts/gating match upstream in headless modes: ported upstream CLI tests pass
  (`cmd/pi/package_cli_test.go` — untrusted skip, remembered trust, `--approve`/`--no-approve`,
  `defaultProjectTrust`, trust.json override, blocked local writes, fresh-project initialization),
  and trust option/store/format behavior is pinned by upstream-generated goldens.
- `pi install/remove/update/list/config` parse, conflict, help, and error texts follow
  `package-manager-cli.ts`; `update --models` keeps the WP-250 refresh path.

## Divergences (per WP scope / DECISIONS)

- npm operations run against the registry natively; no Node toolchain is invoked. Consequences,
  noted as quirk deltas: the `npmCommand` setting is accepted but unused; runtime `dependencies`
  of installed packages are not auto-installed (`bundledDependencies` ship inside the tarball and
  work; pure-JS deps for bridge extensions are re-evaluated in Phase 5); the legacy npm/pnpm
  global-root migration lookup is not ported; `npm install` after git clone/reconcile is skipped.
- `pi update --self` reports self-update as unavailable and points at GitHub releases (gate G4,
  WP-661). `pi config` parses and trust-gates, then reports that the selector needs the TUI
  (WP-450).
- `hosted-git-info` is replaced by a known-host parser conformance-tested against the pinned
  battery, including upstream's `git://`→`https:////` prefix-eating quirk.
- The resolver and native runner support `project_trust`, but the CLI does not yet bootstrap
  extension handlers before deciding trust. That requires the unified resource-loader/JS-bridge
  path scheduled for Sprint 3; override, saved-decision, default, prompt, and headless behavior are
  already active and fixture-tested.

## Out-of-scope notes for follow-up

- `resources.go` (WP-340) still owns live auto-discovery for skills/prompts; `Resolve()` is the
  upstream-shaped surface and the CLI consumes only its package-origin entries. Unifying the two
  pipelines through `Resolve()` (as upstream's resource-loader does) is proposed for WP-370/390.
- Resolved extension (`.ts`/`.js`) and theme paths are exposed but unconsumed until the JS bridge
  (Phase 5) and theme loading (Phase 4).
- Upstream's `getEnv()` `/proc/self/environ` workaround for empty Node environments is not needed
  in Go and was not ported.

The final candidate passes:

```text
make fixtures-check
make build
make test        # go test -race ./...
make lint        # go vet + golangci-lint, 0 issues
go mod verify    # all modules verified; go mod tidy is a no-op
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```
