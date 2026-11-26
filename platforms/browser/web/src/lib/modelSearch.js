const ALIASES = {
  oai: "openai",
  anth: "anthropic",
  gem: "gemini",
  ds: "deepseek",
};

const SEP_RE = /[-_./:\s]/;
const SEP_GLOBAL_RE = /[-_./:\s]+/g;

function stripSep(str) {
  return str.replace(SEP_GLOBAL_RE, "");
}

function isBoundary(str, idx) {
  return idx === 0 || SEP_RE.test(str[idx - 1]);
}

function scoreSubsequence(needle, haystack, baseScore) {
  if (!needle) return 0;
  let ni = 0;
  let firstIdx = -1;
  let lastIdx = -1;
  let prevMatch = -2;
  let boundaryHits = 0;
  let consecutive = 0;

  for (let hi = 0; hi < haystack.length && ni < needle.length; hi++) {
    if (haystack[hi] === needle[ni]) {
      if (ni === 0 && !isBoundary(haystack, hi)) continue;
      if (firstIdx < 0) firstIdx = hi;
      lastIdx = hi;
      if (hi === prevMatch + 1) consecutive++;
      if (isBoundary(haystack, hi)) boundaryHits++;
      prevMatch = hi;
      ni++;
    }
  }

  if (ni < needle.length) return 0;

  const span = lastIdx - firstIdx + 1;
  const tightness = Math.max(0, 40 - (span - needle.length));
  return baseScore + boundaryHits * 25 + consecutive * 15 + tightness;
}

function scoreRawToken(tok, shortLower, shortCompact, idLower, hayLower, hayCompact) {
  if (!tok) return 0;
  const tokCompact = stripSep(tok);
  if (!tokCompact) return 0;

  // 1. Exact short-name match
  if (shortLower === tok || shortCompact === tokCompact) return 1000;

  // 2. Prefix match on shortName
  if (shortLower.startsWith(tok)) return 850;
  if (shortCompact.startsWith(tokCompact)) return 820;

  // 3. Prefix match on full id or haystack
  if (idLower.startsWith(tok)) return 750;
  if (hayCompact.startsWith(tokCompact)) return 720;

  // 4. Word-boundary substring match in shortName
  let idx = shortLower.indexOf(tok);
  while (idx >= 0) {
    if (isBoundary(shortLower, idx)) return 680 - Math.min(idx, 60);
    idx = shortLower.indexOf(tok, idx + 1);
  }

  // 5. Word-boundary substring match in full haystack
  idx = hayLower.indexOf(tok);
  while (idx >= 0) {
    if (isBoundary(hayLower, idx)) return 600 - Math.min(idx, 60);
    idx = hayLower.indexOf(tok, idx + 1);
  }

  // 6. Contiguous substring match (raw then separator-normalized)
  idx = shortLower.indexOf(tok);
  if (idx >= 0) return 520 - Math.min(idx, 60);

  idx = shortCompact.indexOf(tokCompact);
  if (idx >= 0) return 480 - Math.min(idx, 60);

  idx = hayLower.indexOf(tok);
  if (idx >= 0) return 430 - Math.min(idx, 60);

  idx = hayCompact.indexOf(tokCompact);
  if (idx >= 0) return 390 - Math.min(idx, 60);

  // 7. Ordered subsequence match (prefer shortName, then full haystack)
  const subShort = scoreSubsequence(tokCompact, shortLower, 160);
  if (subShort > 0) return subShort;

  const subHay = scoreSubsequence(tokCompact, hayLower, 100);
  if (subHay > 0) return subHay;

  return 0;
}

/**
 * Ranked fuzzy filter for LLM model IDs (e.g. "anthropic/claude-sonnet-4-6").
 *
 * @param {string[]} models
 * @param {string} query
 * @param {(providerId: string) => string} [getProviderName]
 * @returns {string[]}
 */
export function fuzzyFilterModels(models, query, getProviderName = () => "") {
  const list = models || [];
  const trimmed = (query || "").trim().toLowerCase();
  if (!trimmed) return [...list];

  const tokens = trimmed.split(/\s+/).filter(Boolean);
  const fullCompact = stripSep(trimmed);
  const scored = [];

  for (const id of list) {
    const slash = id.indexOf("/");
    const provider = slash >= 0 ? id.slice(0, slash) : "";
    const shortName = slash >= 0 ? id.slice(slash + 1) : id;
    const providerName = (getProviderName ? getProviderName(provider) : "") || "";

    const idLower = id.toLowerCase();
    const shortLower = shortName.toLowerCase();
    const hayLower = providerName ? `${idLower} ${providerName.toLowerCase()}` : idLower;
    const shortCompact = stripSep(shortLower);
    const hayCompact = stripSep(hayLower);

    let total = 0;
    let matchedAll = true;

    for (const tok of tokens) {
      let best = scoreRawToken(tok, shortLower, shortCompact, idLower, hayLower, hayCompact);
      const alias = ALIASES[tok];
      if (alias) {
        const aliasScore = scoreRawToken(alias, shortLower, shortCompact, idLower, hayLower, hayCompact);
        if (aliasScore > best) best = aliasScore;
      }
      if (best <= 0) {
        matchedAll = false;
        break;
      }
      total += best;
    }

    if (!matchedAll) continue;

    // Bonus for full-query exact or prefix match on shortName
    if (shortLower === trimmed || shortCompact === fullCompact) total += 500;
    else if (shortLower.startsWith(trimmed) || shortCompact.startsWith(fullCompact)) total += 200;

    scored.push({ id, score: total });
  }

  scored.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    if (a.id.length !== b.id.length) return a.id.length - b.id.length;
    return a.id.localeCompare(b.id);
  });

  return scored.map(s => s.id);
}
