package tech.ordalie.orb.core

import org.json.*
import org.junit.Assert.*
import org.junit.Test

class TranscriptTest {
    private fun Transcript.feed(vararg events: String) = events.forEach { apply(JSONObject(it)) }

    @Test fun agentEventsBecomeTurnsProseAndToolLines() {
        val t = Transcript()
        t.sent += "hi"
        t.feed(
            """{"type":"message_start","message":{"role":"user","content":"hi"}}""",
            """{"type":"message_end","message":{"role":"user","content":"hi"}}""",
            """{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"Look"}]}}""",
            """{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Looking"},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls -la"}}]}}""",
            """{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls -la"}}""",
            """{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"a\nb"}]},"isError":false}""",
            """{"type":"agent_end"}""",
        )
        assertEquals(3, t.items.size)
        (t.items[0] as You).let { assertEquals("hi", it.text); assertNull(it.via) }
        assertEquals("Looking", (t.items[1] as Said).text)
        (t.items[2] as Tool).let { assertEquals("bash", it.verb); assertEquals("ls -la", it.target); assertEquals("b", it.result); assertFalse(it.live) }
    }

    @Test fun aPromptThisSideDidNotSendCameThroughBridge() {
        val t = Transcript()
        t.feed("""{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"from the Mac"}]}}""")
        assertEquals("bridge", (t.items.single() as You).via)
    }

    @Test fun replayedHistoryIsQuietAndQuestionsReadAsAnswers() {
        val t = Transcript()
        t.load(JSONArray("""[
            {"role":"user","content":"pick"},
            {"role":"assistant","content":[{"type":"toolCall","id":"q","name":"ask_user_question","arguments":{"questions":[{"id":"c","question":"Colour?"}]}}]},
            {"role":"toolResult","toolCallId":"q","content":[{"type":"text","text":"{\"answers\":[{\"id\":\"c\",\"selected\":[\"blue\"]}]}"}]},
            {"role":"assistant","content":[{"type":"toolCall","id":"x","name":"bash","arguments":{"command":"sleep 99"}}]}
        ]"""))
        val tools = t.items.filterIsInstance<Tool>()
        assertEquals("ask", tools[0].verb); assertEquals("Colour?", tools[0].target); assertEquals("blue", tools[0].result)
        assertFalse("an unanswered call from history is not running", tools[1].live)
        assertNull((t.items.first() as You).via)
    }

    @Test fun questionsAreWalkedAndAnsweredWithOneResult() {
        val q = Questions("in1", JSONArray("""[
            {"id":"a","question":"Colour?","options":[{"label":"red"},{"label":"blue"}]},
            {"id":"b","question":"Why?"}
        ]"""))
        assertEquals(listOf("red", "blue"), q.ask().choices)
        assertNull(q.answer("red"))
        assertEquals("Why?", q.ask().title)
        val result = JSONObject(q.answer("because")!!).getJSONArray("answers")
        assertEquals("red", result.getJSONObject(0).getJSONArray("selected").getString(0))
        result.getJSONObject(1).let { assertEquals(0, it.getJSONArray("selected").length()); assertEquals("because", it.getString("custom")) }
        assertTrue(JSONObject(Questions.CANCELLED).getBoolean("cancelled"))
    }

    @Test fun providerErrorsReadAsStatusAndSentence() {
        val raw = "400 [{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Invalid JSON payload received. Unknown name \\\"store\\\": Cannot find field.\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}]"
        assertEquals("400 · Invalid JSON payload received. Unknown name \"store\": Cannot find field.", Transcript.error(raw))
        assertEquals("connection refused", Transcript.error("connection refused"))
    }

    @Test fun compactionRetriesAndMachineTurnsAreNotesNotPeople() {
        val t = Transcript()
        t.feed(
            """{"type":"compaction_start","reason":"manual"}""",
            """{"type":"compaction_end","reason":"manual","result":{"tokensBefore":48000},"aborted":false,"willRetry":false}""",
            """{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"529 overloaded"}""",
            """{"type":"message_start","message":{"role":"user","content":"<task-notification><status>completed</status></task-notification>"}}""",
        )
        val notes = t.items.map { (it as Note).text }
        assertEquals("context compacted · from 48k tokens", notes[1])
        assertTrue(notes[2], notes[2].startsWith("retrying 1 / 3"))
        assertEquals("task notification · completed", notes[3])
    }

    @Test fun providerErrorsWithEscapedNewlinesStillCondense() {
        val raw = "{\\n  \"error\": {\\n    \"code\": 401,\\n    \"message\": \"Request had invalid authentication credentials.\",\\n    \"status\": \"UNAUTHENTICATED\"\\n  }\\n}"
        assertEquals("Request had invalid authentication credentials.", Transcript.error(raw))
    }

    @Test fun retriesCollapseIntoOneLineThatLeavesOnSuccess() {
        val t = Transcript()
        val failed = """{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"network is unreachable"}}"""
        t.feed(failed, """{"type":"auto_retry_start","attempt":1,"maxAttempts":8,"errorMessage":"network is unreachable"}""",
            failed, """{"type":"auto_retry_start","attempt":2,"maxAttempts":8,"errorMessage":"network is unreachable"}""")
        assertEquals(listOf("retrying 2 / 8 · network is unreachable"), t.items.map { (it as Note).text })
        t.feed("""{"type":"auto_retry_end","success":true,"attempt":2}""")
        assertTrue(t.items.isEmpty())
        t.load(JSONArray("""[{"role":"assistant","content":[],"stopReason":"error","errorMessage":"retried"},{"role":"assistant","content":[{"type":"text","text":"ok"}]}]"""))
        assertEquals(1, t.items.size)
    }
}
