#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import {
  mkdir,
  open,
  readFile,
  rename,
  rm,
} from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

const EXPECTED_CORPUS_SIZE = 44;
const UPSTREAM_VERSION = "0.81.1";
const HERE = path.dirname(fileURLToPath(import.meta.url));
const SELF = fileURLToPath(import.meta.url);

const HARNESS_SOURCES = [
  ["matrix", path.join(HERE, "matrix.mjs")],
  ["observer", path.join(HERE, "observer.ts")],
  ["prepare", path.join(HERE, "prepare.mjs")],
  ["smoke", path.join(HERE, "smoke.mjs")],
  ["smokeCases", path.join(HERE, "smoke-cases.json")],
  ["packageManifest", path.join(HERE, "package.json")],
  ["packageLock", path.join(HERE, "package-lock.json")],
  ["report", SELF],
];

function usage() {
  return `Usage: node conformance/extensions/report.mjs \\
  --matrix <raw-matrix.json> \\
  --smoke <raw-smoke.json> \\
  --output <compact-report.json> \\
  [--corpus <corpus.json>] \\
  [--workflow-audit <workflow-audit.json>]

The corpus and workflow audit default to the files beside this script. The
output is deterministic for identical inputs and is replaced atomically.`;
}

function parseArgs(argv) {
  const options = {
    corpus: path.join(HERE, "corpus.json"),
    workflowAudit: path.join(HERE, "workflow-audit.json"),
  };
  const names = new Map([
    ["--matrix", "matrix"],
    ["--smoke", "smoke"],
    ["--output", "output"],
    ["--corpus", "corpus"],
    ["--workflow-audit", "workflowAudit"],
  ]);

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") {
      process.stdout.write(`${usage()}\n`);
      process.exit(0);
    }
    const name = names.get(arg);
    if (!name) {
      throw new Error(`unknown argument ${JSON.stringify(arg)}\n\n${usage()}`);
    }
    const value = argv[i + 1];
    if (!value || value.startsWith("--")) {
      throw new Error(`${arg} requires a path\n\n${usage()}`);
    }
    if (Object.hasOwn(options, name) && !["corpus", "workflowAudit"].includes(name)) {
      throw new Error(`${arg} was provided more than once`);
    }
    options[name] = value;
    i += 1;
  }

  for (const name of ["matrix", "smoke", "output"]) {
    if (!options[name]) {
      throw new Error(`--${name} is required\n\n${usage()}`);
    }
  }
  return Object.fromEntries(
    Object.entries(options).map(([name, value]) => [name, path.resolve(value)]),
  );
}

function expect(condition, message) {
  if (!condition) {
    throw new Error(`validation failed: ${message}`);
  }
}

function expectObject(value, label) {
  expect(value !== null && typeof value === "object" && !Array.isArray(value), `${label} must be an object`);
}

function expectString(value, label) {
  expect(typeof value === "string" && value.length > 0, `${label} must be a non-empty string`);
}

function expectFinite(value, label) {
  expect(Number.isFinite(value), `${label} must be a finite number`);
}

function expectTimestamp(value, label) {
  expectString(value, label);
  expect(Number.isFinite(Date.parse(value)), `${label} must be an ISO timestamp`);
}

function expectEqual(actual, expected, label) {
  expect(isDeepStrictEqual(actual, expected), `${label} does not match its authoritative source`);
}

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function parseJSON(bytes, label) {
  try {
    return JSON.parse(bytes.toString("utf8"));
  } catch (error) {
    throw new Error(`${label} is not valid JSON: ${error.message}`);
  }
}

async function loadFile(file, label, json = false) {
  let bytes;
  try {
    bytes = await readFile(file);
  } catch (error) {
    throw new Error(`cannot read ${label}: ${error.message}`);
  }
  return {
    bytes,
    sha256: sha256(bytes),
    value: json ? parseJSON(bytes, label) : undefined,
  };
}

