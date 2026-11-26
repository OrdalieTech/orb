package tech.ordalie.orb.ui

import android.content.*
import android.widget.Toast
import androidx.compose.animation.*
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.*
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

/** Photographs a QR code with Play services' scanner: the camera never belongs to this app. */
fun Context.scan(found: (String) -> Unit) {
    val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).enableAutoZoom().build()
    GmsBarcodeScanning.getClient(this, options).startScan()
        .addOnSuccessListener { code -> code.rawValue?.let(found) }
        .addOnFailureListener { Toast.makeText(this, "scanner unavailable · paste the code instead", Toast.LENGTH_LONG).show() }
}

@Composable
fun ColumnScope.MitosisScreen(c: Ctx) {
    val v = c.v
    val context = LocalContext.current
    Header("Mitosis", sub = if (v.state.up) "on · peer to peer" else "starting", back = c.nav::back)
    if (v.state.acting) PatternBlue(stop = { v.state.tabs.filter { it.acting }.forEach { v.send("abort", "tab" to it.id) } })
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        item {
            // One gesture pairs a computer: it shows a QR code, this phone photographs it, the computer says yes.
            Column(Modifier.fillMaxWidth().padding(bottom = 18.dp).press { context.scan { c.nav.go(Screen.Join(it)) } }
                .border(1.dp, p.fg, Pane).padding(16.dp)) {
                T("Pair a computer", size = Size.Title, weight = Strong)
                T("On the computer, run", Modifier.padding(top = 10.dp), color = p.mute)
                Box(Modifier.padding(vertical = 8.dp).border(1.dp, p.rule, Soft).padding(horizontal = 12.dp, vertical = 8.dp)) { T("orb mitosis", weight = Strong) }
                T("then photograph its QR code. It asks you to approve this phone there.", size = 13.sp, color = p.meta)
                Row(Modifier.padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Btn("scan", inverted = true) { context.scan { c.nav.go(Screen.Join(it)) } }
                    Btn("paste code") { c.nav.go(Screen.Join(context.paste())) }
                }
            }
        }
        val devices = v.home.machines.filter { !it.self }
        item { Slot("devices", if (devices.isEmpty()) "none yet" else "${devices.count { it.connected }} / ${devices.size} connected") {} }
        items(devices, key = { it.id }) { PeerRow(it, c) }
        item {
            Slot("this phone", modifier = Modifier.padding(top = 18.dp)) {
                T(v.state.self, size = 13.sp, color = p.mute)
                Row(Modifier.padding(vertical = 14.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) { Btn("invite a computer") { c.nav.go(Screen.Invite) } }
                T("Each device is approved by fingerprint on its own screen. Pairs stay paired and reconnect by themselves; forget revokes one.", Modifier.padding(bottom = 24.dp), size = Size.Label, color = p.meta)
            }
        }
    }
}

