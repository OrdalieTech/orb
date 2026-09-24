#!/usr/bin/env bash
# P2 tier-1 gate (DECISIONS.md P2, P10): every target builds and vets, and the
# portable suites execute on both Wasm runtimes. Invoked by `make portability`.
set -euo pipefail

module=github.com/OrdalieTech/orb
native_targets="linux/amd64 linux/arm64 linux/386 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 android/arm64"
# Packages that exist only on hosts with processes, terminals or native SQLite.
# Anything that links one of them is excluded from the Wasm targets with it.
wasm_native_only="cmd/orb platforms/native/sqlite agent/modes platforms/native/bridge platforms/native/tailcat"
# Suites executed under Node (js/wasm) and wazero (wasip1/wasm).
wasm_suites="./ai/... ./engine/... ./agent/rpc/... ./bridge/... ./agent/bridge/... ./platforms/websocket/... ./platforms/memory/... ./platforms/browser/... ./platforms/worker/... ./internal/jsonschema/... ./internal/jsonwire/... ./internal/partialjson/... ./internal/truncate/..."
wasm_suite_skip="$module/ai/models/cmd/genmodels"
# Compressed ceilings: the engine-only browser runtime, and a full AgentSession,
# which must fit Cloudflare's 10 MB compressed Worker limit.
browser_gzip_budget=8000000
session_gzip_budget=10000000
# Cloudflare rejects Worker uploads above 10 MiB compressed.
worker_gzip_budget=10485760

vet_packages() {
	go list "$@" ./... | grep -v /internal/chromalexers
}

for target in $native_targets; do
	echo "portability: $target"
	GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go build ./...
	GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go vet $(GOOS=${target%/*} GOARCH=${target#*/} vet_packages)
done

# The iOS linker requires cgo, so the app shell links the core as a library;
# type-checking the module is the Orb-side contract. runtime/cgo needs the iOS
# SDK, which only macOS hosts have, so CI checks it in the macOS job.
if [ "$(go env GOHOSTOS)" = darwin ]; then
	echo "portability: ios/arm64 (type-check)"
	GOOS=ios GOARCH=arm64 CGO_ENABLED=1 go vet $(GOOS=ios GOARCH=arm64 CGO_ENABLED=1 vet_packages)
else
	echo "portability: ios/arm64 skipped: its cgo runtime needs the macOS toolchain"
fi

for os in js wasip1; do
	echo "portability: $os/wasm"
	packages=$(GOOS=$os GOARCH=wasm CGO_ENABLED=0 go list -e -f '{{.ImportPath}} {{join .Deps " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./... |
		awk -v module="$module/" -v skip="$wasm_native_only" '
			BEGIN { n = split(skip, s, " ") }
			{
				for (i = 1; i <= NF; i++) for (j = 1; j <= n; j++) {
					p = module s[j]
					if ($i == p) next
				}
				print $1
			}' | grep -v /internal/chromalexers)
	GOOS=$os GOARCH=wasm CGO_ENABLED=0 go vet $packages
done

echo "portability: js/wasm browser bundle"
bundle=$(mktemp -d)
trap 'rm -rf "$bundle"' EXIT
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags=-s -o "$bundle/orb.wasm" ./cmd/orb-wasm
size=$(gzip -9 -c "$bundle/orb.wasm" | wc -c | tr -d ' ')
echo "portability: browser bundle ${size} bytes gzip (budget ${browser_gzip_budget})"
if [ "$size" -gt "$browser_gzip_budget" ]; then
	echo "portability: browser bundle exceeds its budget" >&2
	exit 1
fi
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags=-s -o "$bundle/session.wasm" ./conformance/scenario/testdata/session
size=$(gzip -9 -c "$bundle/session.wasm" | wc -c | tr -d ' ')
echo "portability: full AgentSession ${size} bytes gzip (budget ${session_gzip_budget})"
if [ "$size" -gt "$session_gzip_budget" ]; then
	echo "portability: full AgentSession exceeds its budget" >&2
	exit 1
fi
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags=-s -o "$bundle/worker.wasm" ./cmd/orb-worker
size=$(gzip -9 -c "$bundle/worker.wasm" | wc -c | tr -d ' ')
echo "portability: Worker bundle ${size} bytes gzip (budget ${worker_gzip_budget})"
if [ "$size" -gt "$worker_gzip_budget" ]; then
	echo "portability: Worker bundle exceeds Cloudflare's upload limit" >&2
	exit 1
fi

wasm_exec=$(go env GOROOT)/lib/wasm
suites=$(go list $wasm_suites | grep -v -x -F "$wasm_suite_skip")
echo "portability: js/wasm suites (Node)"
GOOS=js GOARCH=wasm CGO_ENABLED=0 go test -count=1 -exec="$wasm_exec/go_js_wasm_exec" $suites
echo "portability: wasip1/wasm suites (wazero)"
GOWASIRUNTIME=wazero GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go test -count=1 -exec="$wasm_exec/go_wasip1_wasm_exec" $suites
