package tech.ordalie.orb.ui

import android.content.ComponentName
import android.content.Intent
import android.content.IntentFilter
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.nio.file.Files
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowEnvironment
import org.robolectric.shadows.ShadowLooper
import tech.ordalie.orb.OrbApp

/**
 * The overview shows every open conversation as a live card: its title, its state (red while it
 * asks), and the end of its transcript as it streams. The real screen and view client, fed by a
 * stand-in for `orb app`; set ORB_OVERVIEW_PNG to keep a picture of it.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "w411dp-h891dp-xxhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class OverviewTest {
    @get:Rule val compose = createEmptyComposeRule()

    private val dir = Files.createTempDirectory("orb-overview").toFile()
    private val feed = File(dir, "feed").apply { writeText("") }
    private val app get() = ApplicationProvider.getApplicationContext<OrbApp>()
    private val rt get() = app.runtime

    private fun say(o: JSONObject) = feed.appendText("$o\n")
    private fun tab(id: String, title: String, extra: JSONObject.() -> Unit = {}) =
        JSONObject().put("id", id).put("title", title).put("peer", "me").put("online", true).put("loaded", true).put("model", "anthropic/claude-opus-5-5").apply(extra)
    private fun md(text: String) = JSONObject().put("type", "p").put("spans", JSONArray().put(JSONObject().put("t", text)))

    @Test fun showsEveryConversationLive() {
        File(dir, "liborb.so").apply { writeText("#!/bin/sh\nexec /usr/bin/tail -f '${feed.path}'\n"); setExecutable(true) }
        app.applicationInfo.nativeLibraryDir = dir.path
        ShadowEnvironment.addExternalDir(File(dir, "storage").path)
        say(JSONObject().put("t", "state").put("self", "me").put("tabs", JSONArray()
            .put(tab("a", "bridge reconnect profile") { put("busy", true).put("context", 0.34) })
            .put(tab("b", "fix flaky e2e") { put("ask", JSONObject().put("id", "q").put("title", "run tests?").put("message", "Run the suite?")) })
            .put(tab("c", "release 0.21 notes") { put("peer", "mac").put("remote", true).put("where", "Baud's MacBook").put("context", 0.86) })
            .put(tab("d", "worker bundle size"))))
        for ((id, lines) in mapOf(
            "a" to listOf("Profile the reconnect path", "Most of it is the TLS handshake, then the replay of missed events."),
            "b" to listOf("Why does e2e flake?", "The fixture races the server start; I can retry the dial."),
            "c" to listOf("Draft the 0.21 notes", "Tiles on the desktop, the model chosen on Home, slimmer cards."),
        )) say(JSONObject().put("t", "rows").put("tab", id).put("at", 0).put("rows", JSONArray()
            .put(JSONObject().put("k", "0").put("kind", "you").put("text", lines[0]))
            .put(JSONObject().put("k", "1").put("kind", "run").put("what", "read bridge/attach.go"))
            .put(JSONObject().put("k", "2").put("kind", "md").put("block", md(lines[1])))))
        val until = System.currentTimeMillis() + 10_000
        while (rt.view.state.tabs.size != 4 || rt.view.rows["c"].isNullOrEmpty()) {
            check(System.currentTimeMillis() < until) { "the view never got the conversations" }
            ShadowLooper.idleMainLooper()
            Thread.sleep(20)
        }
        val bare = ComponentName(app, ComponentActivity::class.java)
        shadowOf(app.packageManager).apply { addActivityIfNotPresent(bare); addIntentFilterForActivity(bare, IntentFilter(Intent.ACTION_MAIN).apply { addCategory(Intent.CATEGORY_LAUNCHER) }) }
        ActivityScenario.launch(ComponentActivity::class.java).onActivity {
            it.setContent {
                val scope = rememberCoroutineScope()
                val nav = remember { Nav(rt, scope) }
                val c = remember { Ctx(rt, nav, mutableStateListOf(), {}, app, null, {}, {}, {}) { _, _ -> } }
                OrbTheme { Column { Overview(c) {} } }
            }
        }
        compose.waitForIdle()
        compose.onNodeWithText("fix flaky e2e").assertIsDisplayed()
        compose.onNodeWithText("asks · run tests?").assertIsDisplayed()
        compose.onNodeWithText("Tiles on the desktop", substring = true).assertIsDisplayed()
        compose.onNodeWithText("+ new session").assertIsDisplayed()
        System.getenv("ORB_OVERVIEW_PNG")?.let { path ->
            File(path).outputStream().use { out -> compose.onRoot().captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, out) }
        }
        dir.deleteRecursively()
    }
}
