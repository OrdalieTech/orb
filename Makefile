UPSTREAM_REPO := $(shell sed -n 's/.*"repo": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
UPSTREAM_COMMIT := $(shell sed -n 's/.*"commit": "\([^"]*\)".*/\1/p' UPSTREAM.lock)
GOLANGCI_LINT_VERSION ?= v2.7.2
GOLANGCI_LINT := $(CURDIR)/.tools/bin/golangci-lint
GO_ENV := GOCACHE=$(CURDIR)/.tools/cache/go-build GOMODCACHE=$(CURDIR)/.tools/cache/go-mod
LINT_ENV := $(GO_ENV) GOLANGCI_LINT_CACHE=$(CURDIR)/.tools/cache/golangci-lint

.PHONY: build test lint upstream sync

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
	@git -C .upstream fetch origin $(UPSTREAM_COMMIT)
	@git -C .upstream checkout --detach $(UPSTREAM_COMMIT)
	@test "$$(git -C .upstream rev-parse HEAD)" = "$(UPSTREAM_COMMIT)"

sync:
	@echo "make sync is implemented by WP-610" >&2
	@exit 1
