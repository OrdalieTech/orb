import test from "node:test";
import assert from "node:assert/strict";
import { fuzzyFilterModels } from "../src/lib/modelSearch.js";

const CATALOG = [
  "anthropic/claude-opus-5-5",
  "anthropic/claude-sonnet-4-6",
  "anthropic/claude-haiku-4-5",
  "openai/gpt-5.4",
  "openai/gpt-5.4-mini",
  "openai-codex/gpt-6.2-sol",
  "google/gemini-4-neon",
  "google/gemini-2.5-pro",
  "deepseek/deepseek-r1",
  "deepseek/deepseek-chat-v3",
  "xai/grok-4",
];

const PROVIDERS = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  "openai-codex": "OpenAI Codex",
  google: "Google",
  deepseek: "DeepSeek",
  xai: "xAI",
};

const getProvider = id => PROVIDERS[id] || "";

test("empty or whitespace query returns all models in original order", () => {
  assert.deepEqual(fuzzyFilterModels(CATALOG, "", getProvider), CATALOG);
  assert.deepEqual(fuzzyFilterModels(CATALOG, "   ", getProvider), CATALOG);
});

test("separator-agnostic matching handles dots, hyphens, and compact numbers", () => {
  const r54 = fuzzyFilterModels(CATALOG, "54", getProvider);
  assert.equal(r54[0], "openai/gpt-5.4");
  assert.ok(r54.includes("openai/gpt-5.4-mini"));

  assert.equal(fuzzyFilterModels(CATALOG, "opus 5.5", getProvider)[0], "anthropic/claude-opus-5-5");
  assert.equal(fuzzyFilterModels(CATALOG, "opus55", getProvider)[0], "anthropic/claude-opus-5-5");
  assert.equal(fuzzyFilterModels(CATALOG, "codex 62", getProvider)[0], "openai-codex/gpt-6.2-sol");
});

test("multi-token AND matching works in any order", () => {
  const a = fuzzyFilterModels(CATALOG, "sonnet anthropic 46", getProvider);
  const b = fuzzyFilterModels(CATALOG, "4.6 sonnet", getProvider);
  assert.equal(a[0], "anthropic/claude-sonnet-4-6");
  assert.equal(b[0], "anthropic/claude-sonnet-4-6");

  // Non-matching extra token excludes the model
  assert.deepEqual(fuzzyFilterModels(CATALOG, "sonnet openai", getProvider), []);
});

test("ordered subsequence acronyms match expected models", () => {
  const g4n = fuzzyFilterModels(CATALOG, "g4n", getProvider);
  assert.equal(g4n[0], "google/gemini-4-neon");

  const gem4n = fuzzyFilterModels(CATALOG, "gem 4 n", getProvider);
  assert.equal(gem4n[0], "google/gemini-4-neon");

  const cs46 = fuzzyFilterModels(CATALOG, "cs46", getProvider);
  assert.equal(cs46[0], "anthropic/claude-sonnet-4-6");
});

test("shorthand provider aliases expand properly", () => {
  const oai = fuzzyFilterModels(CATALOG, "oai 54", getProvider);
  assert.equal(oai[0], "openai/gpt-5.4");

  const anth = fuzzyFilterModels(CATALOG, "anth haiku", getProvider);
  assert.equal(anth[0], "anthropic/claude-haiku-4-5");

  const gem = fuzzyFilterModels(CATALOG, "gem pro", getProvider);
  assert.equal(gem[0], "google/gemini-2.5-pro");

  const ds = fuzzyFilterModels(CATALOG, "ds r1", getProvider);
  assert.equal(ds[0], "deepseek/deepseek-r1");
});

test("relevance ranking prefers exact > prefix > shorter ID tie-breaker", () => {
  const exactVsPrefix = fuzzyFilterModels(
    ["openai/gpt-5.4-mini-preview", "openai/gpt-5.4-mini", "openai/gpt-5.4"],
    "gpt-5.4",
    getProvider,
  );
  assert.deepEqual(exactVsPrefix, [
    "openai/gpt-5.4",
    "openai/gpt-5.4-mini",
    "openai/gpt-5.4-mini-preview",
  ]);
});
