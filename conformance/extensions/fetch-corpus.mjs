#!/usr/bin/env node

// Rebuilds corpus.json and package.json from the public Pi package gallery.
//
// The gallery at https://pi.dev/packages has no JSON API (/api/* answers 501
// "API routes are reserved for future features"), but every card carries
// machine-readable data-package-* attributes, so the paginated HTML is the
// enumeration source. Package identity, version, integrity and the
// pi.extensions manifest field come from the npm registry; download counts
// come from the npm downloads API so monthly and weekly are a consistent pair.
//
// This script only fetches metadata. It never installs, extracts or executes a
// corpus package. Regenerating package-lock.json is a separate, containerised
// step (see README of this directory and the report accompanying this file).

import { createHash } from "node:crypto";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const GALLERY = "https://pi.dev/packages";
const REGISTRY = "https://registry.npmjs.org";
const DOWNLOADS = "https://api.npmjs.org/downloads/point";
const PI_PACKAGE = "@earendil-works/pi-coding-agent";
const PI_VERSION = "0.81.1";
const USER_AGENT = "orb-conformance-corpus/1 (+https://github.com/OrdalieTech/orb)";
const TIER1 = 50;
const TIER2 = 50;

function usage() {
	return `Usage: node fetch-corpus.mjs --cache <dir> [options]
       node fetch-corpus.mjs --verify-paths <node_modules> --corpus <corpus.json>

  --cache <dir>       Directory for raw gallery/registry responses (required)
  --keep <n>          Maximum packages to pin in the corpus (default 300)
  --resolve <n>       Candidates to resolve, most downloaded first (default 1200, 0 = all)
  --rank-pool <n>     Keepers given fresh npm download counts before the top --keep
                      cut is made (default 900)
  --pages <n>         Gallery pages to enumerate (default: all)
  --concurrency <n>   Parallel metadata requests (default 6)
  --rate <n>          Registry requests per second (default 4; npm returns 429 well above this)
  --write             Overwrite corpus.json and package.json in this directory
  --report <file>     Write the full selection audit as JSON

  --verify-paths <d>  Resolve every declared pi.extensions path against an installed
                      tree and rewrite --corpus in place with manifestFlags. Intended
                      to run inside the same container that produced the tree; it
                      only stats paths and never imports package code.

Fetches metadata only. No package is installed, extracted or executed.`;
}

function parseArgs(argv) {
	const options = {
		cache: "",
		keep: 300,
		resolve: 1200,
		"rank-pool": 900,
		pages: 0,
		concurrency: 6,
		rate: 4,
		write: false,
		report: "",
		"verify-paths": "",
		corpus: "",
	};
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if (argument === "--write") {
			options.write = true;
			continue;
		}
		if (index + 1 >= argv.length) throw new Error(`incomplete argument: ${argument}`);
		const value = argv[++index];
		switch (argument) {
			case "--cache":
			case "--report":
			case "--verify-paths":
			case "--corpus":
				options[argument.slice(2)] = path.resolve(value);
				break;
			case "--keep":
			case "--resolve":
			case "--rank-pool":
			case "--pages":
			case "--concurrency":
			case "--rate":
				options[argument.slice(2)] = Number.parseInt(value, 10);
				break;
			default:
				throw new Error(`unknown argument: ${argument}`);
		}
	}
	if (options["verify-paths"]) {
		if (!options.corpus) throw new Error("--verify-paths requires --corpus");
		return options;
	}
	if (!options.cache) throw new Error("--cache is required");
	return options;
}

function cacheName(kind, key) {
	const safe = key.replace(/[^A-Za-z0-9._-]/g, "_");
	if (safe.length <= 120) return `${kind}-${safe}`;
	return `${kind}-${safe.slice(0, 60)}-${createHash("sha256").update(key).digest("hex").slice(0, 16)}`;
}

// npm answers 429 for short bursts well below its documented ceiling, so every
// uncached request passes through one global minimum-interval gate.
const limiter = { interval: 250, next: 0 };

async function throttle() {
	const now = Date.now();
	const at = Math.max(now, limiter.next);
	limiter.next = at + limiter.interval;
	if (at > now) await new Promise((resolve) => setTimeout(resolve, at - now));
}

