UPSTREAM_REPO := $(shell sed -n 's/.*"repo": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
UPSTREAM_COMMIT := $(shell sed -n 's/.*"commit": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
UPSTREAM_DIR ?= $(CURDIR)/.upstream
UPSTREAM_READONLY ?= 0
GOLANGCI_LINT_VERSION ?= v2.7.2
GOLANGCI_LINT := $(CURDIR)/.tools/bin/golangci-lint
ifeq ($(CI),true)
GO_ENV :=
else
GO_ENV := GOCACHE=$(CURDIR)/.tools/cache/go-build GOMODCACHE=$(CURDIR)/.tools/cache/go-mod
endif
LINT_ENV := $(GO_ENV) GOLANGCI_LINT_CACHE=$(CURDIR)/.tools/cache/golangci-lint

.PHONY: check build test lint nightly-live upstream product-assets product-assets-check fixtures fixtures-tui fixtures-check ensure-upstream-fixture-tools upstream-rpc-tests sync sync-bump

# The canonical gate (upstream's `npm run check` norm): run after any code change.
check: build lint test

build:
	$(GO_ENV) CGO_ENABLED=0 go build ./...

test:
	$(GO_ENV) CGO_ENABLED=1 go test -race ./...
	# Race instrumentation suppresses arm64 FMA contraction, so a -race-only gate
	# cannot see wire-format drift in the shipped CGO_ENABLED=0 build. Re-run the
	# byte-compared surfaces in the shape users actually get.
	$(GO_ENV) CGO_ENABLED=0 go test ./ai/... ./conformance/runner/...

lint: $(GOLANGCI_LINT)
	# internal/chromalexers is vendored chroma/v2/lexers kept byte-close to
	# upstream; its unkeyed Rule literals trip vet's composites check. Same
	# exclusion as .golangci.yml (which plain `go vet` cannot read).
	$(GO_ENV) go vet $$($(GO_ENV) go list ./... | grep -v /internal/chromalexers)
	$(LINT_ENV) $(GOLANGCI_LINT) run

nightly-live:
	$(GO_ENV) CGO_ENABLED=0 ORB_NIGHTLY_LIVE=1 go test -v -count=1 -timeout=20m ./agent -run '^TestNightlyLiveSuite$$'

$(GOLANGCI_LINT):
	mkdir -p $(dir $@)
	$(GO_ENV) GOBIN=$(dir $@) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

upstream:
	@if [ "$(UPSTREAM_READONLY)" != "1" ]; then \
		if [ ! -d "$(UPSTREAM_DIR)/.git" ]; then git clone $(UPSTREAM_REPO) "$(UPSTREAM_DIR)"; fi; \
		if ! git -C "$(UPSTREAM_DIR)" cat-file -e '$(UPSTREAM_COMMIT)^{commit}' 2>/dev/null; then git -C "$(UPSTREAM_DIR)" fetch origin $(UPSTREAM_COMMIT); fi; \
		git -C "$(UPSTREAM_DIR)" checkout --detach $(UPSTREAM_COMMIT); \
	fi
	@test "$$(git -C "$(UPSTREAM_DIR)" rev-parse HEAD)" = "$(UPSTREAM_COMMIT)"

product-assets: upstream
	@node conformance/extract/materialize-product-assets.ts "$(UPSTREAM_DIR)" "$(CURDIR)"
	@gzip -9 -n -c agent/modes/assets/CHANGELOG.md > agent/modes/assets/CHANGELOG.md.gz

product-assets-check: upstream
	@cmp "$(UPSTREAM_DIR)/packages/coding-agent/CHANGELOG.md" agent/modes/assets/CHANGELOG.md

ensure-upstream-fixture-tools: upstream
	@if [ ! -x "$(UPSTREAM_DIR)/node_modules/.bin/tsx" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/vitest/package.json").version' 2>/dev/null)" != "4.1.9" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@xterm/headless/package.json").version' 2>/dev/null)" != "5.5.0" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/partial-json/package.json").version' 2>/dev/null)" != "0.1.7" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/typebox/package.json").version' 2>/dev/null)" != "1.3.7" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/openai/package.json").version' 2>/dev/null)" != "6.26.0" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@anthropic-ai/sdk/package.json").version' 2>/dev/null)" != "0.91.1" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@aws-sdk/client-bedrock-runtime/package.json").version' 2>/dev/null)" != "3.1048.0" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@smithy/node-http-handler/package.json").version' 2>/dev/null)" != "4.7.3" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/http-proxy-agent/package.json").version' 2>/dev/null)" != "7.0.2" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/https-proxy-agent/package.json").version' 2>/dev/null)" != "7.0.6" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@google/genai/package.json").version' 2>/dev/null)" != "1.52.0" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@mistralai/mistralai/package.json").version' 2>/dev/null)" != "2.2.6" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/diff/package.json").version' 2>/dev/null)" != "8.0.4" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/cross-spawn/package.json").version' 2>/dev/null)" != "7.0.6" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/yaml/package.json").version' 2>/dev/null)" != "2.9.0" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/@silvia-odwyer/photon-node/package.json").version' 2>/dev/null)" != "0.3.4" ] || \
		[ "$$(node -p 'require("$(UPSTREAM_DIR)/node_modules/undici/package.json").version' 2>/dev/null)" != "8.5.0" ]; then \
		if [ "$(UPSTREAM_READONLY)" = "1" ]; then \
			echo "upstream fixture tools are missing from read-only $(UPSTREAM_DIR)" >&2; exit 1; \
		fi; \
		cd "$(UPSTREAM_DIR)" && npm install --ignore-scripts --no-save --workspaces=false \
			tsx@4.22.1 vitest@4.1.9 @xterm/headless@5.5.0 partial-json@0.1.7 typebox@1.3.7 openai@6.26.0 @anthropic-ai/sdk@0.91.1 \
			@aws-sdk/client-bedrock-runtime@3.1048.0 @smithy/node-http-handler@4.7.3 http-proxy-agent@7.0.2 https-proxy-agent@7.0.6 \
			@mistralai/mistralai@2.2.6 @google/genai@1.52.0 diff@8.0.4 cross-spawn@7.0.6 \
			chalk@5.6.2 get-east-asian-width@1.6.0 glob@13.0.6 highlight.js@10.7.3 hosted-git-info@9.0.3 \
			ignore@7.0.5 jiti@2.7.0 marked@18.0.5 minimatch@10.2.5 proper-lockfile@4.1.2 semver@7.8.0 \
			@silvia-odwyer/photon-node@0.3.4 undici@8.5.0 yaml@2.9.0; \
	fi

fixtures: ensure-upstream-fixture-tools product-assets
	@cd "$(UPSTREAM_DIR)" && node --import tsx "$(CURDIR)/conformance/extract/generate.ts" "$(CURDIR)/conformance/fixtures" $(UPSTREAM_COMMIT)

# Regenerate the Orb-owned TUI render snapshots (D35): the F12* families and
# the WP450 replay/UI-demo render files rewrite from Orb's own renderer, then
# a comparison pass proves the tree is self-consistent. Behavior-shaped values
# in those files are frozen upstream captures and are never rewritten.
fixtures-tui:
	@ORB_UPDATE_F12=1 $(GO_ENV) CGO_ENABLED=0 go test -count=1 ./conformance/runner -run 'TestF12'
	@ORB_UPDATE_F12=1 $(GO_ENV) CGO_ENABLED=0 go test -count=1 ./agent/modes -run 'TestF12|TestWP450'
	@$(GO_ENV) CGO_ENABLED=0 go test -count=1 ./conformance/runner ./agent/modes ./agent -run 'TestF12|TestWP450|TestSnapshotCodec'

# The reciprocal TS-reads-Go gates run first: as the last command of its recipe
# line, a fixture diff aborts the target, which previously skipped them silently.
# Linux-only in practice (as in CI): F9 writes AGENTS.md and AGENTS.MD as
# distinct files, which a case-insensitive macOS volume collapses.
# The Orb-owned render snapshots (D35) are excluded from the upstream
# extraction diff and guarded by their Go comparison tests instead: snapshot
# drift fails here, regeneration is the explicit `make fixtures-tui`.
fixtures-check: ensure-upstream-fixture-tools product-assets-check
	@ORB_F6_TS_VERIFY=1 $(GO_ENV) CGO_ENABLED=1 go test -race ./conformance/runner -run TestF6SessionWriteAndProjectionMatchUpstream
	@ORB_AUTH_TS_VERIFY=1 $(GO_ENV) CGO_ENABLED=1 go test -race ./agent/config -run TestAuthStorageConformance
	@$(GO_ENV) CGO_ENABLED=0 go test -count=1 ./conformance/runner -run 'TestF12|TestSnapshotCodec'
	@$(GO_ENV) CGO_ENABLED=0 go test -count=1 ./agent/modes -run 'TestF12|TestWP450'
	@$(GO_ENV) CGO_ENABLED=0 go test -count=1 ./agent -run 'TestF12'
	@fixture_tmp=$$(mktemp -d); \
		trap 'rm -rf "$$fixture_tmp"' EXIT; \
		cd "$(UPSTREAM_DIR)" && node --import tsx "$(CURDIR)/conformance/extract/generate.ts" "$$fixture_tmp" $(UPSTREAM_COMMIT); \
		diff -ru -x 'F12*' -x 'WP450' "$(CURDIR)/conformance/fixtures" "$$fixture_tmp"

upstream-rpc-tests: ensure-upstream-fixture-tools
	@mkdir -p .tools/bin
	@$(GO_ENV) CGO_ENABLED=0 go build -o .tools/bin/orb-rpc-test ./cmd/orb
	@cd "$(UPSTREAM_DIR)" && node --import tsx "$(CURDIR)/conformance/extract/run-upstream-rpc-tests.ts" "$(CURDIR)/.tools/bin/orb-rpc-test"

sync: ensure-upstream-fixture-tools
	$(GO_ENV) CGO_ENABLED=0 go run ./internal/sync/cmd/orbsync --dry-run $(SYNC_ARGS)

sync-bump: ensure-upstream-fixture-tools
	$(GO_ENV) CGO_ENABLED=0 go run ./internal/sync/cmd/orbsync --bump $(SYNC_ARGS)
