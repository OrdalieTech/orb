package tech.ordalie.orb.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ProvidersTest {
    @Test fun loginListingGroupsMethodsAndSaysWhereTheCredentialLives() {
        val providers = Provider.parse("""
            {"auth":"oauth","id":"anthropic","label":"Sign in with an account","login":true,"method":"Anthropic (Claude Pro/Max)","models":0,"name":"Anthropic"}
            {"auth":"api_key","id":"anthropic","label":"Sign in with an API key","login":true,"method":"Anthropic API key","models":0,"name":"Anthropic"}
            {"auth":"api_key","id":"google","label":"Sign in with an API key","login":true,"method":"Gemini API key","models":26,"name":"Google","status":{"source":"GEMINI_API_KEY","type":"api_key"}}
            {"auth":"oauth","id":"openai-codex","label":"Sign in with an account","login":true,"method":"OpenAI (ChatGPT Plus/Pro)","models":9,"name":"OpenAI Codex","status":{"source":"stored","type":"oauth"}}
            not json
        """.trimIndent())
        assertEquals(listOf("anthropic", "google", "openai-codex"), providers.map { it.id })
        providers[0].let { assertFalse(it.ready); assertEquals(listOf(true, false), it.methods.map(Method::account)) }
        providers[1].let { assertTrue(it.ready); assertEquals(26, it.models); assertEquals("key in this app", it.holds) }
        assertEquals("account", providers[2].holds)
    }
}
