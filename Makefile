UPSTREAM_REPO := $(shell sed -n 's/.*"repo": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
UPSTREAM_COMMIT := $(shell sed -n 's/.*"commit": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
GOLANGCI_LINT_VERSION ?= v2.7.2
GOLANGCI_LINT := $(CURDIR)/.tools/bin/golangci-lint
GO_ENV := GOCACHE=$(CURDIR)/.tools/cache/go-build GOMODCACHE=$(CURDIR)/.tools/cache/go-mod
LINT_ENV := $(GO_ENV) GOLANGCI_LINT_CACHE=$(CURDIR)/.tools/cache/golangci-lint

.PHONY: build test lint upstream fixtures fixtures-check ensure-upstream-fixture-tools upstream-rpc-tests sync sync-bump

build:
	$(GO_ENV) CGO_ENABLED=0 go build ./...

test:
	$(GO_ENV) go test -race ./...

lint: $(GOLANGCI_LINT)
	$(GO_ENV) go vet ./...
	$(LINT_ENV) $(GOLANGCI_LINT) run

$(GOLANGCI_LINT):
	mkdir -p $(dir $@)
	$(GO_ENV) GOBIN=$(dir $@) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

upstream:
	@if [ ! -d .upstream/.git ]; then git clone $(UPSTREAM_REPO) .upstream; fi
	@if ! git -C .upstream cat-file -e '$(UPSTREAM_COMMIT)^{commit}' 2>/dev/null; then git -C .upstream fetch origin $(UPSTREAM_COMMIT); fi
	@git -C .upstream checkout --detach $(UPSTREAM_COMMIT)
	@test "$$(git -C .upstream rev-parse HEAD)" = "$(UPSTREAM_COMMIT)"

ensure-upstream-fixture-tools: upstream
	@if [ ! -x .upstream/node_modules/.bin/tsx ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/vitest/package.json").version' 2>/dev/null)" != "4.1.9" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/partial-json/package.json").version' 2>/dev/null)" != "0.1.7" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/typebox/package.json").version' 2>/dev/null)" != "1.1.38" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/openai/package.json").version' 2>/dev/null)" != "6.26.0" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/@anthropic-ai/sdk/package.json").version' 2>/dev/null)" != "0.91.1" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/@aws-sdk/client-bedrock-runtime/package.json").version' 2>/dev/null)" != "3.1048.0" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/@smithy/node-http-handler/package.json").version' 2>/dev/null)" != "4.7.3" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/http-proxy-agent/package.json").version' 2>/dev/null)" != "7.0.2" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/https-proxy-agent/package.json").version' 2>/dev/null)" != "7.0.6" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/@google/genai/package.json").version' 2>/dev/null)" != "1.52.0" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/@mistralai/mistralai/package.json").version' 2>/dev/null)" != "2.2.6" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/diff/package.json").version' 2>/dev/null)" != "8.0.4" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/cross-spawn/package.json").version' 2>/dev/null)" != "7.0.6" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/yaml/package.json").version' 2>/dev/null)" != "2.9.0" ] || \
		[ "$$(node -p 'require("./.upstream/node_modules/undici/package.json").version' 2>/dev/null)" != "8.5.0" ]; then \
		cd .upstream && npm install --ignore-scripts --no-save --workspaces=false \
			tsx@4.22.1 vitest@4.1.9 partial-json@0.1.7 typebox@1.1.38 openai@6.26.0 @anthropic-ai/sdk@0.91.1 \
			@aws-sdk/client-bedrock-runtime@3.1048.0 @smithy/node-http-handler@4.7.3 http-proxy-agent@7.0.2 https-proxy-agent@7.0.6 @google/genai@1.52.0 @mistralai/mistralai@2.2.6 diff@8.0.4 cross-spawn@7.0.6 \
			chalk@5.6.2 get-east-asian-width@1.6.0 glob@13.0.6 highlight.js@10.7.3 hosted-git-info@9.0.3 \
			ignore@7.0.5 jiti@2.7.0 marked@18.0.5 minimatch@10.2.5 proper-lockfile@4.1.2 semver@7.8.0 \
			undici@8.5.0 yaml@2.9.0; \
	fi

fixtures: ensure-upstream-fixture-tools
	@cd .upstream && node --import tsx ../conformance/extract/generate.ts ../conformance/fixtures $(UPSTREAM_COMMIT)

fixtures-check: ensure-upstream-fixture-tools
	@fixture_tmp=$$(mktemp -d); \
		trap 'rm -rf "$$fixture_tmp"' EXIT; \
		cd .upstream && node --import tsx ../conformance/extract/generate.ts "$$fixture_tmp" $(UPSTREAM_COMMIT); \
		cd ..; \
		diff -ru conformance/fixtures "$$fixture_tmp"
	@PI_GO_F6_TS_VERIFY=1 $(GO_ENV) go test -race ./conformance/runner -run TestF6SessionWriteAndProjectionMatchUpstream
	@PI_GO_AUTH_TS_VERIFY=1 $(GO_ENV) go test -race ./codingagent/config -run TestAuthStorageConformance

upstream-rpc-tests: ensure-upstream-fixture-tools
	@mkdir -p .tools/bin
	@$(GO_ENV) CGO_ENABLED=0 go build -o .tools/bin/pi-go-rpc-test ./cmd/pi
	@cd .upstream && node --import tsx ../conformance/extract/run-upstream-rpc-tests.ts ../.tools/bin/pi-go-rpc-test

sync: ensure-upstream-fixture-tools
	$(GO_ENV) CGO_ENABLED=0 go run ./internal/sync/cmd/pisync --dry-run $(SYNC_ARGS)

sync-bump: ensure-upstream-fixture-tools
	$(GO_ENV) CGO_ENABLED=0 go run ./internal/sync/cmd/pisync --bump $(SYNC_ARGS)
