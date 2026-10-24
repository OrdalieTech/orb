package tech.ordalie.orb.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import android.content.ComponentName
import android.content.Intent
import android.content.IntentFilter
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.nio.file.Files
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowEnvironment
import org.robolectric.shadows.ShadowLooper
import org.robolectric.annotation.Config
import tech.ordalie.orb.OrbApp

/**
 * A conversation opens at its latest turn, follows what streams in while the reader is at the
 * bottom, and holds still once they scroll up, until they come back down. The real screen and
 * the real view client, fed by a stand-in for `orb app` that says what this test writes.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35]) // Robolectric runs 36 on Java 21 only; the build uses 17
class ChatTest {
    @get:Rule val compose = createEmptyComposeRule()

    private val dir = Files.createTempDirectory("orb-chat").toFile()
    private val feed = File(dir, "feed").apply { writeText("") }
    private var turns = 0

    private fun say(o: JSONObject) = feed.appendText("$o\n")
    private fun answered(n: Int) {
        val rows = JSONArray()
        repeat(n) {
            rows.put(JSONObject().put("k", "$turns.0").put("kind", "you").put("text", "question $turns"))
            rows.put(JSONObject().put("k", "$turns.1").put("kind", "md").put("block", JSONObject().put("type", "p")
                .put("spans", JSONArray().put(JSONObject().put("t", "answer $turns\n" + "a line of it\n".repeat(3))))))
            turns++
        }
        say(JSONObject().put("t", "rows").put("tab", "t").put("at", (turns - n) * 2).put("rows", rows))
    }
    /** Whether answer [n] is on screen, after the view's rows arrived and the screen settled. */
    private fun shown(n: Int): Boolean {
        arrived()
        compose.waitForIdle()
        return runCatching { compose.onNodeWithText("answer $n", substring = true).assertIsDisplayed() }.isSuccess
    }
    /** Waits for the view to hold every row said so far; what it hears lands on the main looper. */
    private fun arrived() {
        val until = System.currentTimeMillis() + 10_000
        while (rt.view.rows["t"]?.size != turns * 2) {
            check(System.currentTimeMillis() < until) { "the view never got the rows" }
            ShadowLooper.idleMainLooper()
            Thread.sleep(20)
        }
    }
    private val app get() = ApplicationProvider.getApplicationContext<OrbApp>()
    private val rt get() = app.runtime

    @Test fun opensAtTheLatestTurnAndFollowsOnlyAReaderAtTheBottom() {
        File(dir, "liborb.so").apply { writeText("#!/bin/sh\nexec /usr/bin/tail -f '${feed.path}'\n"); setExecutable(true) }
        app.applicationInfo.nativeLibraryDir = dir.path
        ShadowEnvironment.addExternalDir(File(dir, "storage").path) // the phone's storage, which the Linux asks about
        say(JSONObject().put("t", "state").put("self", "me").put("tabs", JSONArray().put(JSONObject().put("id", "t").put("peer", "me").put("online", true).put("loaded", true))))
        answered(30)
        arrived()
        // A bare activity to draw in, which the app's manifest does not declare.
        val bare = ComponentName(app, ComponentActivity::class.java)
        shadowOf(app.packageManager).apply { addActivityIfNotPresent(bare); addIntentFilterForActivity(bare, IntentFilter(Intent.ACTION_MAIN).apply { addCategory(Intent.CATEGORY_LAUNCHER) }) }
        ActivityScenario.launch(ComponentActivity::class.java).onActivity {
            it.setContent {
                val scope = rememberCoroutineScope()
                val nav = remember { Nav(rt, scope) }
                val c = remember { Ctx(rt, nav, mutableStateListOf(), {}, app, {}, {}, {}) { _, _ -> } }
                OrbTheme { Column { rt.view.tab("t")?.let { Chat(c, it) } } }
            }
        }
        assertTrue(shown(29))
        assertFalse(shown(0))
        answered(1)
        assertTrue(shown(30))

        // Scrolled up, the reader stays where they read while answers keep coming.
        compose.onNode(hasScrollToIndexAction()).performTouchInput { swipeDown() }
        assertFalse(shown(30))
        answered(1)
        assertFalse(shown(31))

        // Back at the bottom, it follows again.
        compose.onNode(hasScrollToIndexAction()).performTouchInput { repeat(12) { swipeUp() } }
        assertTrue(shown(31))
        answered(1)
        assertTrue(shown(32))
        dir.deleteRecursively()
    }
}
