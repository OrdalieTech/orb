package jsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// --- fs module tests ---

func TestFSReadFileSync(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "test.txt"), "hello world")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  const data = fs.readFileSync("`+filepath.Join(cwd, "test.txt")+`", "utf-8");
  pi.registerCommand("result", {description: data, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "hello world")
}

func TestFSExistsSync(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "exists.txt"), "yes")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  const exists = fs.existsSync("`+filepath.Join(cwd, "exists.txt")+`");
  const missing = fs.existsSync("`+filepath.Join(cwd, "nope.txt")+`");
  pi.registerCommand("result", {description: String(exists) + ":" + String(missing), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:false")
}

func TestFSWriteAndReadSync(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "output.txt")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  fs.writeFileSync("`+target+`", "written");
  const data = fs.readFileSync("`+target+`", "utf-8");
  pi.registerCommand("result", {description: data, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "written")
}

func TestFSReaddirSyncWithFileTypes(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "testdir", "file.txt"), "f")
	if err := os.Mkdir(filepath.Join(cwd, "testdir", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cwd, "testdir")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  const entries = fs.readdirSync("`+dir+`", { withFileTypes: true });
  const desc = entries.map(e => e.name + ":" + e.isDirectory() + ":" + e.isFile()).sort().join(",");
  pi.registerCommand("result", {description: desc, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "file.txt:false:true,subdir:true:false")
}

// --- fs.watch tests ---

func TestFSWatchDetectsChange(t *testing.T) {
	cwd := t.TempDir()
	watchedFile := filepath.Join(cwd, "watched.txt")
	mustWrite(t, watchedFile, "initial")

	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default async function(pi) {
  const file = "`+watchedFile+`";
  const detected = await new Promise(resolve => {
    const watcher = fs.watch(file, (eventType, filename) => {
      watcher.close();
      resolve(eventType + ":" + filename);
    });
    // Modify after baseline stat is taken
    fs.writeFileSync(file, "changed");
  });
  pi.registerCommand("result", {description: detected, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "change:watched.txt")
}

func TestFSWatchNoCallbackAfterClose(t *testing.T) {
	cwd := t.TempDir()
	watchedFile := filepath.Join(cwd, "watched.txt")
	mustWrite(t, watchedFile, "initial")

	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default async function(pi) {
  const file = "`+watchedFile+`";
  let callbackCount = 0;
  const watcher = fs.watch(file, () => { callbackCount++; });
  watcher.close();
  // Modify file after close — should not trigger callback
  fs.writeFileSync(file, "changed");
  await new Promise(resolve => setTimeout(resolve, 250));
  pi.registerCommand("result", {description: String(callbackCount), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "0")
}

// --- path module tests ---

func TestPathJoin(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as path from "node:path";
export default function(pi) {
  const joined = path.join("a", "b", "c.txt");
  pi.registerCommand("result", {description: joined, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", filepath.Join("a", "b", "c.txt"))
}

func TestPathDirnameBasenameExtname(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as path from "node:path";
export default function(pi) {
  const d = path.dirname("/foo/bar/baz.txt");
  const b = path.basename("/foo/bar/baz.txt");
  const e = path.extname("baz.txt");
  pi.registerCommand("result", {description: d + "|" + b + "|" + e, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "/foo/bar|baz.txt|.txt")
}

func TestPathSepAndDelimiter(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as path from "node:path";
export default function(pi) {
  pi.registerCommand("result", {description: path.sep + path.delimiter, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "/:")
}

func TestPathResolveUsesExtensionCwd(t *testing.T) {
	extensionCwd := t.TempDir()
	result := loadAndRunExtension(t, extensionCwd, `
import * as path from "node:path";
export default function(pi) {
  const resolved = path.resolve("relative.txt");
  pi.registerCommand("result", {description: resolved, handler: async () => {}});
}
`)
	expected := filepath.Join(extensionCwd, "relative.txt")
	command := extensions.NewRunner(result.Registry, extensions.RunnerOptions{}).Command("result")
	if command == nil {
		t.Fatal("command not registered")
	}
	if command.Description != expected {
		t.Fatalf("path.resolve = %q, want %q (extension cwd = %q)", command.Description, expected, extensionCwd)
	}
}

// --- os module tests ---

func TestOSHomedirAndTmpdir(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as os from "node:os";
export default function(pi) {
  const h = os.homedir();
  const t = os.tmpdir();
  pi.registerCommand("result", {description: (h.length > 0) + ":" + (t.length > 0), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:true")
}

// --- process tests ---

func TestProcessCwdAndPlatform(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
export default function(pi) {
  const p = process.platform;
  const c = process.cwd();
  pi.registerCommand("result", {description: (p.length > 0) + ":" + c, handler: async () => {}});
}
`)
	command := extensions.NewRunner(result.Registry, extensions.RunnerOptions{}).Command("result")
	if command == nil {
		t.Fatal("command not registered")
	}
	if !strings.Contains(command.Description, "true:") || !strings.Contains(command.Description, cwd) {
		t.Fatalf("process description = %q", command.Description)
	}
}

func TestProcessEnv(t *testing.T) {
	t.Setenv("PI_TEST_SHIM_VAR", "shim_value")
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  pi.registerCommand("result", {description: process.env.PI_TEST_SHIM_VAR || "missing", handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "shim_value")
}

func TestBareProcessModuleInBundledDependency(t *testing.T) {
	t.Setenv("PI_TEST_SHIM_VAR", "shim_value")
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "process-consumer.cjs"), `
const imported = require("process");
exports.describe = () => [
  imported === globalThis.process,
  imported.env.PI_TEST_SHIM_VAR,
  imported.cwd(),
].join(":");
`)
	result := loadAndRunExtension(t, cwd, `
import nodeProcess from "node:process";
import { describe } from "./process-consumer.cjs";
export default function(pi) {
  const description = [nodeProcess === globalThis.process, describe()].join(":");
  pi.registerCommand("result", {description, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:true:shim_value:"+cwd)
}

func TestGlobalAliasesGlobalThis(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  global.TESTING_WINDOWS = true;
  pi.registerCommand("result", {
    description: String(global === globalThis) + ":" + String(globalThis.TESTING_WINDOWS),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true:true")
}

func TestOSTotalmemReturnsPhysicalBytes(t *testing.T) {
	memory := nodeTotalMemory()
	if memory < 8*1024*1024 {
		t.Fatalf("nodeTotalMemory() = %d, want at least 8 MiB", memory)
	}
	result := loadAndRunExtension(t, t.TempDir(), `
import os from "node:os";
export default function(pi) {
  const memory = os.totalmem();
  const limit = Math.max(8 * 1024 * 1024, Math.min(Number.MAX_SAFE_INTEGER, Math.floor(memory)));
  pi.registerCommand("result", {
    description: [typeof os.totalmem, typeof memory, Number.isSafeInteger(memory), limit >= 8 * 1024 * 1024].join(":"),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "function:number:true:true")
}

// --- url module tests ---

func TestURLFileURLToPath(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { fileURLToPath } from "node:url";
export default function(pi) {
  const p = fileURLToPath("file:///tmp/test.txt");
  pi.registerCommand("result", {description: p, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "/tmp/test.txt")
}

func TestURLConstructorsResolveRelativeFileURL(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { URL as NodeURL } from "node:url";
export default function(pi) {
  const base = "file:///packages/node_modules/pi-readseek/dist/index.js";
  const globalURL = new URL("../prompts/search.md", base);
  const nodeURL = new NodeURL("../prompts/search.md", base);
  pi.registerCommand("result", {
    description: [
      NodeURL === URL,
      globalURL.href,
      globalURL.pathname,
      nodeURL.href,
      nodeURL.pathname
    ].join("|"),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|file:///packages/node_modules/pi-readseek/prompts/search.md|/packages/node_modules/pi-readseek/prompts/search.md|file:///packages/node_modules/pi-readseek/prompts/search.md|/packages/node_modules/pi-readseek/prompts/search.md")
}

func TestFSReadFileSyncAcceptsFileURL(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "packages", "node_modules", "pi-readseek", "prompts", "search.md")
	mustWrite(t, target, "search prompt")
	base := "file://" + filepath.ToSlash(filepath.Join(cwd, "packages", "node_modules", "pi-readseek", "dist", "index.js"))
	result := loadAndRunExtension(t, cwd, `
import { readFileSync } from "node:fs";
export default function(pi) {
  const promptURL = new URL("../prompts/search.md", "`+base+`");
  const fromObject = readFileSync(promptURL, "utf8");
  const fromString = readFileSync(promptURL.href, "utf8");
  pi.registerCommand("result", {
    description: fromObject + "|" + fromString,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "search prompt|search prompt")
}

// --- util module tests ---

func TestUtilPromisify(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { promisify } from "node:util";
export default async function(pi) {
  function nodeStyleFn(arg, callback) { callback(null, "result:" + arg); }
  const asyncFn = promisify(nodeStyleFn);
  const result = await asyncFn("test");
  pi.registerCommand("result", {description: result, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "result:test")
}

// --- child_process tests ---

func TestChildProcessExecSync(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { execSync } from "node:child_process";
export default function(pi) {
  const output = execSync("echo hello").toString().trim();
  pi.registerCommand("result", {description: output, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "hello")
}

// --- Buffer tests ---

func TestBufferByteLength(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const len = Buffer.byteLength("hello");
  pi.registerCommand("result", {description: String(len), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "5")
}

func TestBareBufferModuleInBundledDependency(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "buffer-consumer.cjs"), `
const imported = require("buffer");
exports.describe = () => [
  imported.Buffer === globalThis.Buffer,
  imported.Buffer.from("hello").toString(),
].join(":");
`)
	result := loadAndRunExtension(t, cwd, `
import { Buffer as NodeBuffer } from "node:buffer";
import { describe } from "./buffer-consumer.cjs";
export default function(pi) {
  const description = [NodeBuffer === globalThis.Buffer, describe()].join(":");
  pi.registerCommand("result", {description, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:true:hello")
}

func TestFSCreateWriteStreamAppendAndEndCallback(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "transcript.jsonl")
	mustWrite(t, target, "before\n")
	result := loadAndRunExtension(t, cwd, `
import { createWriteStream, readFileSync } from "node:fs";
export default async function(pi) {
  const stream = createWriteStream("`+target+`", { flags: "a" });
  const first = stream.write("one\n");
  stream.write(Buffer.from("two\n"));
  await new Promise(resolve => stream.end(resolve));
  pi.registerCommand("result", {
    description: [first, stream.closed, stream.bytesWritten, readFileSync("`+target+`", "utf8")].join("|"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true|true|8|before\none\ntwo\n")
}

func TestCommonNodeFSAndExecFileSyncSurface(t *testing.T) {
	cwd := t.TempDir()
	sourceDir := filepath.Join(cwd, "source")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(sourceDir, "value.txt")
	mustWrite(t, sourceFile, "value")
	link := filepath.Join(cwd, "value-link")
	if err := os.Symlink(sourceFile, link); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(cwd, "copy")
	result := loadAndRunExtension(t, cwd, `
import {
  accessSync, chmodSync, constants, cpSync, existsSync, mkdtempSync,
  readFileSync, readlinkSync, realpathSync, rmSync,
} from "node:fs";
import { chmod, copyFile, lstat, readlink, realpath, rename, rm } from "node:fs/promises";
import { execFileSync } from "node:child_process";
export default async function(pi) {
  accessSync("/bin/sh", constants.X_OK);
  const output = execFileSync("/bin/sh", ["-c", "printf node-surface"], {
    cwd: "`+cwd+`", encoding: "utf8", timeout: 1000,
  });
  cpSync("`+sourceDir+`", "`+copyDir+`", { recursive: true });
  const copied = "`+filepath.Join(copyDir, "value.txt")+`";
  chmodSync(copied, 0o600);
  const temp = mkdtempSync("`+filepath.Join(cwd, "matrix-")+`");
  const syncOK = realpathSync("`+link+`") === "`+sourceFile+`" &&
    readlinkSync("`+link+`") === "`+sourceFile+`" && readFileSync(copied, "utf8") === "value";
  const asyncOK = (await lstat("`+link+`")).isSymbolicLink() &&
    await realpath("`+link+`") === "`+sourceFile+`" && await readlink("`+link+`") === "`+sourceFile+`";
  const renamed = copied + ".renamed";
  await rename(copied, renamed);
  await chmod(renamed, 0o644);
  await copyFile(renamed, copied);
  rmSync("`+copyDir+`", { recursive: true, force: true });
  await rm(temp, { recursive: true, force: true });
  await rm("`+filepath.Join(cwd, "missing")+`", { force: true });
  pi.registerCommand("result", {
    description: [output, syncOK, asyncOK, existsSync("`+copyDir+`")].join(":"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "node-surface:true:true:false")
}

// --- console tests ---

func TestConsoleDoesNotPanic(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  console.log("test message");
  console.error("err message");
  console.warn("warn message");
  pi.registerCommand("result", {description: "ok", handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "ok")
}

// --- timer tests ---

func TestSetTimeoutDefers(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  let value = "before";
  setTimeout(() => { value = "after"; }, 0);
  pi.registerCommand("result", {description: value, handler: async () => {}});
}
`)
	// setTimeout must defer — value is still "before" during sync execution
	assertRegisteredDescription(t, result, "result", "before")
}

func TestSetTimeoutCallbackFires(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const value = await new Promise(resolve => {
    setTimeout(() => resolve("fired"), 10);
  });
  pi.registerCommand("result", {description: value, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "fired")
}

func TestSetTimeoutHonorsDelay(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const order = [];
  const p1 = new Promise(resolve => setTimeout(() => { order.push("slow"); resolve(); }, 100));
  const p2 = new Promise(resolve => setTimeout(() => { order.push("fast"); resolve(); }, 10));
  await Promise.all([p1, p2]);
  pi.registerCommand("result", {description: order.join(","), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "fast,slow")
}

func TestClearTimeoutPreventsDelivery(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  let fired = false;
  const id = setTimeout(() => { fired = true; }, 50);
  clearTimeout(id);
  await new Promise(resolve => setTimeout(resolve, 150));
  pi.registerCommand("result", {description: String(fired), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "false")
}

func TestSetIntervalRepeats(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  let count = 0;
  const id = setInterval(() => { count++; }, 10);
  await new Promise(resolve => setTimeout(resolve, 80));
  clearInterval(id);
  const finalCount = count;
  // Wait to verify no more ticks after clear
  await new Promise(resolve => setTimeout(resolve, 50));
  pi.registerCommand("result", {
    description: String(count > 0) + ":" + String(count === finalCount),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true:true")
}

func TestTimerCloseCleanup(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
export default function(pi) {
  setInterval(() => {}, 10);
  setTimeout(() => {}, 100000);
  pi.registerCommand("result", {description: "ok", handler: async () => {}});
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	assertRegisteredDescription(t, loaded, "result", "ok")

	done := make(chan struct{})
	go func() {
		loader.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() blocked — timer goroutine leak")
	}
}

func TestTimerRace(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const results = [];
  for (let i = 0; i < 5; i++) {
    results.push(new Promise(resolve => setTimeout(() => resolve(i), i * 5)));
  }
  const values = await Promise.all(results);
  pi.registerCommand("result", {description: values.join(","), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "0,1,2,3,4")
}

// --- fetch tests ---

func TestFetchAsyncReturnsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"key":"value"}`))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const data = await resp.json();
  pi.registerCommand("result", {description: data.key + ":" + resp.status, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "value:200")
}

func TestFetchError(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  try {
    await fetch("http://127.0.0.1:1");
    pi.registerCommand("result", {description: "should-not-reach", handler: async () => {}});
  } catch(e) {
    pi.registerCommand("result", {description: "caught", handler: async () => {}});
  }
}
`)
	assertRegisteredDescription(t, result, "result", "caught")
}

func TestFetchHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", r.Header.Get("X-Request"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`", {
    headers: {"X-Request": "hello"}
  });
  const custom = resp.headers.get("X-Custom");
  pi.registerCommand("result", {description: custom, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "hello")
}

func TestFetchBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte("echo:" + string(body)))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`", {
    method: "POST",
    body: "hello-body"
  });
  const text = await resp.text();
  pi.registerCommand("result", {description: text, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "echo:hello-body")
}

func TestFetchOrdering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("delayed"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const fetchPromise = fetch("`+srv.URL+`");
  // Timer with 0 delay fires before fetch completes
  const timerValue = await new Promise(resolve => setTimeout(() => resolve("timer-first"), 0));
  const resp = await fetchPromise;
  const body = await resp.text();
  pi.registerCommand("result", {
    description: timerValue + ":" + body,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "timer-first:delayed")
}

// --- async registered callback tests ---

func TestAsyncCommandHandlerAwaitsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"greeting":"hello-from-fetch"}`))
	}))
	defer srv.Close()

	cwd := t.TempDir()
	marker := filepath.Join(cwd, "marker.txt")
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
import * as fs from "node:fs";
export default function(pi) {
  pi.registerCommand("async-fetch", {
    description: "test",
    handler: async (args, ctx) => {
      const resp = await fetch("`+srv.URL+`");
      const data = await resp.json();
      fs.writeFileSync("`+marker+`", data.greeting);
    }
  });
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModePrint})
	command := runner.Command("async-fetch")
	if command == nil {
		t.Fatal("command not registered")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatalf("async command handler failed: %v", err)
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker file not written: %v", err)
	}
	if string(content) != "hello-from-fetch" {
		t.Fatalf("marker = %q, want %q", string(content), "hello-from-fetch")
	}
}

func TestAsyncEventHandlerAwaitsTimer(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
export default function(pi) {
  pi.on("before_agent_start", async (event, ctx) => {
    await new Promise(resolve => setTimeout(resolve, 10));
    return { systemPrompt: event.systemPrompt + " [async-timer-modified]" };
  });
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModePrint})
	result := runner.EmitBeforeAgentStart(context.Background(), "", nil, "base prompt", extensions.SystemPromptOptions{})
	if result == nil || result.SystemPrompt == nil {
		t.Fatal("expected before_agent_start to modify system prompt after async timer")
	}
	if !strings.Contains(*result.SystemPrompt, "[async-timer-modified]") {
		t.Fatalf("system prompt = %q, want it to contain [async-timer-modified]", *result.SystemPrompt)
	}
}

// --- fs/promises tests ---

func TestFSPromisesReadFile(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "async.txt"), "async content")
	result := loadAndRunExtension(t, cwd, `
import { readFile } from "node:fs/promises";
export default async function(pi) {
  const data = await readFile("`+filepath.Join(cwd, "async.txt")+`", "utf-8");
  pi.registerCommand("result", {description: data, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "async content")
}

func TestFSPromisesFileHandleAndDirents(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "knowledge-base")
	mustWrite(t, filepath.Join(dir, "input.md"), "hello")
	result := loadAndRunExtension(t, cwd, `
import { open, readdir } from "node:fs/promises";
export default async function(pi) {
  const entries = await readdir("`+dir+`", { withFileTypes: true });
  const handle = await open("`+filepath.Join(dir, "input.md")+`", "r");
  try {
    const info = await handle.stat();
    const buffer = Buffer.allocUnsafe(info.size);
    const first = await handle.read(buffer, 0, 2, null);
    const second = await handle.read(buffer, 2, buffer.length - 2, null);
    const overflow = Buffer.allocUnsafe(1);
    const eof = await handle.read(overflow, 0, 1, null);
    const text = new TextDecoder("utf-8", { fatal: true }).decode(buffer.subarray(0, first.bytesRead + second.bytesRead));
    const entry = entries[0];
    pi.registerCommand("result", {
      description: [entry.name, entry.isFile(), entry.isDirectory(), info.size, text, eof.bytesRead].join(":"),
      handler: async () => {},
    });
  } finally {
    await handle.close();
  }
}
`)
	assertRegisteredDescription(t, result, "result", "input.md:true:false:5:hello:0")
}

func TestTextDecoderFatalRejectsInvalidUTF8(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
export default function(pi) {
  let rejected = false;
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(Buffer.from([255]));
  } catch {
    rejected = true;
  }
  pi.registerCommand("result", { description: String(rejected), handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "true")
}

func TestTextDecoderPreservesSplitStreamingUTF8(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
export default function(pi) {
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const first = decoder.decode(Buffer.from([0x41, 0xe2, 0x82]), { stream: true });
  const second = decoder.decode(Buffer.from([0xac, 0x42]), { stream: true });
  const flushed = decoder.decode();
  pi.registerCommand("result", { description: first + "|" + second + "|" + flushed, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "A|€B|")
}

func TestAbortControllerPreservesReasonIdentity(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
export default function(pi) {
  class TaskTimeoutError extends Error {}
  const controller = new AbortController();
  const reason = new TaskTimeoutError("timed out");
  controller.abort(reason);
  controller.abort(new Error("replacement"));
  let thrown;
  try {
    controller.signal.throwIfAborted();
  } catch (error) {
    thrown = error;
  }
  pi.registerCommand("result", {
    description: [controller.signal.aborted, controller.signal.reason === reason, controller.signal.reason instanceof TaskTimeoutError, thrown === reason].join(":"),
    handler: async () => {},
  });
}
`)
	assertRegisteredDescription(t, result, "result", "true:true:true:true")
}

// --- bare specifier tests ---

func TestBareSpecifierWithoutNodePrefix(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "bare.txt"), "bare content")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "fs";
import * as path from "path";
export default function(pi) {
  const data = fs.readFileSync("`+filepath.Join(cwd, "bare.txt")+`", "utf-8");
  const sep = path.sep;
  pi.registerCommand("result", {description: data + sep, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "bare content/")
}

// --- Acceptance tests: upstream examples ---

func TestAcceptanceFileTriggerLoads(t *testing.T) {
	loadUpstreamExample(t, "file-trigger.ts")
}

func TestAcceptanceProtectedPathsLoads(t *testing.T) {
	loadUpstreamExample(t, "protected-paths.ts")
}

func TestAcceptanceGitCheckpointLoads(t *testing.T) {
	loadUpstreamExample(t, "git-checkpoint.ts")
}

func TestAcceptanceDirtyRepoGuardLoads(t *testing.T) {
	loadUpstreamExample(t, "dirty-repo-guard.ts")
}

func TestAcceptanceClaudeRulesLoads(t *testing.T) {
	loadUpstreamExample(t, "claude-rules.ts")
}

// --- Behavioral acceptance tests ---

func TestBehaviorClaudeRulesFunctional(t *testing.T) {
	cwd := t.TempDir()
	rulesDir := filepath.Join(cwd, ".claude", "rules")
	mustWrite(t, filepath.Join(rulesDir, "testing.md"), "# Testing rules")
	mustWrite(t, filepath.Join(rulesDir, "sub", "nested.md"), "# Nested rules")

	source := fixtureSource(t, "claude-rules.ts")
	entry := filepath.Join(cwd, ".pi", "extensions", "claude-rules.ts")
	mustWrite(t, entry, source)

	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ProjectTrusted: true,
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModePrint})

	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})

	before := runner.EmitBeforeAgentStart(context.Background(), "", nil, "base prompt", extensions.SystemPromptOptions{})
	if before == nil || before.SystemPrompt == nil {
		t.Fatal("claude-rules did not modify system prompt")
	}
	if !strings.Contains(*before.SystemPrompt, "testing.md") {
		t.Fatalf("system prompt does not mention testing.md: %s", *before.SystemPrompt)
	}
	if !strings.Contains(*before.SystemPrompt, "sub/nested.md") {
		t.Fatalf("system prompt does not mention sub/nested.md: %s", *before.SystemPrompt)
	}
}

func TestBehaviorFileTriggerWatchesFile(t *testing.T) {
	cwd := t.TempDir()
	triggerFile := filepath.Join(cwd, "trigger.txt")
	mustWrite(t, triggerFile, "")

	// Modify file-trigger to use our temp path and work without pi.sendMessage
	source := `
import * as fs from "node:fs";
export default function(pi) {
  pi.on("session_start", async (_event, ctx) => {
    const triggerFile = "` + triggerFile + `";
    fs.watch(triggerFile, () => {
      try {
        const content = fs.readFileSync(triggerFile, "utf-8").trim();
        if (content) {
          // pi.sendMessage requires WP-520; write a marker file instead
          fs.writeFileSync(triggerFile + ".detected", content);
          fs.writeFileSync(triggerFile, "");
        }
      } catch { /* file might not exist yet */ }
    });
  });
}
`
	entry := filepath.Join(cwd, ".pi", "extensions", "file-trigger.ts")
	mustWrite(t, entry, source)

	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ProjectTrusted: true,
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModePrint})

	// Fire session_start to set up the watcher
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})

	// Write trigger content — the watcher should detect it
	mustWrite(t, triggerFile, "run the tests")

	// Wait for the poll + callback to fire
	deadline := time.Now().Add(2 * time.Second)
	markerFile := triggerFile + ".detected"
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	content, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatalf("watcher callback did not fire: %v", err)
	}
	if string(content) != "run the tests" {
		t.Fatalf("watcher detected content = %q, want %q", string(content), "run the tests")
	}
	// Verify trigger file was cleared
	cleared, _ := os.ReadFile(triggerFile)
	if strings.TrimSpace(string(cleared)) != "" {
		t.Fatalf("trigger file not cleared: %q", string(cleared))
	}
}

// --- os.platform tests ---

func TestOSPlatformIsFunction(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as os from "node:os";
export default function(pi) {
  const p = os.platform();
  pi.registerCommand("result", {description: typeof os.platform + ":" + (p.length > 0), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "function:true")
}

// --- writeFileSync/mkdtemp/stat tests ---

func TestWriteFileSyncNoParentDirCreation(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  let threw = false;
  try {
    fs.writeFileSync("`+filepath.Join(cwd, "nonexistent", "subdir", "file.txt")+`", "data");
  } catch(e) {
    threw = true;
  }
  pi.registerCommand("result", {description: String(threw), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true")
}

func TestMkdtempHonorsPrefix(t *testing.T) {
	cwd := t.TempDir()
	prefix := filepath.Join(cwd, "myprefix-")
	result := loadAndRunExtension(t, cwd, `
import { mkdtemp } from "node:fs/promises";
export default async function(pi) {
  const dir = await mkdtemp("`+prefix+`");
  const inExpectedDir = dir.startsWith("`+cwd+`");
  const hasPrefix = dir.includes("myprefix-");
  pi.registerCommand("result", {description: inExpectedDir + ":" + hasPrefix, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:true")
}

func TestStatHasMtimeMs(t *testing.T) {
	cwd := t.TempDir()
	f := filepath.Join(cwd, "stat-test.txt")
	mustWrite(t, f, "hello")
	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default function(pi) {
  const s = fs.statSync("`+f+`");
  const hasMtimeMs = typeof s.mtimeMs === "number" && s.mtimeMs > 0;
  const hasMtimeGetTime = typeof s.mtime.getTime === "function" && s.mtime.getTime() > 0;
  pi.registerCommand("result", {description: hasMtimeMs + ":" + hasMtimeGetTime, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true:true")
}

// --- path POSIX corpus test (118 entries from Node v24 path.posix) ---

func TestPathPOSIXCorpus(t *testing.T) {
	corpusJSON := pathCorpusJSON()
	var corpus []struct {
		Fn   string `json:"fn"`
		Args []any  `json:"args"`
		Want any    `json:"want"`
	}
	if err := json.Unmarshal([]byte(corpusJSON), &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	t.Logf("corpus size: %d entries", len(corpus))

	for i, tc := range corpus {
		argsJSON, _ := json.Marshal(tc.Args)
		t.Run(fmt.Sprintf("%d_%s_%s", i, tc.Fn, string(argsJSON)), func(t *testing.T) {
			switch tc.Fn {
			case "resolve":
				// resolve uses extension cwd; corpus uses absolute paths only
				args := make([]string, len(tc.Args))
				for j, a := range tc.Args {
					args[j] = fmt.Sprint(a)
				}
				got := posixResolve("/", args...)
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixResolve(%v) = %q, want %q", tc.Args, got, want)
				}
			case "basename":
				p := fmt.Sprint(tc.Args[0])
				base := posixBasename(p)
				if len(tc.Args) > 1 {
					ext := fmt.Sprint(tc.Args[1])
					if ext != "" && strings.HasSuffix(base, ext) {
						hasDir := strings.Contains(p, "/")
						if !hasDir || base != ext {
							base = base[:len(base)-len(ext)]
						}
					}
				}
				want := fmt.Sprint(tc.Want)
				if base != want {
					t.Errorf("posixBasename(%v) = %q, want %q", tc.Args, base, want)
				}
			case "dirname":
				got := posixDirname(fmt.Sprint(tc.Args[0]))
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixDirname(%q) = %q, want %q", tc.Args[0], got, want)
				}
			case "extname":
				got := posixExtname(fmt.Sprint(tc.Args[0]))
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixExtname(%q) = %q, want %q", tc.Args[0], got, want)
				}
			case "normalize":
				got := posixNormalize(fmt.Sprint(tc.Args[0]))
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixNormalize(%q) = %q, want %q", tc.Args[0], got, want)
				}
			case "join":
				args := make([]string, len(tc.Args))
				for j, a := range tc.Args {
					args[j] = fmt.Sprint(a)
				}
				got := posixJoin(args...)
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixJoin(%v) = %q, want %q", tc.Args, got, want)
				}
			case "isAbsolute":
				got := strings.HasPrefix(fmt.Sprint(tc.Args[0]), "/")
				want := tc.Want.(bool)
				if got != want {
					t.Errorf("isAbsolute(%q) = %v, want %v", tc.Args[0], got, want)
				}
			case "relative":
				from := fmt.Sprint(tc.Args[0])
				to := fmt.Sprint(tc.Args[1])
				got := posixRelative("/", from, to)
				want := fmt.Sprint(tc.Want)
				if got != want {
					t.Errorf("posixRelative(%q, %q) = %q, want %q", from, to, got, want)
				}
			case "parse":
				p := fmt.Sprint(tc.Args[0])
				wantMap := tc.Want.(map[string]any)
				gotRoot := posixRoot(p)
				gotBase := posixBasename(p)
				gotExt := posixExtname(gotBase)
				gotName := gotBase
				if gotExt != "" {
					gotName = gotBase[:len(gotBase)-len(gotExt)]
				}
				gotDir := posixParseDir(p)
				check := func(field, got, want string) {
					if got != want {
						t.Errorf("parse(%q).%s = %q, want %q", p, field, got, want)
					}
				}
				check("root", gotRoot, fmt.Sprint(wantMap["root"]))
				check("dir", gotDir, fmt.Sprint(wantMap["dir"]))
				check("base", gotBase, fmt.Sprint(wantMap["base"]))
				check("ext", gotExt, fmt.Sprint(wantMap["ext"]))
				check("name", gotName, fmt.Sprint(wantMap["name"]))
			case "format":
				// format tested via JS integration — skip in pure-Go corpus
			default:
				t.Errorf("unknown corpus function %q", tc.Fn)
			}
		})
	}
}

// --- path.format corpus test (JS integration) ---

func TestPathFormatCorpus(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as path from "node:path";
export default function(pi) {
  const cases = [
    [path.format({}), ""],
    [path.format({dir:".",base:"file.txt"}), "./file.txt"],
    [path.format({root:"/",name:"file"}), "/file"],
    [path.format({name:"file",ext:".txt"}), "file.txt"],
    [path.format({ext:".txt"}), ".txt"],
    [path.format({root:"/"}), "/"],
    [path.format({dir:"/",base:""}), "//"],
    [path.format({dir:"/a",base:"b"}), "/a/b"],
    [path.format({base:"file.txt"}), "file.txt"],
    [path.format({dir:"/home/user",base:"file.txt"}), "/home/user/file.txt"],
    [path.format({name:"f",ext:""}), "f"],
    [path.format({dir:"",base:"a"}), "a"],
    [path.format({name:"file",ext:"txt"}), "file.txt"],
  ];
  const failures = cases.filter(([got, want]) => got !== want)
    .map(([got, want]) => got + "!=" + want);
  pi.registerCommand("result", {
    description: failures.length === 0 ? "13/13" : failures.join(";"),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "13/13")
}

// --- Buffer.slice tests ---

func TestBufferSliceNegativeBounds(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const buf = Buffer.from("hello world");
  const last5 = buf.slice(-5).toString();
  const mid = buf.slice(2, -3).toString();
  const empty = buf.slice(5, 2).toString();
  pi.registerCommand("result", {description: last5 + "|" + mid + "|" + empty, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "world|llo wo|")
}

// --- child_process deferred tests ---

func TestExecCallbackDeferred(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { exec } from "node:child_process";
export default async function(pi) {
  const output = await new Promise((resolve, reject) => {
    exec("echo deferred-output", (err, stdout) => {
      if (err) reject(err);
      else resolve(stdout.trim());
    });
  });
  pi.registerCommand("result", {description: output, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "deferred-output")
}

func TestSpawnDefersExecution(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { spawn } from "node:child_process";
export default async function(pi) {
  const result = await new Promise(resolve => {
    const chunks = [];
    const cp = spawn("echo", ["spawn-test"]);
    cp.stdout.on("data", (data) => { chunks.push(data.toString()); });
    cp.on("close", (code) => {
      resolve(chunks.join("").trim() + ":" + code);
    });
  });
  pi.registerCommand("result", {description: result, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "spawn-test:0")
}

func TestSpawnChildUsesEventEmitter(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { spawn } from "node:child_process";
export default async function(pi) {
  const result = await new Promise((resolve, reject) => {
    const events = [];
    const chunks = [];
    const cp = spawn("echo", ["event-emitter"]);
    const unrefReturnsChild = cp.unref() === cp;
    let spawnCount = 0;
    let exitCode;
    cp.once("error", reject);
    cp.once("spawn", () => {
      spawnCount++;
      events.push("spawn");
    });
    cp.stdout.on("data", data => {
      chunks.push(data.toString());
      events.push("data");
    });
    cp.on("exit", code => {
      exitCode = code;
      events.push("exit");
    });
    cp.on("close", code => {
      events.push("close");
      cp.emit("spawn");
      resolve([
        unrefReturnsChild,
        spawnCount,
        events.join(","),
        chunks.join("").trim(),
        exitCode,
        code
      ].join("|"));
    });
  });
  pi.registerCommand("result", {description: result, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true|1|spawn,data,exit,close|event-emitter|0|0")
}

func TestSpawnMissingExecutableEmitsError(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { spawn } from "node:child_process";
export default async function(pi) {
  const result = await new Promise(resolve => {
    const cp = spawn("pigo-jsbridge-command-that-does-not-exist");
    let spawnCount = 0;
    cp.once("spawn", () => { spawnCount++; });
    cp.once("error", error => {
      resolve([spawnCount, error.code, cp.unref() === cp].join("|"));
    });
  });
  pi.registerCommand("result", {description: result, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "0|ENOENT|true")
}

func TestSpawnWithCwd(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { spawn } from "node:child_process";
export default async function(pi) {
  const result = await new Promise(resolve => {
    const cp = spawn("pwd", [], { cwd: "`+cwd+`" });
    cp.stdout.on("data", (data) => { resolve(data.toString().trim()); });
  });
  pi.registerCommand("result", {description: result, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", cwd)
}

func TestExecFileCallbackDeferred(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { execFile } from "node:child_process";
export default async function(pi) {
  const output = await new Promise((resolve, reject) => {
    execFile("echo", ["execfile-test"], (err, stdout) => {
      if (err) reject(err);
      else resolve(stdout.trim());
    });
  });
  pi.registerCommand("result", {description: output, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "execfile-test")
}

// --- fetch improvement tests ---

func TestFetchArrayBufferIsArrayBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0x48, 0x49})
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const ab = await resp.arrayBuffer();
  const view = new Uint8Array(ab);
  pi.registerCommand("result", {description: view[0] + ":" + view[1] + ":" + (ab instanceof ArrayBuffer), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "72:73:true")
}

func TestFetchHeadersFromPairs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("X-Custom"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`", {
    headers: [["X-Custom", "pair-value"]]
  });
  const echoed = resp.headers.get("X-Echo");
  pi.registerCommand("result", {description: echoed, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "pair-value")
}

// --- EventEmitter tests ---

func TestEventEmitterOnEmit(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { EventEmitter } from "node:events";
export default function(pi) {
  const ee = new EventEmitter();
  const results = [];
  ee.on("data", (v) => results.push("got:" + v));
  ee.emit("data", "hello");
  ee.emit("data", "world");
  pi.registerCommand("result", {description: results.join(","), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "got:hello,got:world")
}

func TestEventEmitterOnce(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { EventEmitter } from "node:events";
export default function(pi) {
  const ee = new EventEmitter();
  let count = 0;
  ee.once("tick", () => count++);
  ee.emit("tick");
  ee.emit("tick");
  pi.registerCommand("result", {description: String(count), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "1")
}

func TestEventEmitterOff(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { EventEmitter } from "node:events";
export default function(pi) {
  const ee = new EventEmitter();
  let count = 0;
  const fn = () => count++;
  ee.on("tick", fn);
  ee.emit("tick");
  ee.off("tick", fn);
  ee.emit("tick");
  pi.registerCommand("result", {description: String(count), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "1")
}

func TestEventEmitterListenerCount(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { EventEmitter } from "node:events";
export default function(pi) {
  const ee = new EventEmitter();
  ee.on("x", () => {});
  ee.on("x", () => {});
  ee.on("y", () => {});
  pi.registerCommand("result", {description: ee.listenerCount("x") + ":" + ee.listenerCount("y"), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "2:1")
}

// --- Request/Response/Headers constructor tests ---

func TestHeadersConstructor(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const h = new Headers({"X-Test": "hello"});
  pi.registerCommand("result", {description: h.get("x-test"), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "hello")
}

func TestRequestConstructor(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const req = new Request("https://example.com", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: '{"key":"val"}'
  });
  pi.registerCommand("result", {
    description: req.url + ":" + req.method + ":" + req.headers.get("content-type"),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "https://example.com:POST:application/json")
}

func TestResponseConstructor(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = new Response('{"key":"val"}', {status: 201, headers: {"X-Custom": "yes"}});
  const data = await resp.json();
  pi.registerCommand("result", {
    description: resp.status + ":" + resp.ok + ":" + data.key + ":" + resp.headers.get("x-custom"),
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "201:true:val:yes")
}

func TestFetchWithRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"method":"` + r.Method + `","ct":"` + r.Header.Get("Content-Type") + `"}`))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const req = new Request("`+srv.URL+`", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: "{}"
  });
  const resp = await fetch(req);
  const data = await resp.json();
  pi.registerCommand("result", {
    description: data.method + ":" + data.ct,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "POST:application/json")
}

// --- Headers append/has-empty/get-combined tests ---

func TestHeadersAppendCombines(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const h = new Headers();
  h.append("x-multi", "a");
  h.append("x-multi", "b");
  pi.registerCommand("result", {description: h.get("x-multi"), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "a, b")
}

func TestHeadersHasEmptyValue(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default function(pi) {
  const h = new Headers();
  h.set("x-empty", "");
  const has = h.has("x-empty");
  const val = h.get("x-empty");
  pi.registerCommand("result", {description: has + ":" + JSON.stringify(val), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", `true:""`)
}

// --- Response.body streaming test ---

func TestResponseBodyGetReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("stream-me"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const reader = resp.body.getReader();
  const { value, done } = await reader.read();
  const text = Buffer.from(value).toString();
  const { done: done2 } = await reader.read();
  pi.registerCommand("result", {description: text + ":" + done + ":" + done2, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "stream-me:false:true")
}

func TestResponseConstructorBody(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = new Response("hello-body");
  const reader = resp.body.getReader();
  const { value, done } = await reader.read();
  const text = Buffer.from(value).toString();
  pi.registerCommand("result", {description: text + ":" + done, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "hello-body:false")
}

// --- streaming fetch tests ---

func TestFetchStreamingChunkedBody(t *testing.T) {
	releaseChunk2 := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("chunk-one"))
		flusher.Flush()
		<-releaseChunk2
		_, _ = w.Write([]byte("chunk-two"))
	}))
	defer srv.Close()

	cwd := t.TempDir()
	marker := filepath.Join(cwd, "chunk1-read")

	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				close(releaseChunk2)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(releaseChunk2)
	}()

	result := loadAndRunExtension(t, cwd, `
import * as fs from "node:fs";
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const reader = resp.body.getReader();
  const c1 = await reader.read();
  const text1 = Buffer.from(c1.value).toString();
  fs.writeFileSync("`+marker+`", text1);
  const c2 = await reader.read();
  const text2 = Buffer.from(c2.value).toString();
  const c3 = await reader.read();
  pi.registerCommand("result", {
    description: text1 + "|" + text2 + "|" + c1.done + "|" + c2.done + "|" + c3.done,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "chunk-one|chunk-two|false|false|true")
}

func TestFetchBodyUsedRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const text = await resp.text();
  let secondFailed = false;
  try { await resp.text(); } catch(e) { secondFailed = true; }
  let readerFailed = false;
  try { resp.body.getReader(); } catch(e) { readerFailed = true; }
  pi.registerCommand("result", {
    description: text + ":" + secondFailed + ":" + readerFailed + ":" + resp.bodyUsed,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "hello:true:true:true")
}

func TestFetchStreamBodyExceedsLimit(t *testing.T) {
	old := fetchMaxResponseBody
	fetchMaxResponseBody = 20
	defer func() { fetchMaxResponseBody = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 30)))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  try {
    await resp.text();
    pi.registerCommand("result", {description: "no-error", handler: async () => {}});
  } catch(e) {
    pi.registerCommand("result", {description: "rejected:" + e.message.includes("limit"), handler: async () => {}});
  }
}
`)
	assertRegisteredDescription(t, result, "result", "rejected:true")
}

func TestFetchStreamReaderExceedsLimit(t *testing.T) {
	old := fetchMaxResponseBody
	fetchMaxResponseBody = 10
	defer func() { fetchMaxResponseBody = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 20)))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const reader = resp.body.getReader();
  try {
    while (true) {
      const { done } = await reader.read();
      if (done) break;
    }
    pi.registerCommand("result", {description: "no-error", handler: async () => {}});
  } catch(e) {
    pi.registerCommand("result", {description: "rejected", handler: async () => {}});
  }
}
`)
	assertRegisteredDescription(t, result, "result", "rejected")
}

func TestFetchReaderCancelNoLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("start"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const reader = resp.body.getReader();
  const { value } = await reader.read();
  await reader.cancel();
  pi.registerCommand("result", {description: Buffer.from(value).toString(), handler: async () => {}});
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	assertRegisteredDescription(t, loaded, "result", "start")
	done := make(chan struct{})
	go func() {
		loader.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() blocked — resource leak after reader.cancel()")
	}
}

func TestFetchVMCloseClosesStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("partial"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const reader = resp.body.getReader();
  await reader.read();
  pi.registerCommand("result", {description: "ok", handler: async () => {}});
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	done := make(chan struct{})
	go func() {
		loader.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() blocked — stream not cleaned up")
	}
}

func TestResponseConstructorBodyUsed(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = new Response('{"a":1}');
  const data = await resp.json();
  let secondFailed = false;
  try { await resp.text(); } catch(e) { secondFailed = true; }
  pi.registerCommand("result", {
    description: data.a + ":" + secondFailed + ":" + resp.bodyUsed,
    handler: async () => {}
  });
}
`)
	assertRegisteredDescription(t, result, "result", "1:true:true")
}

// --- child_process env test ---

func TestExecWithEnv(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import { execSync } from "node:child_process";
export default function(pi) {
  const output = execSync("echo $MY_TEST_VAR", { env: { MY_TEST_VAR: "from-env", PATH: process.env.PATH } }).toString().trim();
  pi.registerCommand("result", {description: output, handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "from-env")
}

// --- mkdtemp relative prefix test ---

func TestMkdtempRelativePrefix(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { mkdtemp } from "node:fs/promises";
export default async function(pi) {
  const dir = await mkdtemp("rel-prefix-");
  const inCwd = dir.startsWith("`+cwd+`");
  pi.registerCommand("result", {description: String(inCwd), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true")
}

// --- arch mapping test ---

func TestArchMapping(t *testing.T) {
	result := loadAndRunExtension(t, t.TempDir(), `
import * as os from "node:os";
export default function(pi) {
  const a = os.arch();
  const pa = process.arch;
  pi.registerCommand("result", {description: a + ":" + (a === pa), handler: async () => {}});
}
`)
	command := extensions.NewRunner(result.Registry, extensions.RunnerOptions{}).Command("result")
	if command == nil {
		t.Fatal("command not registered")
	}
	// On amd64 Linux, should be x64, not amd64
	expected := nodeArch()
	want := expected + ":true"
	if command.Description != want {
		t.Fatalf("arch = %q, want %q", command.Description, want)
	}
}

// --- deterministic headers test ---

func TestHeadersEntriesSorted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Z-Header", "z")
		w.Header().Set("A-Header", "a")
		w.Header().Set("M-Header", "m")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	result := loadAndRunExtension(t, t.TempDir(), `
export default async function(pi) {
  const resp = await fetch("`+srv.URL+`");
  const keys = [];
  resp.headers.forEach((v, k) => keys.push(k));
  // Verify sorted (case-insensitive)
  const sorted = [...keys].sort();
  const isSorted = JSON.stringify(keys) === JSON.stringify(sorted);
  pi.registerCommand("result", {description: String(isSorted), handler: async () => {}});
}
`)
	assertRegisteredDescription(t, result, "result", "true")
}

// --- helpers ---

func loadAndRunExtension(t *testing.T, cwd, source string) LoadResult {
	t.Helper()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, entry, source)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	return loaded
}

func assertRegisteredDescription(t *testing.T, result LoadResult, name, want string) {
	t.Helper()
	command := extensions.NewRunner(result.Registry, extensions.RunnerOptions{}).Command(name)
	if command == nil {
		t.Fatalf("command %q not registered", name)
	}
	if command.Description != want {
		t.Fatalf("command %q description = %q, want %q", name, command.Description, want)
	}
}

func loadUpstreamExample(t *testing.T, name string) {
	t.Helper()
	source := fixtureSource(t, name)
	cwd := t.TempDir()
	entry := filepath.Join(cwd, ".pi", "extensions", name)
	mustWrite(t, entry, source)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ProjectTrusted: true,
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("upstream example %s load errors = %#v", name, loaded.Errors)
	}
	if len(loaded.Registry.Extensions()) == 0 {
		t.Fatalf("upstream example %s registered no extensions", name)
	}
}

// pathCorpusJSON returns a 119-entry Node v24 path.posix differential corpus.
func pathCorpusJSON() string {
	return `[{"fn":"basename","args":[""],"want":""},{"fn":"basename","args":["/"],"want":""},{"fn":"basename","args":["//"],"want":""},{"fn":"basename","args":["foo"],"want":"foo"},{"fn":"basename","args":["foo/"],"want":"foo"},{"fn":"basename","args":["/foo"],"want":"foo"},{"fn":"basename","args":["/foo/"],"want":"foo"},{"fn":"basename","args":["/a/b"],"want":"b"},{"fn":"basename","args":["/a/b/"],"want":"b"},{"fn":"basename","args":["a/b"],"want":"b"},{"fn":"basename","args":["."],"want":"."},{"fn":"basename","args":[".."],"want":".."},{"fn":"basename","args":["foo","foo"],"want":""},{"fn":"basename","args":["foo","oo"],"want":"f"},{"fn":"basename","args":["foo","bar"],"want":"foo"},{"fn":"basename","args":[".js",".js"],"want":""},{"fn":"basename","args":["file.js",".js"],"want":"file"},{"fn":"basename","args":["file.js","js"],"want":"file."},{"fn":"basename","args":["a/b","b"],"want":"b"},{"fn":"basename","args":[".bashrc","rc"],"want":".bash"},{"fn":"dirname","args":[""],"want":"."},{"fn":"dirname","args":["/"],"want":"/"},{"fn":"dirname","args":["//"],"want":"/"},{"fn":"dirname","args":["foo"],"want":"."},{"fn":"dirname","args":["/foo"],"want":"/"},{"fn":"dirname","args":["/a/b"],"want":"/a"},{"fn":"dirname","args":["/a/b/"],"want":"/a"},{"fn":"dirname","args":["a/b"],"want":"a"},{"fn":"dirname","args":["."],"want":"."},{"fn":"dirname","args":[".."],"want":"."},{"fn":"dirname","args":["/a/b/c"],"want":"/a/b"},{"fn":"extname","args":[""],"want":""},{"fn":"extname","args":["."],"want":""},{"fn":"extname","args":[".."],"want":""},{"fn":"extname","args":["..."],"want":"."},{"fn":"extname","args":["file"],"want":""},{"fn":"extname","args":["file.txt"],"want":".txt"},{"fn":"extname","args":["file."],"want":"."},{"fn":"extname","args":[".bashrc"],"want":""},{"fn":"extname","args":[".file.txt"],"want":".txt"},{"fn":"extname","args":["/path/to/.gitignore"],"want":""},{"fn":"extname","args":["file.tar.gz"],"want":".gz"},{"fn":"extname","args":["/a/b/c.js"],"want":".js"},{"fn":"extname","args":["a/.b.c"],"want":".c"},{"fn":"extname","args":["..txt"],"want":".txt"},{"fn":"normalize","args":[""],"want":"."},{"fn":"normalize","args":["."],"want":"."},{"fn":"normalize","args":[".."],"want":".."},{"fn":"normalize","args":["/"],"want":"/"},{"fn":"normalize","args":["//"],"want":"/"},{"fn":"normalize","args":["///"],"want":"/"},{"fn":"normalize","args":["a//b"],"want":"a/b"},{"fn":"normalize","args":["/a//b"],"want":"/a/b"},{"fn":"normalize","args":["a/./b"],"want":"a/b"},{"fn":"normalize","args":["a/../b"],"want":"b"},{"fn":"normalize","args":["/a/b/../c"],"want":"/a/c"},{"fn":"normalize","args":["a/../.."],"want":".."},{"fn":"normalize","args":["../.."],"want":"../.."},{"fn":"normalize","args":["a/b/"],"want":"a/b/"},{"fn":"normalize","args":["/a/b/"],"want":"/a/b/"},{"fn":"normalize","args":["///a///b///"],"want":"/a/b/"},{"fn":"join","args":[],"want":"."},{"fn":"join","args":[""],"want":"."},{"fn":"join","args":["",""],"want":"."},{"fn":"join","args":["a","b"],"want":"a/b"},{"fn":"join","args":["/a","b"],"want":"/a/b"},{"fn":"join","args":["a","..","b"],"want":"b"},{"fn":"join","args":["a","b",".."],"want":"a"},{"fn":"join","args":["/","a"],"want":"/a"},{"fn":"join","args":["a","","b"],"want":"a/b"},{"fn":"join","args":[".","a"],"want":"a"},{"fn":"isAbsolute","args":[""],"want":false},{"fn":"isAbsolute","args":["/"],"want":true},{"fn":"isAbsolute","args":["/a"],"want":true},{"fn":"isAbsolute","args":["a"],"want":false},{"fn":"isAbsolute","args":["./a"],"want":false},{"fn":"isAbsolute","args":["../a"],"want":false},{"fn":"resolve","args":["/a","b","c"],"want":"/a/b/c"},{"fn":"resolve","args":["/a","/b","c"],"want":"/b/c"},{"fn":"resolve","args":["/a","b","/c"],"want":"/c"},{"fn":"resolve","args":["/a",".","b"],"want":"/a/b"},{"fn":"resolve","args":["/a","..","b"],"want":"/b"},{"fn":"resolve","args":["/"],"want":"/"},{"fn":"resolve","args":["/a/b"],"want":"/a/b"},{"fn":"resolve","args":["/a",""],"want":"/a"},{"fn":"relative","args":["/a/b","/a/c"],"want":"../c"},{"fn":"relative","args":["/a/b","/a/b"],"want":""},{"fn":"relative","args":["/a/b/c","/a/d"],"want":"../../d"},{"fn":"relative","args":["/","/a"],"want":"a"},{"fn":"relative","args":["/","/"],"want":""},{"fn":"relative","args":["/a","/"],"want":".."},{"fn":"relative","args":["/a/b","/a/b/c/d"],"want":"c/d"},{"fn":"relative","args":["/a/b/c","/a/b/c"],"want":""},{"fn":"relative","args":["/a/b","/c/d"],"want":"../../c/d"},{"fn":"parse","args":[""],"want":{"root":"","dir":"","base":"","ext":"","name":""}},{"fn":"parse","args":["/"],"want":{"root":"/","dir":"/","base":"","ext":"","name":""}},{"fn":"parse","args":["foo"],"want":{"root":"","dir":"","base":"foo","ext":"","name":"foo"}},{"fn":"parse","args":["/foo"],"want":{"root":"/","dir":"/","base":"foo","ext":"","name":"foo"}},{"fn":"parse","args":["/a/b/c.txt"],"want":{"root":"/","dir":"/a/b","base":"c.txt","ext":".txt","name":"c"}},{"fn":"parse","args":["a.b.c"],"want":{"root":"","dir":"","base":"a.b.c","ext":".c","name":"a.b"}},{"fn":"parse","args":["."],"want":{"root":"","dir":"","base":".","ext":"","name":"."}},{"fn":"parse","args":[".."],"want":{"root":"","dir":"","base":"..","ext":"","name":".."}},{"fn":"parse","args":[".bashrc"],"want":{"root":"","dir":"","base":".bashrc","ext":"","name":".bashrc"}},{"fn":"parse","args":[".file.txt"],"want":{"root":"","dir":"","base":".file.txt","ext":".txt","name":".file"}},{"fn":"parse","args":["/a/b/"],"want":{"root":"/","dir":"/a","base":"b","ext":"","name":"b"}},{"fn":"parse","args":["a/b"],"want":{"root":"","dir":"a","base":"b","ext":"","name":"b"}},{"fn":"format","args":[{}],"want":""},{"fn":"format","args":[{"dir":".","base":"file.txt"}],"want":"./file.txt"},{"fn":"format","args":[{"root":"/","name":"file"}],"want":"/file"},{"fn":"format","args":[{"name":"file","ext":".txt"}],"want":"file.txt"},{"fn":"format","args":[{"ext":".txt"}],"want":".txt"},{"fn":"format","args":[{"root":"/"}],"want":"/"},{"fn":"format","args":[{"dir":"/","base":""}],"want":"//"},{"fn":"format","args":[{"dir":"/a","base":"b"}],"want":"/a/b"},{"fn":"format","args":[{"base":"file.txt"}],"want":"file.txt"},{"fn":"format","args":[{"dir":"/home/user","base":"file.txt"}],"want":"/home/user/file.txt"},{"fn":"format","args":[{"name":"f","ext":""}],"want":"f"},{"fn":"format","args":[{"dir":"","base":"a"}],"want":"a"},{"fn":"format","args":[{"name":"file","ext":"txt"}],"want":"file.txt"}]`
}
