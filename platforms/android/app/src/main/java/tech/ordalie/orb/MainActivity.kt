package tech.ordalie.orb

import android.Manifest
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import tech.ordalie.orb.ui.App
import tech.ordalie.orb.ui.OrbTheme
import java.io.File

class MainActivity : ComponentActivity() {
    private val cites = mutableStateListOf<String>()
    private val shared = mutableStateOf<String?>(null)

    /** Cited documents are copied into the workspace, where every Orb tool can read them. */
    private val pick = registerForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris -> uris.forEach(::cite) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 0)
        startForegroundService(Intent(this, OrbService::class.java))
        receive(intent)
        setContent { OrbTheme { App(runtime, cites, { pick.launch(arrayOf("*/*")) }, shared) } }
    }

    override fun onNewIntent(intent: Intent) { super.onNewIntent(intent); receive(intent) }

    private fun receive(intent: Intent?) {
        if (intent?.action == Intent.ACTION_SEND) shared.value = intent.getStringExtra(Intent.EXTRA_TEXT)
        if (intent?.action == Intent.ACTION_VIEW) shared.value = intent.dataString
    }

    private fun cite(uri: Uri) {
        val name = contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }?.replace('/', '_') ?: "document"
        val file = File(runtime.orb.workspace, "cites/$name").apply { parentFile?.mkdirs() }
        contentResolver.openInputStream(uri)?.use { input -> file.outputStream().use(input::copyTo) }
        cites += "cites/$name"
    }
}
