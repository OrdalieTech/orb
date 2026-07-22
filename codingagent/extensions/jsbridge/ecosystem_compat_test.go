package jsbridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

func TestEcosystemIntlSegmenterUsesUTF16GraphemeOffsets(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const segments = new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment("A👩‍💻e\u0301");
  const values = [...segments].map((entry) => entry.segment + "@" + entry.index);
  const inside = segments.containing(3);
  const outside = segments.containing(8);
  pi.registerCommand("result", {
    description: values.join("|") + ";" + inside.segment + "@" + inside.index + ";" + String(outside),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "A@0|👩‍💻@1|é@6;👩‍💻@1;undefined")
}

func TestEcosystemCreateRequireResolveUsesCallingModule(t *testing.T) {
	cwd := t.TempDir()
	packageJSON := filepath.Join(cwd, "node_modules", "@fixture", "tool", "package.json")
	marker := filepath.Join(cwd, "marker.js")
	mustWrite(t, packageJSON, `{"name":"@fixture/tool","version":"1.0.0"}`)
	mustWrite(t, marker, `module.exports = true;`)
	canonicalCWD := canonicalTestPath(t, cwd)
	result := loadAndRunExtension(t, cwd, `
import { createRequire } from "node:module";
export default function(pi) {
  const localRequire = createRequire(import.meta.url);
  const packageJSON = localRequire.resolve("@fixture/tool/package.json");
  const marker = localRequire.resolve("./marker");
  const tty = localRequire.resolve("tty");
  const zlib = localRequire.resolve("node:zlib");
  let missing = false;
  try { localRequire.resolve("@fixture/missing"); } catch { missing = true; }
  pi.registerCommand("result", {
    description: packageJSON + "|" + marker + "|" + tty + "|" + zlib + "|" + missing,
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", filepath.Join(canonicalCWD, "node_modules", "@fixture", "tool", "package.json")+"|"+filepath.Join(canonicalCWD, "marker.js")+"|tty|node:zlib|true")
}

func TestEcosystemPackageDirAndUtilDeprecate(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := loadAndRunExtension(t, t.TempDir(), `
import { getPackageDir } from "@earendil-works/pi-coding-agent";
import { deprecate } from "node:util";
export default function(pi) {
  let calls = 0;
  const wrapped = deprecate(function(value) {
    calls++;
    return this.base + value;
  }, "legacy helper", "DEP_FIXTURE");
  const first = wrapped.call({ base: 3 }, 4);
  const second = wrapped.call({ base: 5 }, 6);
  pi.registerCommand("result", {
    description: [getPackageDir(), typeof wrapped, first, second, calls].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", filepath.Dir(executable)+"|function|7|11|2")
}

func TestEcosystemProcessExitAndPerformanceHooks(t *testing.T) {
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "exit.txt")
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
import { writeFileSync } from "node:fs";
import { performance } from "node:perf_hooks";
export default function(pi) {
  const first = performance.now();
  const second = globalThis.performance.now();
  process.once("exit", (code) => writeFileSync(`+strconv.Quote(marker)+`, String(code)));
  pi.registerCommand("result", {
    description: [typeof process.on, typeof process.once, performance === globalThis.performance, first >= 0, second >= first].join("|"),
    handler: async () => {},
  });
}
`)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		loader.Close()
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	command := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{}).Command("result")
	if command == nil || command.Description != "function|function|true|true|true" {
		loader.Close()
		t.Fatalf("command = %#v", command)
	}
	loader.Close()
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "0" {
		t.Fatalf("process exit code = %q", contents)
	}
}

func TestEcosystemNetAndSDKTypeGuards(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import net, { isIP, isIPv4, isIPv6 } from "node:net";
import { isContextOverflow } from "@earendil-works/pi-ai";
import { isToolCallEventType } from "@earendil-works/pi-coding-agent";
export default function(pi) {
  const overflow = isContextOverflow({
    role: "assistant",
    content: [],
    api: "openai-completions",
    provider: "openai",
    model: "fixture",
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
    stopReason: "error",
    errorMessage: "Range of input length should be [1, 999999]",
    timestamp: 0,
  }, 200000);
  pi.registerCommand("result", {
    description: [
      net === net,
      isIP("127.0.0.1"),
      isIP("::1"),
      isIP("127.00.0.1"),
      isIPv4("127.0.0.1"),
      isIPv6("::ffff:127.0.0.1"),
      overflow,
      isToolCallEventType("bash", { toolName: "bash" }),
      isToolCallEventType("read", { toolName: "bash" }),
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|4|6|0|true|true|true|true|false")
}

func TestEcosystemAsyncLocalStoragePropagatesAcrossPromises(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { AsyncLocalStorage } from "node:async_hooks";
export default async function(pi) {
  const scope = new AsyncLocalStorage({ defaultValue: "none", name: "fixture" });
  const concurrent = await Promise.all([
    scope.run("first", async () => { await Promise.resolve(); return scope.getStore(); }),
    scope.run("second", async () => { await Promise.resolve(); return scope.getStore(); })
  ]);
  const entered = scope.run("outer", () => {
    scope.enterWith("inner");
    return scope.getStore();
  });
  const exited = scope.run("outer", () => scope.exit(() => scope.getStore()));
  scope.disable();
  pi.registerCommand("result", {
    description: [scope.name, concurrent.join(","), entered, exited, scope.getStore()].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "fixture|first,second|inner|none|none")
}

func TestEcosystemDiagnosticsChannelSingletonAndSubscriptions(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { Channel, channel, hasSubscribers, subscribe, tracingChannel, unsubscribe } from "node:diagnostics_channel";
export default function(pi) {
  const first = channel("fixture:messages");
  const second = channel("fixture:messages");
  const events = [];
  function listener(message, name) { events.push(message.value + "@" + name); }
  const missing = hasSubscribers("fixture:missing");
  const subscribeResult = subscribe("fixture:messages", listener);
  first.subscribe(listener);
  first.publish({ value: "one" });
  const firstRemoval = unsubscribe("fixture:messages", listener);
  second.publish({ value: "two" });
  const secondRemoval = second.unsubscribe(listener);
  const thirdRemoval = second.unsubscribe(listener);
  const direct = new Channel("fixture:direct");
  const names = ["start", "end", "asyncStart", "asyncEnd", "error"];
  const customChannels = Object.fromEntries(names.map(name => [name, channel("fixture:custom:" + name)]));
  const customTrace = tracingChannel(customChannels);
  pi.registerCommand("result", {
    description: [
      first === second,
      first instanceof Channel,
      missing,
      subscribeResult === undefined,
      events.join(","),
      firstRemoval,
      secondRemoval,
      thirdRemoval,
      hasSubscribers("fixture:messages"),
      channel("fixture:direct") === direct,
      names.every(name => customTrace[name] === customChannels[name])
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|true|false|true|one@fixture:messages,one@fixture:messages,two@fixture:messages|true|true|false|false|true|true")
}

func TestEcosystemDiagnosticsChannelRunsBoundStores(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { AsyncLocalStorage } from "node:async_hooks";
import { channel } from "node:diagnostics_channel";
export default function(pi) {
  const outer = new AsyncLocalStorage({ defaultValue: "none" });
  const inner = new AsyncLocalStorage({ defaultValue: "none" });
  const messages = channel("fixture:stores");
  const seen = [];
  function listener(message) {
    seen.push("publish:" + outer.getStore() + ":" + inner.getStore().value + ":" + message.value);
  }
  messages.subscribe(listener);
  messages.bindStore(outer, message => "outer-" + message.value);
  messages.bindStore(inner);
  const activeWithStores = messages.hasSubscribers;
  const returned = messages.runStores({ value: 5 }, function(add) {
    seen.push("run:" + outer.getStore() + ":" + inner.getStore().value);
    return this.base + add;
  }, { base: 7 }, 3);
  const firstUnbind = messages.unbindStore(inner);
  const secondUnbind = messages.unbindStore(inner);
  messages.unsubscribe(listener);
  messages.unbindStore(outer);
  pi.registerCommand("result", {
    description: [
      activeWithStores,
      returned,
      seen.join(","),
      outer.getStore(),
      inner.getStore(),
      firstUnbind,
      secondUnbind,
      messages.hasSubscribers
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|10|publish:outer-5:5:5,run:outer-5:5|none|none|true|false|false")
}

func TestEcosystemDiagnosticsTracingLifecycle(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { tracingChannel } from "node:diagnostics_channel";
export default async function(pi) {
  const tracing = tracingChannel("fixture:lifecycle");
  const events = [];
  const handlers = Object.fromEntries(
    ["start", "end", "asyncStart", "asyncEnd", "error"].map(name => [name, context => {
      events.push(name + ":" + context.id + ":" + ("result" in context ? context.result : "") + ":" + (context.error?.message ?? ""));
    }])
  );
  const before = tracing.hasSubscribers;
  const subscribeResult = tracing.subscribe(handlers);
  const after = tracing.hasSubscribers;

  const syncContext = { id: "sync" };
  const syncResult = tracing.traceSync(function(value) {
    events.push("fn:sync");
    return this.base + value;
  }, syncContext, { base: 2 }, 3);

  try {
    tracing.traceSync(() => {
      events.push("fn:throw");
      throw new Error("boom");
    }, { id: "throw" });
  } catch {}

  const promiseResult = await tracing.tracePromise(() => Promise.resolve(9), { id: "promise" });
  try {
    await tracing.tracePromise(() => Promise.reject(new Error("nope")), { id: "reject" });
  } catch {}

  const callbackContext = { id: "callback" };
  const callbackResult = tracing.traceCallback(function(value, callback) {
    events.push("fn:callback");
    callback.call({ marker: "callbackThis" }, null, value * 2);
    return "scheduled";
  }, 1, callbackContext, null, 4, function(error, value) {
    events.push("callback:" + this.marker + ":" + value);
  });

  const firstUnsubscribe = tracing.unsubscribe(handlers);
  const secondUnsubscribe = tracing.unsubscribe(handlers);
  const passthrough = tracing.tracePromise(() => 13, { id: "inactive" });
  pi.registerCommand("result", {
    description: [
      before,
      subscribeResult === undefined,
      after,
      syncResult,
      promiseResult,
      callbackResult,
      firstUnsubscribe,
      secondUnsubscribe,
      tracing.hasSubscribers,
      typeof passthrough + ":" + passthrough,
      events.join("|")
    ].join(";"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "false;true;true;5;9;scheduled;true;false;false;number:13;"+
		"start:sync::|fn:sync|end:sync:5:|"+
		"start:throw::|fn:throw|error:throw::boom|end:throw::boom|"+
		"start:promise::|end:promise::|asyncStart:promise:9:|asyncEnd:promise:9:|"+
		"start:reject::|end:reject::|error:reject::nope|asyncStart:reject::nope|asyncEnd:reject::nope|"+
		"start:callback::|fn:callback|asyncStart:callback:8:|callback:callbackThis:8|asyncEnd:callback:8:|end:callback:8:")
}

func TestEcosystemTTYReportsRealProcessStreams(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import tty from "node:tty";
export default function(pi) {
  pi.registerCommand("result", {
    description: [
      typeof tty.isatty,
      tty.isatty(process.stdout.fd) === process.stdout.isTTY,
      tty.isatty(process.stderr.fd) === process.stderr.isTTY,
      tty.isatty(-1)
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "function|true|true|false")
}

func TestEcosystemZlibSyncRoundTripAndErrors(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { gzipSync, gunzipSync } from "node:zlib";
export default function(pi) {
  const input = Buffer.from("braintrust sync payload");
  const compressed = gzipSync(input, { level: 9 });
  const decompressed = gunzipSync(compressed);
  let dataError;
  try { gunzipSync(Buffer.from("not gzip")); } catch (error) {
    dataError = [error instanceof Error, error.code, error.errno, error.message].join(":");
  }
  let levelError;
  try { gzipSync(input, { level: 99 }); } catch (error) {
    levelError = [error instanceof RangeError, error.code].join(":");
  }
  pi.registerCommand("result", {
    description: [
      Buffer.isBuffer(compressed),
      Buffer.isBuffer(decompressed),
      compressed.length > 0,
      decompressed.toString(),
      dataError,
      levelError
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|true|true|braintrust sync payload|true:Z_DATA_ERROR:-3:incorrect header check|true:ERR_OUT_OF_RANGE")
}

func TestEcosystemZlibCallbacksAreDeferredAndPromisifiable(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { gzip, gunzip } from "node:zlib";
import { promisify } from "node:util";
export default async function(pi) {
  const events = [];
  let synchronous = true;
  let returnValue;
  const compressed = await new Promise((resolve, reject) => {
    returnValue = gzip(Buffer.from("callback payload"), { level: 1 }, (error, value) => {
      events.push(synchronous ? "sync" : "deferred");
      if (error) reject(error); else resolve(value);
    });
    synchronous = false;
  });
  const decompressed = await promisify(gunzip)(compressed);
  const promisified = await promisify(gzip)("braintrust promisify");
  const promisifiedText = (await promisify(gunzip)(promisified)).toString();
  synchronous = true;
  const invalid = await new Promise(resolve => {
    gunzip(Buffer.from("bad"), (error, value) => {
      resolve([synchronous, error.code, error.errno, value === undefined].join(":"));
    });
    synchronous = false;
  });
  pi.registerCommand("result", {
    description: [
      returnValue === undefined,
      events.join(","),
      Buffer.isBuffer(compressed),
      decompressed.toString(),
      promisifiedText,
      invalid
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|deferred|true|callback payload|braintrust promisify|false:Z_DATA_ERROR:-3:true")
}

func TestEcosystemDNSPromisesLookupShapesAndFamilies(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { lookup } from "node:dns/promises";
export default async function(pi) {
  let synchronous = true;
  const deferredPromise = lookup("localhost", { family: 4 }).then(() => synchronous);
  synchronous = false;
  const deferred = await deferredPromise;
  const all = await lookup("localhost", { all: true, order: "ipv4first" });
  const familyZero = await lookup("localhost", { family: 0, order: "ipv4first" });
  const familyFour = await lookup("localhost", { family: 4 });
  const familySix = await lookup("localhost", { family: 6 });
  const orderedSix = await lookup("localhost", { all: true, verbatim: false, order: "ipv6first" });
  const literal = await lookup("127.0.0.1", 6);
  let invalid;
  try { lookup("localhost", { family: 5 }); } catch (error) {
    invalid = [error instanceof TypeError, error.code].join(":");
  }
  pi.registerCommand("result", {
    description: [
      deferred,
      Array.isArray(all),
      all.length >= 2,
      all[0].family,
      all.some(value => value.address === "127.0.0.1" && value.family === 4),
      all.some(value => value.address === "::1" && value.family === 6),
      Array.isArray(familyZero),
      familyZero.family,
      familyFour.address + ":" + familyFour.family,
      familySix.address + ":" + familySix.family,
      orderedSix[0].family,
      literal.address + ":" + literal.family,
      invalid
    ].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "false|true|true|4|true|true|false|4|127.0.0.1:4|::1:6|6|127.0.0.1:4|true:ERR_INVALID_ARG_VALUE")
}
