# Orb on Cloudflare Durable Objects and self-hosted Celld (platforms/worker/README.md).
# Included from the Makefile, or run standalone: make -f platforms/worker/worker.mk <target>.
#
#   worker-build         assemble .tools/worker: dist/worker.mjs + dist/orb.wasm + wrangler
#   worker-dev           serve it locally in workerd (wrangler dev); ORB_TOKEN etc. from .tools/worker/.dev.vars
#   worker-deploy        deploy it to the authenticated Cloudflare account (wrangler deploy)
#   worker-celld-dev     serve it locally under Celld (celld dev)
#   worker-test          Go suites for the Worker host, natively and in js/wasm with a fake DO storage
#   worker-e2e-workerd   end-to-end in workerd, including a dev-server restart
#   worker-e2e-celld     end-to-end under Celld, including a node restart
#   worker-e2e-bridge-workerd|-celld  Bridge pairing both ways with a throwaway native `orb` (isolated under .tools)
#   worker-e2e-bridge-deployed        the same against ORB_WORKER_URL, redeploying WORKER_E2E_DIR to restart it
#   worker-e2e-deployed  one phase (WORKER_E2E_PHASE=write|verify) against a deployed Worker (ORB_WORKER_URL, ORB_TOKEN)

WORKER_DIR ?= $(CURDIR)/.tools/worker
WORKER_E2E_DIR ?= $(CURDIR)/.tools/worker-e2e
ifneq ($(origin GO_ENV),undefined)
WORKER_GO_ENV ?= $(GO_ENV)
else
WORKER_GO_ENV ?= GOCACHE=$(CURDIR)/.tools/cache/go-build GOMODCACHE=$(CURDIR)/.tools/cache/go-mod
endif
CELLD ?= $(CURDIR)/.tools/bin/celld
WORKER_WASM_EXEC = $$($(WORKER_GO_ENV) go env GOROOT)/lib/wasm

WORKER_BRIDGE_DIR ?= $(CURDIR)/.tools/worker-bridge-e2e
WORKER_BRIDGE_ARGS = --orb $(WORKER_BRIDGE_DIR)/orb --laptop $(WORKER_BRIDGE_DIR)/laptop

.PHONY: worker-bridge-orb worker-e2e-bridge-workerd worker-e2e-bridge-celld worker-e2e-bridge-deployed
.PHONY: worker-build worker-dev worker-deploy worker-celld-dev worker-test worker-e2e-workerd worker-e2e-celld worker-e2e-deployed worker-e2e-build

# $(1): output directory; $(2): extra scripts prepended after wasm_exec.js.
define worker_assemble
	mkdir -p $(1)/dist
	$(WORKER_GO_ENV) CGO_ENABLED=0 GOOS=js GOARCH=wasm go build -trimpath -ldflags=-s -o $(1)/dist/orb.wasm ./cmd/orb-worker
	cat $(WORKER_WASM_EXEC)/wasm_exec.js $(2) platforms/worker/deploy/worker.mjs > $(1)/dist/worker.mjs
	cp platforms/worker/deploy/wrangler.jsonc platforms/worker/deploy/package.json $(1)/
	cd $(1) && npm install --no-audit --no-fund --loglevel=error
endef

worker-build:
	$(call worker_assemble,$(WORKER_DIR),)

# cf dev/cf deploy (cf 0.4) delegate to Wrangler only through the experimental
# cloudflare.config.ts, which Celld cannot read; Wrangler itself shares
# wrangler.jsonc with Celld.
worker-dev: worker-build
	cd $(WORKER_DIR) && npx wrangler dev

worker-deploy: worker-build
	cd $(WORKER_DIR) && npx wrangler deploy

$(CELLD):
	curl -fsSL https://celld.dev/install.sh | CELLD_INSTALL_ROOT=$(CURDIR)/.tools sh

worker-celld-dev: worker-build $(CELLD)
	$(CELLD) dev $(WORKER_DIR) --no-watch

worker-test:
	$(WORKER_GO_ENV) go test -count=1 ./platforms/worker/... ./cmd/orb-worker/...
	$(WORKER_GO_ENV) GOOS=js GOARCH=wasm CGO_ENABLED=0 go test -count=1 -exec="$(WORKER_WASM_EXEC)/go_js_wasm_exec" ./platforms/worker/...

worker-e2e-workerd: worker-build
	node platforms/worker/e2e/e2e.mjs --runtime workerd --dir $(WORKER_DIR)

worker-e2e-celld: worker-build $(CELLD)
	node platforms/worker/e2e/e2e.mjs --runtime celld --dir $(WORKER_DIR) --celld $(CELLD)

# The deployed check runs the scripted model inside the Worker (fake-model.js
# answers https://fake-model.invalid), so it needs no provider key.
worker-e2e-build:
	$(call worker_assemble,$(WORKER_E2E_DIR),platforms/worker/e2e/fake-model.js)

WORKER_E2E_PHASE ?= write
worker-e2e-deployed:
	node platforms/worker/e2e/e2e.mjs --runtime remote --url "$(ORB_WORKER_URL)" --phase $(WORKER_E2E_PHASE)

# The laptop side of the Bridge checks: this tree's `orb`, run with HOME,
# ORB_STATE_HOME, ORB_BRIDGE_HOME and PI_CODING_AGENT_DIR under $(WORKER_BRIDGE_DIR).
worker-bridge-orb:
	mkdir -p $(WORKER_BRIDGE_DIR)
	$(WORKER_GO_ENV) CGO_ENABLED=0 go build -o $(WORKER_BRIDGE_DIR)/orb ./cmd/orb

worker-e2e-bridge-workerd: worker-build worker-bridge-orb
	node platforms/worker/e2e/bridge.mjs --runtime workerd --dir $(WORKER_DIR) $(WORKER_BRIDGE_ARGS)

worker-e2e-bridge-celld: worker-build worker-bridge-orb $(CELLD)
	node platforms/worker/e2e/bridge.mjs --runtime celld --dir $(WORKER_DIR) --celld $(CELLD) $(WORKER_BRIDGE_ARGS)

# Needs the deployed worker-e2e-build bundle in WORKER_E2E_DIR (its .orb-token
# and Wrangler login); the check redeploys it once to restart the object.
worker-e2e-bridge-deployed: worker-bridge-orb
	node platforms/worker/e2e/bridge.mjs --runtime remote --url "$(ORB_WORKER_URL)" --token-file $(WORKER_E2E_DIR)/.orb-token --redeploy $(WORKER_E2E_DIR) $(WORKER_BRIDGE_ARGS)