@Composable
private fun PeerRow(peer: Machine, c: Ctx) = Column(Modifier.animateContentSize()) {
    val scope = rememberCoroutineScope()
    var updating by remember(peer.version) { mutableStateOf("") }
    Row(
        Modifier.fillMaxWidth().press { peer.running.firstOrNull()?.let { c.open("i:" + it.instance) } }.padding(vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Dot(if (peer.connected) p.fg else p.rule, 8.dp, pulse = peer.running.any { it.busy })
        Column(Modifier.weight(1f)) {
            T(peer.name, size = 17.sp, weight = if (peer.connected) Strong else Regular, color = if (peer.connected) p.fg else p.meta)
            T(listOfNotNull(if (peer.connected) "${peer.running.size} session" + (if (peer.running.size == 1) "" else "s") else "disconnected", peer.id.substringAfterLast(":").take(6), peer.version.ifEmpty { null }?.let { "orb $it" }).joinToString(" · "), size = Size.Label, color = p.meta)
            if (updating.isNotEmpty()) T(updating, size = Size.Label, color = p.mute)
        }
        // A device behind the latest release updates from here (its Orb swaps itself and restarts Mitosis).
        c.v.state.latest.takeIf { it.isNotEmpty() && peer.version.isNotEmpty() && peer.connected && Release.newer(it, peer.version) && updating.isEmpty() }?.let { next ->
            Btn("update") { updating = "updating to $next…"; scope.launch { c.v.ask("update", "machine" to peer.id).let { updating = it.error ?: it.result?.toString().orEmpty() } } }
        }
        // Named here (it may not say its own name), or forgotten.
        Box(Modifier.press {
            c.pick(Picker(peer.name, listOf("rename", "forget · revoke its access")) {
                if (it == "rename") c.rename(Rename(peer.name) { name -> c.v.send("rename", "machine" to peer.id, "name" to name) })
                else c.v.send("forget", "machine" to peer.id)
            })
        }.padding(8.dp)) { T("⋯", size = 20.sp, color = p.meta) }
    }
    Rule()
}

private data class SpreadHost(val alias: String, val hostname: String, val user: String, val port: Int)
private data class SpreadClient(val id: String, val name: String, val found: Boolean)

@Composable
fun ColumnScope.InviteScreen(c: Ctx) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val inv = c.v.state.invitation
    var now by remember { mutableLongStateOf(System.currentTimeMillis() / 1000) }
    var spreadMachine by remember { mutableStateOf("") }
    var spreadMachineName by remember { mutableStateOf("") }
    var sshHosts by remember { mutableStateOf<List<SpreadHost>>(emptyList()) }
    var clients by remember { mutableStateOf<List<SpreadClient>>(emptyList()) }
    var sshTarget by remember { mutableStateOf("") }
    var spreading by remember { mutableStateOf(false) }
    var spreadStatus by remember { mutableStateOf("") }
    var spreadError by remember { mutableStateOf("") }

    LaunchedEffect(Unit) {
        if (inv == null) c.v.send("invite")
        launch {
            val obj = c.v.ask("spread.info").obj ?: return@launch
            spreadMachine = obj.optString("machine")
            spreadMachineName = obj.optString("machine_name")
            val hostsArr = obj.optJSONArray("ssh_hosts")
            if (hostsArr != null) {
                sshHosts = buildList {
                    for (i in 0 until hostsArr.length()) {
                        val h = hostsArr.optJSONObject(i) ?: continue
                        val alias = h.optString("alias")
                        if (alias.isNotEmpty()) {
                            add(SpreadHost(alias, h.optString("hostname"), h.optString("user"), h.optInt("port", 22)))
                        }
                    }
                }
            }
            val clientsArr = obj.optJSONArray("clients")
            if (clientsArr != null) {
                clients = buildList {
                    for (i in 0 until clientsArr.length()) {
                        val cl = clientsArr.optJSONObject(i) ?: continue
                        val name = cl.optString("name")
                        if (name.isNotEmpty() && cl.optBoolean("found")) {
                            add(SpreadClient(cl.optString("id"), name, true))
                        }
                    }
                }
            }
        }
        while (true) { delay(1000); now = System.currentTimeMillis() / 1000 }
    }

    val left = ((inv?.expires ?: now) - now).coerceAtLeast(0)
    val code = inv?.code.orEmpty()
    val joinCmd = "orb mitosis join $code"
    val installAndJoinCmd = "command -v orb >/dev/null || curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh; orb mitosis join $code"
    val browserLink = "https://netapy.com/orb/#$code"

    fun runSpread() {
        val target = sshTarget.trim()
        if (target.isEmpty() || code.isEmpty() || spreading) return
        spreading = true
        spreadError = ""
        spreadStatus = "spreading to $target…"
        scope.launch {
            val reply = c.v.ask("spread.ssh", "machine" to spreadMachine, "target" to target, "code" to code)
            spreading = false
            if (reply.error != null) {
                spreadStatus = ""
                spreadError = reply.error
            } else {
                spreadError = ""
                val msg = reply.obj?.optString("message").orEmpty().ifEmpty { reply.result?.toString().orEmpty() }
                spreadStatus = msg.ifEmpty { "spread sent to $target" }
            }
        }
    }

    Header("Invite a computer", back = c.nav::back)
    Column(
        Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = Margin),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            T("on the computer, run", Modifier.weight(1f), label = true)
            T(if (inv == null) "creating" else "%d:%02d · single use".format(left / 60, left % 60), size = Size.Label, color = if (left > 0 || inv == null) p.meta else Ink.Rupture)
        }
        Box(Modifier.fillMaxWidth().border(1.dp, p.fg, Pane).padding(14.dp)) {
            T("orb mitosis join " + code.ifEmpty { "…" }, size = 12.sp, color = if (left > 0) p.fg else p.meta)
        }
        Row(
            Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Btn("install + join", inverted = true) { if (code.isNotEmpty()) context.copy(installAndJoinCmd, "install + join copied") }
            Btn("browser link") { if (code.isNotEmpty()) context.copy(browserLink, "browser link copied") }
            Btn("join") { if (code.isNotEmpty()) context.copy(joinCmd, "join command copied") }
            Btn("share") { if (code.isNotEmpty()) context.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, joinCmd), "Orb invitation")) }
            Btn("new code") { c.v.send("invite") }
        }

        Rule()

        Row(verticalAlignment = Alignment.CenterVertically) {
            T("spread via ssh", Modifier.weight(1f), label = true)
            if (spreadMachineName.isNotEmpty()) T("via $spreadMachineName", size = Size.Label, color = p.meta)
        }
        Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Box(Modifier.weight(1f).border(1.dp, p.rule, Pane).padding(horizontal = 12.dp, vertical = 10.dp)) {
                BasicTextField(
                    value = sshTarget,
                    onValueChange = { sshTarget = it; spreadError = "" },
                    modifier = Modifier.fillMaxWidth(),
                    singleLine = true,
                    textStyle = type(13.sp, p.fg),
                    cursorBrush = SolidColor(p.fg),
                )
                if (sshTarget.isEmpty()) T("ssh user@host or host alias", size = 13.sp, color = p.meta, lines = 1)
            }
            Btn(if (spreading) "…" else "spread", inverted = true) { runSpread() }
        }
        if (sshHosts.isNotEmpty()) {
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                sshHosts.forEach { host ->
                    val selected = sshTarget.trim() == host.alias
                    Box(
                        Modifier.clip(Pane)
                            .background(if (selected) p.raised else Color.Transparent)
                            .border(1.dp, if (selected) p.mute else p.rule, Pane)
                            .press { sshTarget = host.alias; spreadError = "" }
                            .padding(horizontal = 10.dp, vertical = 5.dp),
                    ) {
                        T(host.alias, size = 12.sp, weight = if (selected) Strong else Medium, color = if (selected) p.fg else p.mute, lines = 1)
                    }
                }
            }
        }
        if (spreadStatus.isNotEmpty()) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (spreading) Dot(p.mute, pulse = true)
                T(spreadStatus, size = 12.sp, color = p.mute)
            }
        }
        if (spreadError.isNotEmpty()) {
            T(spreadError, size = 12.sp, color = Ink.Rupture)
        }

        if (clients.isNotEmpty()) {
            Rule()
            T("hand to an agent", label = true)
            Row(
                Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                clients.forEach { client ->
                    Box(
                        Modifier.clip(Pane)
                            .border(1.dp, p.rule, Pane)
                            .press {
                                if (code.isNotEmpty()) {
                                    val prompt = "Install Orb if needed (curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh) and join my Mitosis mesh by running: orb mitosis join $code"
                                    context.copy(prompt, "prompt for ${client.name} copied")
                                }
                            }
                            .padding(horizontal = 10.dp, vertical = 5.dp),
                    ) {
                        T("hand to ${client.name}", size = 12.sp, weight = Medium, color = p.mute, lines = 1)
                    }
                }
            }
        }

        Rule()
        Row(verticalAlignment = Alignment.CenterVertically) { Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T("waiting for the computer", color = p.mute) }
        T("You approve it here by fingerprint once it runs the command. The code works once, for ten minutes.", Modifier.padding(bottom = 16.dp), size = Size.Label, color = p.meta)
    }
}

