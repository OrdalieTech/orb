package tech.ordalie.orb.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.widget.Toast
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import tech.ordalie.orb.core.Bridge
import tech.ordalie.orb.core.Release
import tech.ordalie.orb.core.Peer

/** Photographs a QR code with Play services' scanner: the camera never belongs to this app. */
fun Context.scan(found: (String) -> Unit) {
    val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).enableAutoZoom().build()
    GmsBarcodeScanning.getClient(this, options).startScan()
        .addOnSuccessListener { code -> code.rawValue?.let(found) }
        .addOnFailureListener { Toast.makeText(this, "scanner unavailable · paste the code instead", Toast.LENGTH_LONG).show() }
}

@Composable
fun ColumnScope.BridgeScreen(c: Ctx) {
    val b = c.rt.bridge
    val context = LocalContext.current
    Header("bridge", sub = if (b.up) "on · peer to peer" else "starting", back = c.nav::back, big = true)
    if (c.rt.acting) PatternBlue { c.rt.local?.abort() }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        item {
            // One gesture pairs a computer: it shows a QR code, this phone photographs it, the computer says yes.
            Column(Modifier.fillMaxWidth().padding(bottom = 18.dp).press { context.scan { c.nav.go(Screen.Join(it, auto = true)) } }
                .border(1.dp, p.fg, RoundedCornerShape(Radius.Card)).padding(22.dp)) {
                Stretch("SCAN", 110.dp, squeeze = 0.62f)
                T("On the computer, run", Modifier.padding(top = 14.dp), color = p.mute)
                Box(Modifier.padding(vertical = 8.dp).border(1.dp, p.rule, RoundedCornerShape(12.dp)).padding(horizontal = 12.dp, vertical = 8.dp)) { T("orb bridge pair", bold = true) }
                T("then photograph its QR code. It asks you to approve this phone there.", size = 13.sp, color = p.meta)
                Row(Modifier.padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Btn("scan", inverted = true) { context.scan { c.nav.go(Screen.Join(it, auto = true)) } }
                    Btn("paste code") { c.nav.go(Screen.Join(context.paste())) }
                }
            }
        }
        item { Slot("devices", if (b.peers.isEmpty()) "none yet" else "${b.peers.count { it.connected }} / ${b.peers.size} connected") {} }
        items(b.peers, key = { it.id }) { PeerRow(it, c) }
        item {
            Slot("this phone", modifier = Modifier.padding(top = 18.dp)) {
                BasicText(b.self, style = mono(13.sp, p.mute))
                Row(Modifier.padding(vertical = 14.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) { Btn("invite a computer") { c.nav.go(Screen.Invite) } }
                T("Each device is approved by fingerprint on its own screen. Pairs stay paired and reconnect by themselves; forget revokes one.", Modifier.padding(bottom = 24.dp), size = Size.Label, color = p.meta)
            }
        }
    }
}

@Composable
private fun PeerRow(peer: Peer, c: Ctx) = Column(Modifier.animateContentSize()) {
    val scope = rememberCoroutineScope()
    var updating by remember(peer.version) { mutableStateOf("") }
    Row(
        Modifier.fillMaxWidth().press { peer.instances.firstOrNull()?.let { c.nav.go(Screen.Chat(c.rt.open(it))) } }.padding(vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Dot(if (peer.connected) p.fg else p.rule, 8.dp, pulse = peer.instances.any { it.busy })
        Column(Modifier.weight(1f)) {
            T(peer.name, size = 17.sp, bold = peer.connected, color = if (peer.connected) p.fg else p.meta)
            T(listOfNotNull(if (peer.connected) "${peer.instances.size} session" + (if (peer.instances.size == 1) "" else "s") else peer.state, peer.short, peer.version.ifEmpty { null }?.let { "orb $it" }).joinToString(" · "), size = Size.Label, color = p.meta)
            if (updating.isNotEmpty()) T(updating, size = Size.Label, color = p.mute)
        }
        // A device behind the latest release updates from here (its Orb swaps itself and restarts Bridge).
        c.rt.latest?.takeIf { peer.version.isNotEmpty() && peer.connected && Release.newer(it, peer.version) && updating.isEmpty() }?.let { next ->
            Btn("update") { updating = "updating to $next…"; scope.launch { updating = c.rt.bridge.update(peer.id) } }
        }
        Box(Modifier.press { c.pick(Picker("forget ${peer.name}?", listOf("forget · revoke its access", "keep")) { if (it.startsWith("forget")) c.rt.scope.launch { c.rt.bridge.forget(peer.id) } }) }.padding(8.dp)) { T("×", size = 20.sp, color = p.meta) }
    }
    Rule()
}

@Composable
fun ColumnScope.InviteScreen(c: Ctx) {
    val context = LocalContext.current
    var inv by remember { mutableStateOf(c.rt.bridge.invitation) }
    var now by remember { mutableLongStateOf(System.currentTimeMillis() / 1000) }
    LaunchedEffect(Unit) { if (inv == null) inv = c.rt.bridge.invite(); while (true) { delay(1000); now = System.currentTimeMillis() / 1000 } }
    val left = ((inv?.optLong("expires") ?: now) - now).coerceAtLeast(0)
    val code = inv?.let(c.rt.bridge::code).orEmpty()
    Header("invite a computer", back = c.nav::back)
    Column(Modifier.weight(1f).padding(horizontal = Margin), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row { T("on the computer, run", Modifier.weight(1f), label = true); T(if (inv == null) "creating" else "%d:%02d · single use".format(left / 60, left % 60), size = Size.Label, color = p.meta) }
        Box(Modifier.fillMaxWidth().border(1.dp, p.fg, RoundedCornerShape(16.dp)).padding(14.dp)) {
            BasicText("orb bridge join " + code.ifEmpty { "…" }, style = mono(12.sp, if (left > 0) p.fg else p.meta))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Btn("share", inverted = true) { context.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, "orb bridge join $code"), "Orb invitation")) }
            Btn("copy") { context.copy("orb bridge join $code") }
        }
        Spacer(Modifier.weight(1f))
        Rule()
        Row(verticalAlignment = Alignment.CenterVertically) { Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T("waiting for the computer", color = p.mute) }
        T("You approve it here by fingerprint once it runs the command. The code works once, for ten minutes.", Modifier.padding(bottom = 16.dp), size = Size.Label, color = p.meta)
    }
}

