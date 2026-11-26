import test from "node:test";
import assert from "node:assert/strict";
import { stripAnsi, hasVisibleText, named, tidy } from "../src/lib/utils.js";

test("stripAnsi removes CSI escape sequences and bare SGR codes", () => {
  assert.equal(stripAnsi("\x1b[1;32mhello\x1b[0m world"), "hello world");
  assert.equal(stripAnsi("[38;5;240mdim[0m"), "dim");
  assert.equal(stripAnsi(""), "");
});

test("hasVisibleText ignores lone status dots/ANSI and detects real text", () => {
  assert.equal(hasVisibleText(["○"]), false);
  assert.equal(hasVisibleText(["●"]), false);
  assert.equal(hasVisibleText(["\x1b[32m●\x1b[0m   "]), false);
  assert.equal(hasVisibleText([]), false);
  assert.equal(hasVisibleText(["○ 2 tasks"]), true);
  assert.equal(hasVisibleText(["\x1b[1mready\x1b[0m"]), true);
});

test("named capitalizes first character and defaults to New session", () => {
  assert.equal(named("deploy check"), "Deploy check");
  assert.equal(named(""), "New session");
  assert.equal(named(null), "New session");
});

test("tidy replaces /Users/<name> and /home/<name> with ~", () => {
  assert.equal(tidy("/Users/alice/dev/orb"), "~/dev/orb");
  assert.equal(tidy("/home/bob/work"), "~/work");
  assert.equal(tidy("/srv/agents/buzz"), "/srv/agents/buzz");
  assert.equal(tidy(""), "");
});