function normalizeEntrypoint(value) {
  return value.replace(/^\.\//, "");
}

function percent(numerator, denominator) {
  return Number(((numerator / denominator) * 100).toFixed(1));
}

function countBy(values, key) {
  const counts = new Map();
  for (const value of values) {
    const name = key(value);
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return Object.fromEntries([...counts].sort(([a], [b]) => a.localeCompare(b)));
}

function median(values) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const value = sorted.length % 2 === 1
    ? sorted[middle]
    : (sorted[middle - 1] + sorted[middle]) / 2;
  return Number(value.toFixed(3));
}

function compactStats(stats) {
  if (!stats) return null;
  return {
    n: stats.n,
    medianMs: stats.medianMs,
    p90Ms: stats.p90Ms,
    madMs: stats.madMs,
    noisy: stats.noisy,
  };
}

function validateStats(stats, expectedSamples, label, required) {
  expectObject(stats, label);
  expect(Number.isInteger(stats.n) && stats.n >= 0, `${label}.n must be a non-negative integer`);
  if (required) {
    expect(stats.n === expectedSamples, `${label}.n must equal the configured sample count`);
  }
  if (stats.n === 0) {
    expect(stats.medianMs === null && stats.p90Ms === null && stats.madMs === null, `${label} has timings without samples`);
    return;
  }
  expectFinite(stats.medianMs, `${label}.medianMs`);
  expectFinite(stats.p90Ms, `${label}.p90Ms`);
  expectFinite(stats.madMs, `${label}.madMs`);
  expect(typeof stats.noisy === "boolean", `${label}.noisy must be boolean when samples exist`);
}

function validateCorpus(corpus) {
  expectObject(corpus, "corpus");
  expect(corpus.schemaVersion === 1, "corpus.schemaVersion must be 1");
  expectString(corpus.capturedAt, "corpus.capturedAt");
  expectObject(corpus.selection, "corpus.selection");
  expect(Array.isArray(corpus.extensions), "corpus.extensions must be an array");
  expect(corpus.extensions.length === EXPECTED_CORPUS_SIZE, `corpus must contain exactly ${EXPECTED_CORPUS_SIZE} extensions`);

  const packages = new Set();
  for (const [index, extension] of corpus.extensions.entries()) {
    const label = `corpus.extensions[${index}]`;
    expectObject(extension, label);
    expect(extension.rank === index + 1, `${label}.rank must be ${index + 1}`);
    expectString(extension.package, `${label}.package`);
    expect(!packages.has(extension.package), `${label}.package is duplicated`);
    packages.add(extension.package);
    expectString(extension.version, `${label}.version`);
    expectString(extension.integrity, `${label}.integrity`);
    expect(extension.integrity.startsWith("sha512-"), `${label}.integrity must be an npm sha512 integrity`);
    expectObject(extension.downloads, `${label}.downloads`);
    expectFinite(extension.downloads.monthly, `${label}.downloads.monthly`);
    expectFinite(extension.downloads.weekly, `${label}.downloads.weekly`);
    expect(Array.isArray(extension.extensions) && extension.extensions.length > 0, `${label}.extensions must be non-empty`);
    for (const entrypoint of extension.extensions) {
      expectString(entrypoint, `${label}.extensions entry`);
    }
  }
}

function validateRuntimeProbe(probe, method, label) {
  expectObject(probe, label);
  expect(Array.isArray(probe.attempts), `${label}.attempts must be an array`);
  expect(probe.attempts.length === method.warmups + method.samples, `${label} attempt count is incomplete`);
  expect(["stable", "failed", "flaky", "infra_error"].includes(probe.state), `${label}.state is unknown`);
  expect(typeof probe.ok === "boolean", `${label}.ok must be boolean`);
  expect(typeof probe.registrationStable === "boolean", `${label}.registrationStable must be boolean`);
  validateStats(probe.startup, method.samples, `${label}.startup`, probe.ok);
  validateStats(probe.command, method.samples, `${label}.command`, probe.ok);
}

function validateMatrix(matrix, corpus) {
  expectObject(matrix, "matrix");
  expect(matrix.schemaVersion === 2, "matrix.schemaVersion must be 2");
  expectTimestamp(matrix.generatedAt, "matrix.generatedAt");
  expectObject(matrix.method, "matrix.method");
  expect(Number.isInteger(matrix.method.warmups) && matrix.method.warmups > 0, "matrix.method.warmups must be positive");
  expect(Number.isInteger(matrix.method.samples) && matrix.method.samples > 0, "matrix.method.samples must be positive");
  expect(matrix.method.interleaved === true, "matrix probes must be interleaved");
  expectString(matrix.method.network, "matrix.method.network");
  expect(matrix.safety?.networkNamespaceGuard?.isolated === true, "matrix did not prove network namespace isolation");
  expectEqual(matrix.safety.networkNamespaceGuard.external, [], "matrix external network interfaces");
  expect(matrix.safety.credentialsInherited === false, "matrix inherited host credentials");
  expectObject(matrix.inputs, "matrix.inputs");
  expectObject(matrix.corpus, "matrix.corpus");
  expect(matrix.corpus.count === EXPECTED_CORPUS_SIZE && matrix.corpus.totalCount === EXPECTED_CORPUS_SIZE, "matrix corpus count is incomplete");
  expect(matrix.corpus.capturedAt === corpus.capturedAt, "matrix corpus capture date is stale");
  expectEqual(matrix.corpus.selection, corpus.selection, "matrix corpus selection");
  expectObject(matrix.runtimes, "matrix.runtimes");
  expect(matrix.runtimes.pi?.version?.status === 0, "matrix Pi version probe failed");
  expect(matrix.runtimes.pi.version.stdout.trim() === UPSTREAM_VERSION, `matrix Pi version must be ${UPSTREAM_VERSION}`);
  expect(matrix.runtimes.pigo?.version?.status === 0, "matrix Pigo version probe failed");
  expect(matrix.runtimes.pigo.version.stdout.includes(`upstream pi ${UPSTREAM_VERSION}`), `matrix Pigo must target upstream ${UPSTREAM_VERSION}`);

  for (const runtime of ["pi", "pigo"]) {
    validateRuntimeProbe(matrix.baseline?.[runtime], matrix.method, `matrix.baseline.${runtime}`);
    expect(matrix.baseline[runtime].state === "stable" && matrix.baseline[runtime].ok, `matrix.baseline.${runtime} must be stable and successful`);
    expect(matrix.baseline[runtime].registrationStable, `matrix.baseline.${runtime} registration must be stable`);
  }

  expect(Array.isArray(matrix.extensions), "matrix.extensions must be an array");
  expect(matrix.extensions.length === EXPECTED_CORPUS_SIZE, "matrix extension probes are incomplete");
  const statuses = ["load_register_pass", "load_only_pass", "unsupported"];
  for (const [index, result] of matrix.extensions.entries()) {
    const label = `matrix.extensions[${index}]`;
    const expected = corpus.extensions[index];
    expectObject(result, label);
    expectObject(result.extension, `${label}.extension`);
    for (const field of ["rank", "package", "version", "downloads", "integrity"]) {
      expectEqual(result.extension[field], expected[field], `${label}.extension.${field}`);
    }
    expectEqual(
      result.extension.extensions.map(normalizeEntrypoint),
      expected.extensions.map(normalizeEntrypoint),
      `${label}.extension.extensions`,
    );
    expect(statuses.includes(result.status), `${label}.status is unknown`);
    expectString(result.reason, `${label}.reason`);
    expect(result.upstreamSupported === true, `${label} must be supported by the upstream reference`);
    validateRuntimeProbe(result.pi, matrix.method, `${label}.pi`);
    validateRuntimeProbe(result.pigo, matrix.method, `${label}.pigo`);
    expect(result.pi.state === "stable" && result.pi.ok && result.pi.registrationStable, `${label}.pi must load stably`);
    if (result.status === "unsupported") {
      expect(!result.pigo.ok && result.reason === "pigo_load_failure", `${label} unsupported status must represent a Pigo load failure`);
      expect(result.performance?.available === false, `${label} cannot publish performance for a failed load`);
    } else {
      expect(result.pigo.state === "stable" && result.pigo.ok && result.pigo.registrationStable, `${label}.pigo pass must be stable`);
      expect(result.reason === result.status, `${label}.reason must agree with its pass status`);
      expect(result.performance?.available === true, `${label} must publish performance for stable probes`);
    }
  }

  expectObject(matrix.summary, "matrix.summary");
  expect(matrix.summary.valid === true && matrix.summary.completeCorpus === true, "matrix summary is not valid and complete");
  const counts = countBy(matrix.extensions, (entry) => entry.status);
  const normalizedCounts = Object.fromEntries(statuses.map((status) => [status, counts[status] ?? 0]));
  normalizedCounts.flaky = 0;
  normalizedCounts.infra_error = 0;
  expectEqual(matrix.summary.counts, normalizedCounts, "matrix.summary.counts");
  expect(matrix.summary.counts.flaky === 0 && matrix.summary.counts.infra_error === 0, "matrix contains flaky or infrastructure results");

  const reasons = countBy(matrix.extensions, (entry) => entry.reason);
  expectEqual(matrix.summary.reasons, reasons, "matrix.summary.reasons");
  const loadRegisterPass = counts.load_register_pass ?? 0;
  const loadOnlyPass = counts.load_only_pass ?? 0;
  const loadCompatible = loadRegisterPass + loadOnlyPass;
  const expectedTotals = {
    total: EXPECTED_CORPUS_SIZE,
    tested: EXPECTED_CORPUS_SIZE,
    loadRegisterPass,
    loadOnlyPass,
    loadCompatible,
    loadCompatiblePercent: percent(loadCompatible, EXPECTED_CORPUS_SIZE),
    loadRegisterPercent: percent(loadRegisterPass, EXPECTED_CORPUS_SIZE),
  };
  expectEqual(matrix.summary.allCorpus, expectedTotals, "matrix.summary.allCorpus");
  expect(matrix.summary.parity?.upstreamSupported === EXPECTED_CORPUS_SIZE, "matrix parity denominator is incomplete");
  for (const [key, value] of Object.entries(expectedTotals)) {
    if (key !== "total" && key !== "tested") {
      expect(matrix.summary.parity[key] === value, `matrix.summary.parity.${key} is inconsistent`);
    }
  }
}

function validateCaseIdentity(result, definition, corpusEntry, label) {
  for (const key of ["id", "rank", "package", "command", "args", "rationale", "evidencePatterns"]) {
    expectEqual(result[key], definition[key], `${label}.${key}`);
  }
  expect(result.version === corpusEntry.version, `${label}.version is stale`);
  expect(result.integrity === corpusEntry.integrity, `${label}.integrity is stale`);
}

function runtimeSmokePassed(result, kind) {
  return result.status === `${kind}_smoke_pass`;
}

function validateSmokeRuntime(result, inputs, label) {
  expectObject(result, label);
  expectString(result.status, `${label}.status`);
  expect(Array.isArray(result.attempts), `${label}.attempts must be an array`);
  expect(result.attempts.length === inputs.warmups + inputs.samples, `${label} attempt count is incomplete`);
  validateStats(result.measuredHandlerLatency, inputs.samples, `${label}.measuredHandlerLatency`, result.status.endsWith("_pass"));
}

function validateSmoke(smoke, cases, corpus, hashes) {
  expectObject(smoke, "smoke");
  expect(smoke.schemaVersion === 1, "smoke.schemaVersion must be 1");
  expectTimestamp(smoke.generatedAt, "smoke.generatedAt");
  expect(smoke.claimScope === cases.claimScope, "smoke claim scope does not match smoke-cases.json");
  expectEqual(smoke.inspectedExclusions, cases.inspectedExclusions, "smoke inspected exclusions");
  expectObject(smoke.inputs, "smoke.inputs");
  expect(smoke.inputs.harness?.sha256 === hashes.smoke, "smoke raw result is not bound to the current smoke.mjs");
  expect(smoke.inputs.cases?.sha256 === hashes.smokeCases, "smoke raw result is not bound to the current smoke-cases.json");
  expect(smoke.inputs.corpus?.sha256 === hashes.corpus, "smoke raw result is not bound to the current corpus.json");
  expect(smoke.inputs.upstreamPi?.version?.status === 0, "smoke Pi version probe failed");
  expect(smoke.inputs.upstreamPi.version.stdout.trim() === UPSTREAM_VERSION, `smoke Pi version must be ${UPSTREAM_VERSION}`);
  expect(smoke.inputs.pigo?.version?.status === 0, "smoke Pigo version probe failed");
  expect(smoke.inputs.pigo.version.stdout.includes(`upstream pi ${UPSTREAM_VERSION}`), `smoke Pigo must target upstream ${UPSTREAM_VERSION}`);
  expect(Number.isInteger(smoke.inputs.warmups) && smoke.inputs.warmups > 0, "smoke warmup count must be positive");
  expect(Number.isInteger(smoke.inputs.samples) && smoke.inputs.samples > 0, "smoke sample count must be positive");
  expect(smoke.inputs.packageLockSHA256 === hashes.packageLock, "smoke raw result is not bound to the current package-lock.json");

  expect(smoke.safety?.networkNamespaceGuard?.isolated === true, "smoke did not prove network namespace isolation");
  expectEqual(smoke.safety.networkNamespaceGuard.external, [], "smoke external network interfaces");
  expect(smoke.safety.allowNetworkOverrideUsed === false, "smoke used the network override");
  expect(smoke.safety.credentialsInherited === false, "smoke inherited credentials");
  expect(smoke.safety.modelActivityFailsAttempt === true, "smoke did not fail closed on model activity");
  expect(smoke.safety.dialogRequests === "cancelled", "smoke dialogs were not cancelled");

  expect(Array.isArray(cases.cases) && cases.cases.length > 0, "smoke-cases.json has no command cases");
  expect(Array.isArray(cases.workflowCases) && cases.workflowCases.length > 0, "smoke-cases.json has no workflow cases");
  expect(Array.isArray(smoke.commandResults), "smoke.commandResults must be an array");
  expect(Array.isArray(smoke.workflowResults), "smoke.workflowResults must be an array");
  expect(smoke.commandResults.length === cases.cases.length, "smoke command results are incomplete");
  expect(smoke.workflowResults.length === cases.workflowCases.length, "smoke workflow results are incomplete");

  const corpusByRank = new Map(corpus.extensions.map((entry) => [entry.rank, entry]));
  const validateResults = (results, definitions, kind) => {
    const definitionsByID = new Map(definitions.map((entry) => [entry.id, entry]));
    expect(definitionsByID.size === definitions.length, `${kind} case ids must be unique`);
    const seen = new Set();
    for (const [index, result] of results.entries()) {
      const label = `smoke.${kind}Results[${index}]`;
      const definition = definitionsByID.get(result.id);
      expect(definition, `${label}.id is not in smoke-cases.json`);
      expect(!seen.has(result.id), `${label}.id is duplicated`);
      seen.add(result.id);
      const corpusEntry = corpusByRank.get(definition.rank);
      expect(corpusEntry?.package === definition.package, `${label} does not identify a corpus package`);
      validateCaseIdentity(result, definition, corpusEntry, label);
      if (kind === "workflow") {
        expectEqual(result.fixtures, definition.fixtures, `${label}.fixtures`);
        expect(result.module.endsWith(normalizeEntrypoint(definition.module)), `${label}.module is stale`);
      }
      validateSmokeRuntime(result.pi, smoke.inputs, `${label}.pi`);
      validateSmokeRuntime(result.pigo, smoke.inputs, `${label}.pigo`);
      expectObject(result.comparison, `${label}.comparison`);
      expect(typeof result.comparison.parity === "boolean", `${label}.comparison.parity must be boolean`);
      expectObject(result.comparison.observableOutput, `${label}.comparison.observableOutput`);
      expect(result.comparison.observableOutput.stableWithinEachRuntime === true, `${label} observable output is unstable`);
      expect(
        result.comparison.parity === result.comparison.observableOutput.equalAcrossRuntimes,
        `${label}.comparison.parity does not match observable output equality`,
      );
      expectEqual(result.comparison.handlerLatency.pi, result.pi.measuredHandlerLatency, `${label} Pi handler latency`);
      expectEqual(result.comparison.handlerLatency.pigo, result.pigo.measuredHandlerLatency, `${label} Pigo handler latency`);
      if (kind === "workflow") {
        expect(result.comparison.workflowPayload?.stableWithinEachRuntime === true, `${label} workflow payload is unstable`);
        expect(
          result.comparison.parity === result.comparison.workflowPayload.equalAcrossRuntimes,
          `${label}.comparison.parity does not match workflow payload equality`,
        );
      }
    }
    expect(seen.size === definitions.length, `${kind} smoke cases are incomplete`);
  };
  validateResults(smoke.commandResults, cases.cases, "command");
  validateResults(smoke.workflowResults, cases.workflowCases, "workflow");

  const commandPassCount = smoke.commandResults.filter((result) => result.comparison.parity).length;
  const workflowPassCount = smoke.workflowResults.filter((result) => result.comparison.parity).length;
  const piCommandPassCount = smoke.commandResults.filter((result) => runtimeSmokePassed(result.pi, "command_handler")).length;
  const pigoCommandPassCount = smoke.commandResults.filter((result) => runtimeSmokePassed(result.pigo, "command_handler")).length;
  const piWorkflowPassCount = smoke.workflowResults.filter((result) => runtimeSmokePassed(result.pi, "workflow")).length;
  const pigoWorkflowPassCount = smoke.workflowResults.filter((result) => runtimeSmokePassed(result.pigo, "workflow")).length;
  const expectedSummary = {
    commandCaseCount: smoke.commandResults.length,
    commandPassCount,
    allCommandsComparedPass: commandPassCount === smoke.commandResults.length,
    piCommandPassCount,
    pigoCommandPassCount,
    workflowCaseCount: smoke.workflowResults.length,
    workflowPassCount,
    allWorkflowsComparedPass: workflowPassCount === smoke.workflowResults.length,
    piWorkflowPassCount,
    pigoWorkflowPassCount,
    allComparedPass: commandPassCount === smoke.commandResults.length && workflowPassCount === smoke.workflowResults.length,
  };
  expectEqual(smoke.summary, expectedSummary, "smoke.summary");
}

function validateAudit(audit, corpus, matrix) {
  expectObject(audit, "workflow audit");
  expect(audit.schemaVersion === 1, "workflow audit schemaVersion must be 1");
  expectString(audit.auditedAt, "workflow audit auditedAt");
  expectString(audit.caveat, "workflow audit caveat");
  expectString(audit.method, "workflow audit method");
  expect(Array.isArray(audit.entries), "workflow audit entries must be an array");
  expect(audit.entries.length === EXPECTED_CORPUS_SIZE, "workflow audit is incomplete");
  const verdicts = new Set(["likely_compatible", "partial", "main_feature_blocked", "load_blocked"]);
  for (const [index, entry] of audit.entries.entries()) {
    const label = `workflow audit.entries[${index}]`;
    const corpusEntry = corpus.extensions[index];
    const matrixEntry = matrix.extensions[index];
    expect(entry.rank === corpusEntry.rank && entry.package === corpusEntry.package, `${label} is stale or out of order`);
    expect(verdicts.has(entry.verdict), `${label}.verdict is unknown`);
    expectString(entry.blockerCategory, `${label}.blockerCategory`);
    expect(Array.isArray(entry.evidence) && entry.evidence.length > 0, `${label}.evidence must be non-empty`);
    for (const evidence of entry.evidence) expectString(evidence, `${label}.evidence entry`);
    expectString(entry.explanation, `${label}.explanation`);
    expectString(entry.remediation, `${label}.remediation`);
    expectString(entry.conditions, `${label}.conditions`);
    if (entry.verdict === "load_blocked") {
      expect(matrixEntry.status === "unsupported", `${label} conflicts with the measured load result`);
    } else {
      expect(matrixEntry.status !== "unsupported", `${label} claims workflow reachability for a load-blocked extension`);
    }
  }
}

function performanceSummary(entries, selector) {
  const values = entries.map(selector).filter(Boolean);
  const comparable = values.filter((value) => value.quality === "ok" && Number.isFinite(value.ratio));
  const ratios = comparable.map((value) => value.ratio);
  return {
    attempted: entries.length,
    measured: values.length,
    comparable: comparable.length,
    noisy: values.length - comparable.length,
    unavailable: entries.length - values.length,
    notDecisionGrade: entries.length - comparable.length,
    pigoFaster: ratios.filter((ratio) => ratio < 1).length,
    equal: ratios.filter((ratio) => ratio === 1).length,
    piFaster: ratios.filter((ratio) => ratio > 1).length,
    medianPigoVsPiRatio: median(ratios),
  };
}

function groupedBlockers(audit) {
  const groups = new Map();
  for (const entry of audit.entries) {
    let group = groups.get(entry.blockerCategory);
    if (!group) {
      group = { category: entry.blockerCategory, count: 0, verdicts: {}, extensions: [] };
      groups.set(entry.blockerCategory, group);
    }
    group.count += 1;
    group.verdicts[entry.verdict] = (group.verdicts[entry.verdict] ?? 0) + 1;
    group.extensions.push({ rank: entry.rank, package: entry.package });
  }
  return [...groups.values()]
    .map((group) => ({
      ...group,
      verdicts: Object.fromEntries(Object.entries(group.verdicts).sort(([a], [b]) => a.localeCompare(b))),
    }))
    .sort((a, b) => b.count - a.count || a.category.localeCompare(b.category));
}

function compactMismatch(output) {
  if (output.equalAcrossRuntimes) return undefined;
  return {
    pi: output.piRepresentative,
    pigo: output.pigoRepresentative,
  };
}

function compactSmokeResult(result, kind) {
  const compact = {
    id: result.id,
    rank: result.rank,
    package: result.package,
    version: result.version,
    command: result.command,
    args: result.args,
    rationale: result.rationale,
    parity: result.comparison.parity,
    runtimes: {
      pi: result.pi.status,
      pigo: result.pigo.status,
    },
    observableOutput: {
      stableWithinEachRuntime: result.comparison.observableOutput.stableWithinEachRuntime,
      equalAcrossRuntimes: result.comparison.observableOutput.equalAcrossRuntimes,
      piDistinctCount: result.comparison.observableOutput.piDistinctOutputCount,
      pigoDistinctCount: result.comparison.observableOutput.pigoDistinctOutputCount,
    },
    handlerLatency: result.comparison.handlerLatency,
  };
  const mismatch = compactMismatch(result.comparison.observableOutput);
  if (mismatch) compact.observableMismatch = mismatch;
  if (kind === "workflow") {
    compact.module = result.module;
    compact.workflowPayload = result.comparison.workflowPayload;
  }
  return compact;
}

function buildReport({ matrix, smoke, corpus, audit, raw, sources }) {
  const auditByRank = new Map(audit.entries.map((entry) => [entry.rank, entry]));
  const commandIDsByRank = new Map();
  const workflowIDsByRank = new Map();
  for (const result of smoke.commandResults) {
    commandIDsByRank.set(result.rank, [...(commandIDsByRank.get(result.rank) ?? []), result.id]);
  }
  for (const result of smoke.workflowResults) {
    workflowIDsByRank.set(result.rank, [...(workflowIDsByRank.get(result.rank) ?? []), result.id]);
  }

  const extensions = matrix.extensions.map((result) => {
    const workflow = auditByRank.get(result.extension.rank);
    return {
      rank: result.extension.rank,
      package: result.extension.package,
      version: result.extension.version,
      downloads: result.extension.downloads,
      load: {
        status: result.status,
        reason: result.reason,
        upstreamSupported: result.upstreamSupported,
        registrationDifference: result.registrationDifference,
      },
      workflow: {
        tier: workflow.verdict,
        blockerCategory: workflow.blockerCategory,
        rootCause: workflow.explanation,
        remediation: workflow.remediation,
        verificationNeeded: workflow.conditions,
        evidence: workflow.evidence,
      },
      smokeCoverage: {
        commandCases: commandIDsByRank.get(result.extension.rank) ?? [],
        workflowCases: workflowIDsByRank.get(result.extension.rank) ?? [],
      },
      timings: {
        pi: {
          state: result.pi.state,
          startup: compactStats(result.pi.startup),
          observerCommandRPC: compactStats(result.pi.command),
        },
        pigo: {
          state: result.pigo.state,
          startup: compactStats(result.pigo.startup),
          observerCommandRPC: compactStats(result.pigo.command),
        },
        comparison: result.performance,
      },
    };
  });

  const sourceAsOf = [matrix.generatedAt, smoke.generatedAt, `${audit.auditedAt}T00:00:00.000Z`]
    .sort((a, b) => Date.parse(b) - Date.parse(a))[0];
  const workflowCounts = countBy(audit.entries, (entry) => entry.verdict);
  const compactCommands = smoke.commandResults.map((result) => compactSmokeResult(result, "command"));
  const compactWorkflows = smoke.workflowResults.map((result) => compactSmokeResult(result, "workflow"));

  return {
    schemaVersion: 1,
    kind: "pigo-extension-compatibility-report",
    sourceAsOf,
    methodology: {
      population: {
        capturedAt: corpus.capturedAt,
        count: EXPECTED_CORPUS_SIZE,
        selection: corpus.selection,
      },
      loadAndRegistration: matrix.method,
      workflowAudit: {
        auditedAt: audit.auditedAt,
        method: audit.method,
        caveat: audit.caveat,
      },
      smoke: {
        claimScope: smoke.claimScope,
        warmups: smoke.inputs.warmups,
        samples: smoke.inputs.samples,
        timeoutMs: smoke.inputs.timeoutMs,
        settleMs: smoke.inputs.settleMs,
        safety: smoke.safety,
      },
      interpretation: [
        "Load compatibility proves stable extension evaluation; load/register additionally proves stable API registration. Neither proves the package's defining workflow.",
        "Workflow tiers are static, line-grounded judgments. Only entries named by a workflow smoke case have executed workflow evidence.",
        "Timing ratios are decision-grade only when quality is ok; noisy or unavailable ratios must not support a speed claim.",
      ],
    },
    provenance: {
      validation: {
        complete: true,
        corpusEntries: EXPECTED_CORPUS_SIZE,
        matrixEntries: matrix.extensions.length,
        workflowAuditEntries: audit.entries.length,
        commandSmokeCases: smoke.commandResults.length,
        workflowSmokeCases: smoke.workflowResults.length,
        corpusIdentityMatchedAcrossInputs: true,
        smokeEmbeddedSourceHashesMatched: true,
        matrixHarnessBinding: "raw matrix hashes match the current harness, corpus, observer, and package lock",
      },
      sourceTimestamps: {
        matrix: matrix.generatedAt,
        smoke: smoke.generatedAt,
        workflowAudit: audit.auditedAt,
      },
      runtimes: {
        node: matrix.runtimes.node,
        pi: {
          version: matrix.runtimes.pi.version.stdout.trim(),
          smokeBinarySHA256: smoke.inputs.upstreamPi.sha256,
        },
        pigo: {
          version: matrix.runtimes.pigo.version.stdout.trim(),
          smokeBinarySHA256: smoke.inputs.pigo.sha256,
        },
        packageLockSHA256: smoke.inputs.packageLockSHA256,
      },
      sha256: {
        rawInputs: raw,
        harnessSources: sources,
      },
    },
    summary: {
      loadAndRegistration: matrix.summary,
      workflowTiers: workflowCounts,
      smoke: smoke.summary,
      performance: {
        baseline: {
          pi: {
            startup: compactStats(matrix.baseline.pi.startup),
            observerCommandRPC: compactStats(matrix.baseline.pi.command),
          },
          pigo: {
            startup: compactStats(matrix.baseline.pigo.startup),
            observerCommandRPC: compactStats(matrix.baseline.pigo.command),
          },
        },
        extensionStartup: performanceSummary(matrix.extensions, (entry) => entry.performance?.available ? entry.performance.startup : null),
        baselineSubtractedLoad: performanceSummary(matrix.extensions, (entry) => entry.performance?.available ? entry.performance.baselineSubtractedLoad : null),
        commandHandlerSmoke: performanceSummary(smoke.commandResults, (entry) => ({
          quality: entry.comparison.handlerLatency.pigoVsPiRatio === null ? "noisy" : "ok",
          ratio: entry.comparison.handlerLatency.pigoVsPiRatio,
        })),
        workflowHandlerSmoke: performanceSummary(smoke.workflowResults, (entry) => ({
          quality: entry.comparison.handlerLatency.pigoVsPiRatio === null ? "noisy" : "ok",
          ratio: entry.comparison.handlerLatency.pigoVsPiRatio,
        })),
      },
      blockerCategories: groupedBlockers(audit),
    },
    extensions,
    commandSmokes: compactCommands,
    workflowSmokes: compactWorkflows,
    inspectedSmokeExclusions: smoke.inspectedExclusions,
  };
}

async function atomicWrite(file, contents) {
  const directory = path.dirname(file);
  await mkdir(directory, { recursive: true });
  const temporary = path.join(directory, `.${path.basename(file)}.${process.pid}.${randomUUID()}.tmp`);
  let handle;
  try {
    handle = await open(temporary, "wx", 0o644);
    await handle.writeFile(contents, "utf8");
    await handle.sync();
    await handle.close();
    handle = undefined;
    await rename(temporary, file);
  } catch (error) {
    if (handle) await handle.close().catch(() => {});
    await rm(temporary, { force: true }).catch(() => {});
    throw error;
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const inputs = [options.matrix, options.smoke, options.corpus, options.workflowAudit];
  expect(!inputs.includes(options.output), "output path must not overwrite an input");
  expect(new Set(inputs).size === inputs.length, "input paths must be distinct");

  const [matrixFile, smokeFile, corpusFile, auditFile, ...sourceFiles] = await Promise.all([
    loadFile(options.matrix, "raw matrix", true),
    loadFile(options.smoke, "raw smoke", true),
    loadFile(options.corpus, "corpus", true),
    loadFile(options.workflowAudit, "workflow audit", true),
    ...HARNESS_SOURCES.map(([name, file]) => loadFile(file, `harness source ${name}`)),
  ]);
  const sources = Object.fromEntries(HARNESS_SOURCES.map(([name], index) => [name, {
    sha256: sourceFiles[index].sha256,
    bytes: sourceFiles[index].bytes.length,
  }]));
  const raw = {
    matrix: { sha256: matrixFile.sha256, bytes: matrixFile.bytes.length },
    smoke: { sha256: smokeFile.sha256, bytes: smokeFile.bytes.length },
    corpus: { sha256: corpusFile.sha256, bytes: corpusFile.bytes.length },
    workflowAudit: { sha256: auditFile.sha256, bytes: auditFile.bytes.length },
  };

  const corpus = corpusFile.value;
  const matrix = matrixFile.value;
  const smoke = smokeFile.value;
  const audit = auditFile.value;
  const cases = parseJSON(sourceFiles[HARNESS_SOURCES.findIndex(([name]) => name === "smokeCases")].bytes, "smoke-cases.json");

  validateCorpus(corpus);
  expect(cases.schemaVersion === 1, "smoke-cases schemaVersion must be 1");
  expect(cases.upstreamVersion === UPSTREAM_VERSION, `smoke-cases upstreamVersion must be ${UPSTREAM_VERSION}`);
  validateMatrix(matrix, corpus);
  expect(matrix.inputs.harness?.sha256 === sources.matrix.sha256, "matrix raw result is not bound to the current matrix.mjs");
  expect(matrix.inputs.corpus?.sha256 === corpusFile.sha256, "matrix raw result is not bound to the current corpus.json");
  expect(matrix.inputs.observer?.sha256 === sources.observer.sha256, "matrix raw result is not bound to the current observer.ts");
  expect(matrix.inputs.packageLock?.sha256 === sources.packageLock.sha256, "matrix raw result is not bound to the current package-lock.json");
  validateSmoke(smoke, cases, corpus, {
    corpus: corpusFile.sha256,
    smoke: sources.smoke.sha256,
    smokeCases: sources.smokeCases.sha256,
    packageLock: sources.packageLock.sha256,
  });
  expect(matrix.inputs.packageLock.sha256 === smoke.inputs.packageLockSHA256, "matrix and smoke used different package locks");
  expect(matrix.inputs.upstreamPi.sha256 === smoke.inputs.upstreamPi.sha256, "matrix and smoke used different Pi binaries");
  expect(matrix.inputs.pigo.sha256 === smoke.inputs.pigo.sha256, "matrix and smoke used different Pigo binaries");
  validateAudit(audit, corpus, matrix);
  expect(Date.parse(smoke.generatedAt) >= Date.parse(matrix.generatedAt), "smoke results predate the load matrix");

  const report = buildReport({ matrix, smoke, corpus, audit, raw, sources });
  await atomicWrite(options.output, `${JSON.stringify(report, null, 2)}\n`);
  process.stdout.write(`wrote ${options.output}\n`);
}

main().catch((error) => {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
});
