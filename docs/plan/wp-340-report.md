# WP-340 skills, prompt templates, and slash resolution report

Status: **historical integration snapshot**. The gaps described here were open when WP-340 first
landed; current Sprint 1 status and superseding evidence live in `docs/compare/sprint-1.md`.

## Behavior

The coding-agent loader preserves upstream's recursive `SKILL.md` stopping rule, root-markdown
differences between `.pi` and `.agents`, ignore files, symlinks, first-wins collision diagnostics,
and lenient Agent Skills validation. It parses `allowed-tools` and
`disable-model-invocation`; hidden skills remain explicitly invocable but are omitted from the
progressive-disclosure XML. Prompt directories remain non-recursive, frontmatter and description
fallbacks match upstream, and argument expansion is a single pass over positional, wildcard,
default, and slice forms.

The agent harness exposes the same loaders through an execution-environment filesystem seam,
including sourced resources and diagnostics. The phase plan cited harness skills but did not name
the adjacent harness prompt-template module; it landed here because prompt-template plumbing is in
WP-340 scope and `ARCHITECTURE.md` assigns both resource types to the harness.

`AgentSession` prompt submission resolves extension commands before input interception, then skill
commands before templates. Skill invocation rereads the file so edits are visible without reload,
and queued steer/follow-up messages reject extension commands as upstream does. CLI resource and
trust flags plus settings paths feed the same loader; no-skills/no-prompts leave explicit CLI paths
additive.

## Conformance evidence

F8 is generated from the pinned TypeScript core and both agent-harness resource loaders. It records
argument parsing/substitution, loader metadata and ordering, UTF-16 description truncation,
invocation formatting, command ordering, and a real upstream `AgentSession` trace for extension,
input-transform, handled-input, skill/template collision, and unknown-skill fallback cases. F9 now
records the exact skills disclosure block, including XML escaping and hidden-skill exclusion.

The final candidate passes:

```text
make fixtures-check
make build test lint
go mod verify
go mod tidy -diff
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
git diff --check
```

## Status at integration time

| Criterion | Status | Evidence |
|---|---|---|
| Retained F8 expansion and resolution goldens green | Passed | pinned upstream extractor plus Go runner |
| F9 skills disclosure green | Passed | exact upstream system-prompt comparison |
| Complete upstream skill and prompt loading parity | Pending | Sprint 1 expands F8 before repairing ordering, metadata, dedupe, diagnostics, and substitution |
| Discovery, trust, settings, package, extension, and CLI seams are byte-right | Pending | package and extension resource routing remain outside the retained fixture surface |
| Pure-Go build and repository quality gates pass | Passed | race, vet, lint, fixture, module, and four cross-build gates above |

The only new direct dependencies are `github.com/bmatcuk/doublestar/v4` and `gopkg.in/yaml.v3`,
both explicitly approved for skills in `ARCHITECTURE.md` §8. No golden was edited by hand.
