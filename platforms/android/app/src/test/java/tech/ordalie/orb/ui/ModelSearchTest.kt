package tech.ordalie.orb.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ModelSearchTest {

    private val catalog = listOf(
        "anthropic/claude-opus-5-5",
        "anthropic/claude-sonnet-4-6",
        "anthropic/claude-haiku-4-5",
        "openai/gpt-5.4",
        "openai/gpt-5.4-mini",
        "openai-codex/gpt-6.2-sol",
        "google/gemini-4-neon",
        "google/gemini-2.5-pro",
        "deepseek/deepseek-r1",
    )

    @Test
    fun blankQueryReturnsOriginalOrder() {
        assertEquals(catalog, fuzzyFilterModels(catalog, ""))
        assertEquals(catalog, fuzzyFilterModels(catalog, "   "))
    }

    @Test
    fun separatorNormalizedQueriesMatch() {
        assertEquals("anthropic/claude-opus-5-5", fuzzyFilterModels(catalog, "opus 55").first())
        assertEquals("anthropic/claude-opus-5-5", fuzzyFilterModels(catalog, "opus 5.5").first())
        assertEquals("anthropic/claude-opus-5-5", fuzzyFilterModels(catalog, "opus55").first())
    }

    @Test
    fun providerAndVersionTokensMatchInAnyOrder() {
        assertEquals("openai-codex/gpt-6.2-sol", fuzzyFilterModels(catalog, "codex 62").first())
        assertEquals("openai-codex/gpt-6.2-sol", fuzzyFilterModels(catalog, "62 codex").first())
    }

    @Test
    fun subsequenceShorthandsMatch() {
        assertEquals("openai-codex/gpt-6.2-sol", fuzzyFilterModels(catalog, "g62s").first())
        assertEquals("google/gemini-4-neon", fuzzyFilterModels(catalog, "gem 4 n").first())
        assertEquals("anthropic/claude-sonnet-4-6", fuzzyFilterModels(catalog, "cs46").first())
    }

    @Test
    fun providerAliasesExpand() {
        assertEquals("openai/gpt-5.4-mini", fuzzyFilterModels(catalog, "oai 54 mini").first())
        assertEquals("deepseek/deepseek-r1", fuzzyFilterModels(catalog, "ds r1").first())
        assertTrue(fuzzyFilterModels(catalog, "anth haiku").first() == "anthropic/claude-haiku-4-5")
    }

    @Test
    fun exactAndPrefixBeatScatteredSubsequence() {
        val models = listOf(
            "vendor/x-g-p-t-5-4-experimental",
            "openai/gpt-5.4-mini",
            "openai/gpt-5.4",
        )
        val ranked = fuzzyFilterModels(models, "gpt-5.4")
        assertEquals("openai/gpt-5.4", ranked[0])
        assertEquals("openai/gpt-5.4-mini", ranked[1])
        assertEquals("vendor/x-g-p-t-5-4-experimental", ranked[2])
    }

    @Test
    fun noMatchReturnsEmptyList() {
        assertTrue(fuzzyFilterModels(catalog, "zzzzzz-nonexistent").isEmpty())
    }
}
