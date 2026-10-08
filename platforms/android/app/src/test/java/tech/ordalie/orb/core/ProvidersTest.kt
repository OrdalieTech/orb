package tech.ordalie.orb.core

import org.junit.Assert.*
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

    @Test fun accountListingCarriesEveryWindowOfThePlan() {
        val claude = Account.parse(org.json.JSONObject("""{"active":true,"auth":"oauth","id":"ambient","name":"baudouin","provider":"claude-sessions","provider_name":"Claude","usage":{"plan":"max","windows":[{"name":"5h","remaining":76,"resets_at":"2026-10-08T10:39:59.781434Z"},{"name":"7d","remaining":45,"resets_at":"2026-10-13T04:59:59.781455Z"}],"checked_at":"2026-10-08T11:06:02.744207+02:00"}}"""))
        assertTrue(claude.active)
        assertEquals("max", claude.plan)
        assertEquals(listOf("5h" to 76.0, "7d" to 45.0), claude.windows.map { it.name to it.left })
        val key = Account.parse(org.json.JSONObject("""{"active":true,"auth":"api_key","id":"default","name":"Default","provider":"google","provider_name":"Google"}"""))
        assertTrue(key.windows.isEmpty())
    }
}