@Composable
fun ColumnScope.JoinScreen(c: Ctx, text: String, auto: Boolean) {
    val b = c.rt.bridge
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var value by remember { mutableStateOf(text) }
    var error by remember { mutableStateOf("") }
    val inviter = Bridge.parse(value)?.optString("peer_id").orEmpty()
    fun pair() { error = ""; scope.launch { b.join(value)?.let { error = it } } }
    LaunchedEffect(Unit) { if (auto && b.joining.isEmpty()) pair() }
    LaunchedEffect(b.joining) { if (b.joining == "paired") { delay(1100); b.cancelJoin(); while (c.nav.stack.size > 1) c.nav.back() } }
    Header("pair", back = { b.cancelJoin(); c.nav.back() })
    AnimatedContent(b.joining, Modifier.weight(1f), transitionSpec = { fadeIn(tween(240)) togetherWith fadeOut(tween(160)) }, label = "join") { state ->
        Column(Modifier.padding(horizontal = Margin), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (state.isEmpty()) {
                var editing by remember { mutableStateOf(inviter.isEmpty()) }
                if (!editing && inviter.isNotEmpty()) {
                    // A valid code reads as what it is: an invitation from one machine, for a while.
                    val left = ((Bridge.parse(value)?.optLong("expires") ?: 0) - System.currentTimeMillis() / 1000).coerceAtLeast(0)
                    Column(Modifier.fillMaxWidth().border(1.dp, p.fg, RoundedCornerShape(Radius.Card)).padding(22.dp)) {
                        T("invitation from", label = true, color = p.meta)
                        Stretch(inviter.substringAfterLast(':').take(8), 72.dp, squeeze = 0.7f, modifier = Modifier.padding(vertical = 10.dp))
                        T(inviter, size = Size.Label, color = p.meta)
                        Row(Modifier.padding(top = 12.dp)) {
                            T(if (left > 0) "valid %d:%02d · single use".format(left / 60, left % 60) else "expired", Modifier.weight(1f), size = 13.sp, color = if (left > 0) p.mute else Ink.Rupture)
                            Box(Modifier.press { editing = true }) { T("edit", size = 13.sp, color = p.meta) }
                        }
                    }
                    T("Pairing lets each side read and drive the other's sessions, and start Orb in its folders. The computer confirms this phone before anything is shared.", size = Size.Label, color = p.meta)
                } else {
                    T("Paste the code from  orb bridge pair,  or scan its QR code.", color = p.mute)
                    Box(Modifier.fillMaxWidth().heightIn(min = 120.dp).border(1.dp, p.fg, RoundedCornerShape(Radius.Card)).padding(16.dp)) {
                        BasicTextField(value, { value = it; error = "" }, Modifier.fillMaxWidth(), textStyle = mono(12.sp, p.fg), cursorBrush = SolidColor(p.fg))
                        if (value.isEmpty()) T("orb-bridge:v1:…", color = p.meta, size = 12.sp)
                    }
                    if (inviter.isNotEmpty()) T("from " + inviter.substringAfterLast(':').take(8), size = Size.Label, color = p.meta)
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Btn("pair", inverted = true) { pair() }
                    Btn("scan") { context.scan { value = it; pair() } }
                    Btn("paste") { value = context.paste() }
                }
                if (error.isNotEmpty()) T(error, color = Ink.Rupture)
            } else {
                Stretch(if (state == "paired") "PAIRED" else "APPROVE", 120.dp, squeeze = 0.6f, modifier = Modifier.padding(top = 12.dp))
                T(if (state == "paired") "The computer said yes. Its sessions appear on Home." else "On the computer, answer y. It shows this phone as", color = p.mute)
                if (state != "paired") {
                    // The fingerprint exactly as the terminal prints it, its start large enough to compare at a glance.
                    Stretch(b.self.substringAfterLast(':').take(8), 64.dp, squeeze = 0.7f)
                    BasicText(b.self, style = mono(13.sp, p.meta))
                    Row(Modifier.padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T(if (state == "claiming") "reaching the computer" else "waiting for the yes", Modifier.weight(1f), color = p.mute)
                        Btn("cancel") { b.cancelJoin() }
                    }
                }
            }
        }
    }
}

private fun Context.copy(text: String) = getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("orb", text))
private fun Context.paste(): String = getSystemService(ClipboardManager::class.java).primaryClip?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()
