UPSTREAM_REPO := $(shell sed -n 's/.*"repo": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
UPSTREAM_COMMIT := $(shell sed -n 's/.*"commit": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
GOLANGCI_LINT_VERSION ?= v2.7.2
GOLANGCI_LINT := $(CURDIR)/.tools/bin/golangci-lint
GO_ENV := GOCACHE=$(CURDIR)/.tools/cache/go-build GOMODCACHE=$(CURDIR)/.tools/cache/go-mod
LINT_ENV := $(GO_ENV) GOLANGCI_LINT_CACHE=$(CURDIR)/.tools/cache/golangci-lint

.PHONY: build test lint upstream fixtures fixtures-check ensure-upstream-tsx sync

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

ensure-upstream-tsx: upstream
	@if [ ! -x .upstream/node_modules/.bin/tsx ]; then cd .upstream && npm install --ignore-scripts --no-save --workspaces=false tsx@4.22.1; fi

fixtures: ensure-upstream-tsx
	@cd .upstream && node --import tsx ../conformance/extract/generate.ts ../conformance/fixtures $(UPSTREAM_COMMIT)

fixtures-check: ensure-upstream-tsx
	@fixture_tmp=$$(mktemp -d); \
		trap 'rm -rf "$$fixture_tmp"' EXIT; \
		cd .upstream && node --import tsx ../conformance/extract/generate.ts "$$fixture_tmp" $(UPSTREAM_COMMIT); \
		cd ..; \
		diff -ru conformance/fixtures "$$fixture_tmp"

sync:
	@echo "make sync is implemented by WP-610" >&2
	@exit 1