@Composable
fun ColumnScope.JoinScreen(c: Ctx, text: String) {
    val v = c.v
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var value by remember { mutableStateOf(text) }
    var error by remember { mutableStateOf("") }
    // What the code is, as the view reads it: an invitation from which device, until when.
    var inviter by remember { mutableStateOf("") }
    var expires by remember { mutableLongStateOf(0L) }
    LaunchedEffect(value) { v.ask("invitation", "text" to value).obj.let { inviter = it?.optString("peer").orEmpty(); expires = it?.optLong("expires") ?: 0 } }
    fun pair() { error = ""; scope.launch { v.ask("join", "text" to value).error?.let { if (it != "cancelled") error = it } } }
    LaunchedEffect(v.state.joining) { if (v.state.joining == "paired") { delay(1100); v.send("join.cancel"); while (c.nav.stack.size > 1) c.nav.back() } }
    Header("Pair", back = { v.send("join.cancel"); c.nav.back() })
    AnimatedContent(v.state.joining, Modifier.weight(1f), transitionSpec = { fadeIn(tween(240)) togetherWith fadeOut(tween(160)) }, label = "join") { state ->
        Column(Modifier.padding(horizontal = Margin), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (state.isEmpty()) {
                var editing by remember { mutableStateOf(inviter.isEmpty()) }
                if (!editing && inviter.isNotEmpty()) {
                    // A valid code reads as what it is: an invitation from one machine, for a while.
                    val left = (expires - System.currentTimeMillis() / 1000).coerceAtLeast(0)
                    Column(Modifier.fillMaxWidth().border(1.dp, p.fg, Pane).padding(16.dp)) {
                        T("invitation from", label = true, color = p.meta)
                        T(inviter.substringAfterLast(':').take(8), Modifier.padding(vertical = 8.dp), size = 28.sp, weight = Strong)
                        T(inviter, size = Size.Label, color = p.meta)
                        Row(Modifier.padding(top = 12.dp)) {
                            T(if (left > 0) "valid %d:%02d · single use".format(left / 60, left % 60) else "expired", Modifier.weight(1f), size = 13.sp, color = if (left > 0) p.mute else Ink.Rupture)
                            Box(Modifier.press { editing = true }) { T("edit", size = 13.sp, color = p.meta) }
                        }
                    }
                    T("Pairing lets this device read and drive the phone's conversations. Once it approves the phone, the phone gets its conversations and can start Orb in its folders. Pair only with a device you know.", size = Size.Label, color = p.meta)
                } else {
                    T("Paste the code from  orb mitosis,  or scan its QR code.", color = p.mute)
                    Box(Modifier.fillMaxWidth().heightIn(min = 120.dp).border(1.dp, p.fg, Pane).padding(16.dp)) {
                        BasicTextField(value, { value = it; error = "" }, Modifier.fillMaxWidth(), textStyle = type(12.sp, p.fg), cursorBrush = SolidColor(p.fg))
                        if (value.isEmpty()) T("orb-mitosis:v1:…", color = p.meta, size = 12.sp)
                    }
                    if (inviter.isNotEmpty()) T("from " + inviter.substringAfterLast(':').take(8), size = Size.Label, color = p.meta)
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Btn("pair", inverted = true) { pair() }
                    Btn("scan") { context.scan { value = it } }
                    Btn("paste") { value = context.paste() }
                }
                if (error.isNotEmpty()) T(error, color = Ink.Rupture)
            } else {
                T(if (state == "paired") "Paired" else "Approve on the computer", Modifier.padding(top = 12.dp), size = 24.sp, weight = Strong)
                T(if (state == "paired") "The computer said yes. Its sessions appear on Home." else "On the computer, answer y. It shows this phone as", color = p.mute)
                if (state != "paired") {
                    // The fingerprint exactly as the terminal prints it, its start large enough to compare at a glance.
                    T(v.state.self.substringAfterLast(':').take(8), size = 28.sp, weight = Strong)
                    T(v.state.self, size = 13.sp, color = p.meta)
                    Row(Modifier.padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T(if (state == "claiming") "reaching the computer" else "waiting for the yes", Modifier.weight(1f), color = p.mute)
                        Btn("cancel") { v.send("join.cancel") }
                    }
                }
            }
        }
    }
}