async function cachedFetch(cache, kind, key, url, { json = true } = {}) {
	const file = path.join(cache, kind, cacheName(kind, key));
	try {
		const cached = await readFile(file, "utf8");
		return json ? JSON.parse(cached) : cached;
	} catch {
		/* not cached yet */
	}
	let lastError;
	for (let attempt = 0; attempt < 16; attempt++) {
		try {
			await throttle();
			const response = await fetch(url, { headers: { "user-agent": USER_AGENT, accept: json ? "application/json" : "text/html" } });
			if (response.status === 429 || response.status >= 500) {
				const retryAfter = Number.parseInt(response.headers.get("retry-after") ?? "", 10);
				const wait = Number.isFinite(retryAfter) ? (retryAfter + 5) * 1000 : Math.min(120000, 1000 * 2 ** attempt);
				await response.arrayBuffer();
				// Back the global gate off as well, not just this one request.
				limiter.next = Math.max(limiter.next, Date.now() + wait);
				await new Promise((resolve) => setTimeout(resolve, wait));
				lastError = new Error(`HTTP ${response.status}`);
				continue;
			}
			if (response.status === 404 || response.status === 405) {
				const body = JSON.stringify({ __httpStatus: response.status });
				await mkdir(path.dirname(file), { recursive: true });
				await writeFile(file, body);
				return JSON.parse(body);
			}
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			const body = await response.text();
			const parsed = json ? JSON.parse(body) : body;
			await mkdir(path.dirname(file), { recursive: true });
			await writeFile(file, body);
			return parsed;
		} catch (error) {
			lastError = error;
			await new Promise((resolve) => setTimeout(resolve, 400 * 2 ** attempt));
		}
	}
	throw new Error(`fetch failed for ${url}: ${lastError?.message}`);
}

async function pool(items, concurrency, worker) {
	const results = new Array(items.length);
	let next = 0;
	const runners = Array.from({ length: Math.max(1, Math.min(concurrency, items.length)) }, async () => {
		for (;;) {
			const index = next++;
			if (index >= items.length) return;
			results[index] = await worker(items[index], index);
		}
	});
	await Promise.all(runners);
	return results;
}

const CARD = /<article\b[^>]*data-package-card="true"[^>]*>/g;

function attribute(tag, name) {
	const match = tag.match(new RegExp(`${name}="([^"]*)"`));
	if (!match) return "";
	return match[1].replace(/&amp;/g, "&").replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">");
}

