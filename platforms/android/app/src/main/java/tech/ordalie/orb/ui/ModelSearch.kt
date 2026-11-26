package tech.ordalie.orb.ui

private val MODEL_SEARCH_ALIASES = mapOf(
    "oai" to listOf("openai"),
    "anth" to listOf("anthropic"),
    "gem" to listOf("gemini"),
    "ds" to listOf("deepseek"),
)

private fun isSeparator(c: Char): Boolean = c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c.isWhitespace()

private fun normalizeModelText(text: String): String = buildString(text.length) {
    for (c in text) {
        if (!isSeparator(c)) append(c.lowercaseChar())
    }
}

private fun isWordBoundary(text: String, index: Int): Boolean =
    index == 0 || isSeparator(text[index - 1])

/**
 * Computes an ordered-subsequence score for [needle] inside [haystack], or null if not a subsequence.
 * Prefers word-boundary hits, consecutive runs, and earlier matches.
 */
private fun subsequenceScore(needle: String, haystack: String, baseScore: Int): Int? {
    if (needle.isEmpty()) return baseScore
    var ni = 0
    var score = baseScore
    var lastMatch = -2
    var firstMatch = -1
    for (hi in haystack.indices) {
        if (haystack[hi] == needle[ni]) {
            if (firstMatch < 0) firstMatch = hi
            if (hi == lastMatch + 1) score += 22
            if (isWordBoundary(haystack, hi)) score += 30
            lastMatch = hi
            ni++
            if (ni == needle.length) break
        }
    }
    if (ni < needle.length) return null
    val span = (lastMatch - firstMatch + 1).coerceAtLeast(needle.length)
    val gapPenalty = (span - needle.length) * 3
    val startPenalty = firstMatch.coerceAtMost(20)
    return (score - gapPenalty - startPenalty).coerceAtLeast(1)
}

private fun scoreTokenVariant(
    token: String,
    shortLower: String,
    shortNorm: String,
    fullLower: String,
    fullNorm: String,
): Int? {
    val normToken = normalizeModelText(token)
    if (normToken.isEmpty()) return null

    if (shortLower == token || shortNorm == normToken) return 1000
    if (shortLower.startsWith(token)) return 760
    if (shortNorm.startsWith(normToken)) return 720

    val shortIdx = shortLower.indexOf(token)
    if (shortIdx >= 0) {
        val boundaryBonus = if (isWordBoundary(shortLower, shortIdx)) 120 else 0
        return 460 + boundaryBonus - shortIdx.coerceAtMost(30)
    }

    val shortNormIdx = shortNorm.indexOf(normToken)
    if (shortNormIdx >= 0) {
        return 380 - shortNormIdx.coerceAtMost(30)
    }

    val fullIdx = fullLower.indexOf(token)
    if (fullIdx >= 0) {
        val boundaryBonus = if (isWordBoundary(fullLower, fullIdx)) 80 else 0
        return 260 + boundaryBonus - fullIdx.coerceAtMost(30)
    }

    val fullNormIdx = fullNorm.indexOf(normToken)
    if (fullNormIdx >= 0) {
        return 210 - fullNormIdx.coerceAtMost(30)
    }

    subsequenceScore(token, shortLower, 150)?.let { return it }
    subsequenceScore(normToken, shortNorm, 120)?.let { return it }
    subsequenceScore(token, fullLower, 90)?.let { return it }
    return subsequenceScore(normToken, fullNorm, 60)
}

/**
 * Ranked fuzzy filter for LLM model IDs (`provider/model`).
 *
 * Splits [query] on whitespace with AND semantics (every token must match, in any order),
 * matching both raw `provider/model` + optional provider display name and a separator-normalized
 * form, with shorthand aliases (`oai`, `anth`, `gem`, `ds`).
 */
fun fuzzyFilterModels(
    models: List<String>,
    query: String,
    providerNames: Map<String, String> = emptyMap(),
): List<String> {
    val trimmed = query.trim().lowercase()
    if (trimmed.isEmpty()) return models
    val tokens = trimmed.split(Regex("\\s+")).filter { it.isNotEmpty() }
    if (tokens.isEmpty()) return models
    val fullQueryNorm = normalizeModelText(trimmed)

    val scored = ArrayList<Pair<String, Int>>(models.size)
    for (id in models) {
        val provider = id.substringBefore('/', "")
        val short = id.substringAfter('/', id)
        val providerName = providerNames[provider].orEmpty()
        val shortLower = short.lowercase()
        val shortNorm = normalizeModelText(shortLower)
        val fullLower = if (providerName.isNotEmpty()) "$id $providerName".lowercase() else id.lowercase()
        val fullNorm = normalizeModelText(fullLower)

        var total = 0
        var matchedAll = true
        for (tok in tokens) {
            var best: Int? = scoreTokenVariant(tok, shortLower, shortNorm, fullLower, fullNorm)
            MODEL_SEARCH_ALIASES[tok]?.forEach { alias ->
                val aliasScore = scoreTokenVariant(alias, shortLower, shortNorm, fullLower, fullNorm)
                if (aliasScore != null && (best == null || aliasScore > best)) {
                    best = aliasScore
                }
            }
            if (best == null) {
                matchedAll = false
                break
            }
            total += best
        }
        if (!matchedAll) continue

        // Bonus when the full query itself is an exact or prefix match on the short model name.
        if (shortLower == trimmed || shortNorm == fullQueryNorm) total += 500
        else if (shortLower.startsWith(trimmed) || shortNorm.startsWith(fullQueryNorm)) total += 200

        scored += id to total
    }

    return scored
        .sortedWith(compareByDescending<Pair<String, Int>> { it.second }.thenBy { it.first.length }.thenBy { it.first })
        .map { it.first }
}