function parseGalleryPage(html) {
	const cards = [];
	for (const match of html.matchAll(CARD)) {
		const tag = match[0];
		cards.push({
			package: attribute(tag, "data-package-name"),
			types: attribute(tag, "data-package-types").split(/[\s,]+/).filter(Boolean),
			galleryDownloads: Number.parseInt(attribute(tag, "data-package-downloads") || "0", 10),
			publishedAt: Number.parseInt(attribute(tag, "data-package-date") || "0", 10),
		});
	}
	const pages = [...html.matchAll(/\/packages\?[^"]*page=(\d+)/g)].map((match) => Number.parseInt(match[1], 10));
	return { cards, lastPage: pages.length ? Math.max(...pages) : 1 };
}

async function census(options) {
	const first = await cachedFetch(options.cache, "gallery", "page-1", `${GALLERY}?page=1`, { json: false });
	const head = parseGalleryPage(first);
	const lastPage = options.pages > 0 ? Math.min(options.pages, head.lastPage) : head.lastPage;
	const numbers = Array.from({ length: lastPage - 1 }, (_, index) => index + 2);
	const rest = await pool(numbers, options.concurrency, async (page) => {
		const html = await cachedFetch(options.cache, "gallery", `page-${page}`, `${GALLERY}?page=${page}`, { json: false });
		return parseGalleryPage(html).cards;
	});
	const seen = new Map();
	for (const card of [head.cards, ...rest].flat()) {
		if (!card.package) continue;
		if (!seen.has(card.package)) seen.set(card.package, card);
	}
	return { lastPage, cards: [...seen.values()] };
}

function classify(manifest) {
	if (!manifest || manifest.__httpStatus) return { keep: false, reason: `registry_${manifest?.__httpStatus ?? "error"}` };
	const pi = manifest.pi;
	if (pi === undefined || pi === null) return { keep: false, reason: "no_pi_field" };
	if (typeof pi !== "object" || Array.isArray(pi)) return { keep: false, reason: "pi_field_not_object" };
	const declared = pi.extensions;
	if (declared === undefined || declared === null) return { keep: false, reason: "no_pi_extensions" };
	if (typeof declared === "string") return { keep: false, reason: "pi_extensions_string" };
	if (!Array.isArray(declared)) return { keep: false, reason: "pi_extensions_not_array" };
	if (declared.length === 0) return { keep: false, reason: "pi_extensions_empty_array" };
	if (!declared.every((entry) => typeof entry === "string" && entry.length > 0)) {
		return { keep: false, reason: "pi_extensions_non_string_entry" };
	}
	return { keep: true, extensions: declared };
}

function anomalies(entries) {
	const flags = [];
	for (const entry of entries) {
		if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(entry) || entry.startsWith("//")) flags.push(`non_path_specifier:${entry}`);
		else if (path.posix.isAbsolute(entry)) flags.push(`absolute_path:${entry}`);
		else if (path.posix.normalize(entry).startsWith("..")) flags.push(`escapes_package_root:${entry}`);
	}
	return flags;
}

// The downloads API accepts up to 128 comma-separated unscoped names per
// request but rejects bulk queries containing a scoped name, so unscoped
// packages are batched and scoped packages are asked for one at a time.
const BULK_LIMIT = 100;

async function downloadPoint(options, period, entries) {
	const kind = `downloads-${period}`;
	const scoped = entries.filter((entry) => entry.package.startsWith("@"));
	const plain = entries.filter((entry) => !entry.package.startsWith("@"));
	const counts = new Map();

	for (let index = 0; index < plain.length; index += BULK_LIMIT) {
		const batch = plain.slice(index, index + BULK_LIMIT);
		const names = batch.map((entry) => entry.package);
		const body = await cachedFetch(options.cache, `${kind}-bulk`, names.join(","), `${DOWNLOADS}/${period}/${names.join(",")}`);
		// A one-name bulk query answers with the single-package shape instead.
		if (names.length === 1) counts.set(names[0], body?.downloads ?? null);
		else for (const name of names) counts.set(name, body?.[name]?.downloads ?? null);
	}
	await pool(scoped, options.concurrency, async (entry) => {
		const body = await cachedFetch(options.cache, kind, entry.package, `${DOWNLOADS}/${period}/${entry.package.replace("/", "%2F")}`);
		counts.set(entry.package, body?.downloads ?? null);
	});
	return counts;
}

async function attachMonthly(options, entries) {
	const monthly = await downloadPoint(options, "last-month", entries);
	for (const entry of entries) entry.monthly = monthly.get(entry.package) ?? entry.galleryDownloads;
}

async function attachWeekly(options, entries) {
	const weekly = await downloadPoint(options, "last-week", entries);
	for (const entry of entries) entry.downloads = { monthly: entry.monthly, weekly: weekly.get(entry.package) ?? 0 };
}

const SUFFIXES = ["", ".ts", ".tsx", ".js", ".mjs", ".cjs", "/index.ts", "/index.js", "/index.mjs", "/index.cjs"];

async function resolveDeclaredPath(root, entry) {
	const target = path.resolve(root, entry);
	if (target !== root && !target.startsWith(root + path.sep)) return { outside: true };
	for (const suffix of SUFFIXES) {
		try {
			const found = await stat(target + suffix);
			return { suffix, directory: found.isDirectory() };
		} catch {
			/* try the next candidate */
		}
	}
	return { missing: true };
}

async function verifyPaths(options) {
	const corpus = JSON.parse(await readFile(options.corpus, "utf8"));
	let flagged = 0;
	for (const record of corpus.extensions) {
		const root = path.resolve(options["verify-paths"], record.package);
		const flags = (record.manifestFlags ?? []).filter((flag) => !flag.startsWith("unresolved_path:") && !flag.startsWith("resolved_via_suffix:"));
		let installed = true;
		try {
			await stat(root);
		} catch {
			installed = false;
		}
		if (!installed) {
			flags.push("not_installed");
		} else {
			for (const entry of record.extensions) {
				const outcome = await resolveDeclaredPath(root, entry);
				if (outcome.outside) flags.push(`escapes_package_root:${entry}`);
				else if (outcome.missing) flags.push(`unresolved_path:${entry}`);
				else if (outcome.suffix) flags.push(`resolved_via_suffix:${entry}${outcome.suffix}`);
			}
		}
		const unique = [...new Set(flags)];
		if (unique.length) {
			record.manifestFlags = unique;
			flagged++;
		} else {
			delete record.manifestFlags;
		}
	}
	await writeFile(options.corpus, JSON.stringify(corpus, null, "\t") + "\n");
	process.stderr.write(`verify-paths: ${flagged} of ${corpus.extensions.length} entries carry manifestFlags\n`);
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(usage() + "\n");
		return;
	}
	if (options["verify-paths"]) {
		await verifyPaths(options);
		return;
	}
	await mkdir(options.cache, { recursive: true });
	if (options.rate > 0) limiter.interval = Math.ceil(1000 / options.rate);

	const gallery = await census(options);
	process.stderr.write(`gallery: ${gallery.cards.length} unique packages over ${gallery.lastPage} pages\n`);

	const ranked = [...gallery.cards].sort((left, right) => right.galleryDownloads - left.galleryDownloads || left.package.localeCompare(right.package));
	const ordered = options.resolve > 0 ? ranked.slice(0, options.resolve) : ranked;
	process.stderr.write(`resolving ${ordered.length} of ${ranked.length} candidates, most downloaded first\n`);
	let done = 0;
	const manifests = await pool(ordered, options.concurrency, async (card) => {
		const manifest = await cachedFetch(options.cache, "registry", card.package, `${REGISTRY}/${card.package.replace("/", "%2F")}/latest`);
		if (++done % 100 === 0) process.stderr.write(`  resolved ${done}/${ordered.length}\n`);
		return manifest;
	});

	const dropped = [];
	const kept = [];
	for (const [index, card] of ordered.entries()) {
		const manifest = manifests[index];
		const verdict = classify(manifest);
		if (!verdict.keep) {
			dropped.push({ package: card.package, reason: verdict.reason, types: card.types, galleryDownloads: card.galleryDownloads });
			continue;
		}
		kept.push({
			package: card.package,
			version: manifest.version,
			integrity: manifest.dist?.integrity ?? "",
			extensions: verdict.extensions,
			types: card.types,
			galleryDownloads: card.galleryDownloads,
			flags: anomalies(verdict.extensions),
		});
	}
	process.stderr.write(`selection: ${kept.length} keepers, ${dropped.length} dropped\n`);

	// Gallery counts are a cached snapshot, so they only choose the pool. The
	// cut and the ordering are made on freshly fetched npm counts, otherwise a
	// package whose traffic collapsed since the snapshot keeps a pinned slot.
	const poolSize = Math.max(options.keep, options["rank-pool"]);
	const ranking = kept.filter((entry) => entry.integrity).slice(0, poolSize);
	process.stderr.write(`downloads: refreshing ${ranking.length} monthly counts to rank the top ${options.keep}\n`);
	await attachMonthly(options, ranking);

	ranking.sort((left, right) => right.monthly - left.monthly || left.package.localeCompare(right.package));
	const pinned = ranking.slice(0, options.keep);
	await attachWeekly(options, pinned);

	const extensions = pinned.map((entry, index) => {
		const record = {
			rank: index + 1,
			tier: index < TIER1 ? 1 : index < TIER1 + TIER2 ? 2 : 3,
			package: entry.package,
			version: entry.version,
			downloads: entry.downloads,
			integrity: entry.integrity,
			extensions: entry.extensions,
		};
		if (entry.flags.length) record.manifestFlags = entry.flags;
		return record;
	});

	const dropCounts = {};
	for (const entry of dropped) dropCounts[entry.reason] = (dropCounts[entry.reason] ?? 0) + 1;

	const corpus = {
		schemaVersion: 1,
		capturedAt: new Date().toISOString().slice(0, 10),
		selection: {
			source: "https://pi.dev/packages (paginated HTML; data-package-* card attributes) plus https://registry.npmjs.org/<name>/latest",
			rule: `Gallery packages whose published pi.extensions manifest field is a non-empty array of strings; the ${ranking.length} most downloaded of them were re-measured against the npm downloads API and the top ${options.keep} by last-month downloads are pinned`,
			caveat:
				"npm downloads measure registry traffic, not unique Pi users; malformed string-valued pi.extensions manifests are not loadable by upstream and are excluded; manifestFlags marks kept entries whose declared paths are absolute, escape the package root, or are not plain relative paths",
			tiers: {
				"1": `top ${TIER1} by monthly downloads`,
				"2": `next ${TIER2} by monthly downloads`,
				"3": "remaining pinned packages",
			},
			census: {
				galleryPages: gallery.lastPage,
				galleryPackages: gallery.cards.length,
				galleryExtensionTyped: gallery.cards.filter((card) => card.types.includes("extension")).length,
				candidatesResolved: ordered.length,
				kept: kept.length,
				downloadsRefreshed: ranking.length,
				pinned: extensions.length,
				dropped: dropped.length,
				dropReasons: dropCounts,
			},
		},
		extensions,
	};

	const manifest = {
		name: "orb-extension-matrix",
		version: "0.0.0",
		private: true,
		description: "Pinned dependency tree for the Orb ecosystem extension matrix",
		dependencies: Object.fromEntries(
			[[PI_PACKAGE, PI_VERSION], ...extensions.map((entry) => [entry.package, entry.version])].sort(([left], [right]) =>
				left.localeCompare(right),
			),
		),
	};

	if (options.report) {
		await writeFile(options.report, JSON.stringify({ census: corpus.selection.census, dropped, kept }, null, "\t") + "\n");
	}
	if (options.write) {
		await writeFile(path.join(HERE, "corpus.json"), JSON.stringify(corpus, null, "\t") + "\n");
		await writeFile(path.join(HERE, "package.json"), JSON.stringify(manifest, null, "\t") + "\n");
	} else {
		process.stdout.write(JSON.stringify(corpus, null, "\t") + "\n");
	}
}

main().catch((error) => {
	process.stderr.write(`fetch-corpus: ${error.message}\n`);
	process.exitCode = 1;
});
