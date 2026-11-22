package tech.ordalie.orb.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.*
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.*
import tech.ordalie.orb.R

/** Ordalie tokens: raw console × Evangelion × teenage engineering — drafting light & pitch-black OLED dark. */
object Ink {
    val Texte = Color(0xFFFFFFFF)
    val Rupture = Color(0xFFFF3D00)
    val Blue = Color(0xFF003CB3)
    val Charcoal = Color(0xFF090909)
}

@Immutable
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color)

/** Raw technical manual light & pitch-black OLED console dark. */
val LightPalette = Palette(Color(0xFFEFECE6), Ink.Charcoal, Color(0xFF2C2C2A), Color(0xFF52514C), Color(0xFFB4B1A9), Color(0xFFF8F7F3))
val DarkPalette = Palette(Color(0xFF000000), Color(0xFFF2F2ED), Color(0xFFB8B8B0), Color(0xFF7E7E76), Color(0xFF2E2E2C), Color(0xFF0A0A0A))
val LocalPalette = staticCompositionLocalOf { LightPalette }

/** A peer's hue (1 to 6, as the view assigns them; 0, this phone, takes the ink): six vivid hardware
 *  channel LED tones apart from signal vermilion, as on desktop and web. */
private val Hues = listOf(0xFF0047FF to 0xFF2E9BFF, 0xFF00875A to 0xFF00E58B, 0xFFCC6B00 to 0xFFFFB000, 0xFF6528D7 to 0xFFB185FF, 0xFF177E2E to 0xFF4CE052, 0xFFC81E78 to 0xFFFF4DA6)
@Composable
fun hue(n: Int, phone: Color = p.mute): Color = if (n <= 0) phone else Hues[(n - 1) % Hues.size].let { (light, dark) -> Color(if (p == DarkPalette) dark else light) }
val p: Palette @Composable get() = LocalPalette.current

/** Ubuntu Sans Mono, variable, so its weights are real; set without tracking at reading sizes (a
 *  monospace's cells are already tight), tracked out for small caps, in for titles. */
val Mono = FontFamily(listOf(400, 500, 600, 700).map { Font(R.font.ubuntu_sans_mono, FontWeight(it), variationSettings = FontVariation.Settings(FontVariation.weight(it))) })

/** One scale — title 19 · body 15 · label 11 — and three weights: regular reads, medium marks, semibold names. */
object Size { val Title = 19.sp; val Body = 15.sp; val Label = 11.sp }
val Regular = FontWeight.Normal
val Medium = FontWeight.Medium
val Strong = FontWeight.SemiBold

/** Sharp raw console geometry: 0dp corners throughout. */
val Soft = RoundedCornerShape(0.dp)
val Pane = RoundedCornerShape(0.dp)
val Margin = 16.dp

/** The display's corner radius, as the platform reports it; none where the screen is square. */
val LocalCorner = staticCompositionLocalOf { 0.dp }
/** A surface floating [inset] above the screen's bottom edge: its corners run concentric with the screen's. */
@Composable
fun bezel(inset: Dp) = RoundedCornerShape((LocalCorner.current - inset).coerceAtLeast(10.dp))

/** [corner] is the screen's corner radius in pixels. */
@Composable
fun OrbTheme(corner: Int = 0, content: @Composable () -> Unit) = CompositionLocalProvider(
    LocalPalette provides if (isSystemInDarkTheme()) DarkPalette else LightPalette,
    LocalCorner provides with(LocalDensity.current) { corner.toDp() }, content = content,
)

fun type(size: TextUnit = Size.Body, color: Color = Color.Unspecified, weight: FontWeight = FontWeight.Normal) =
    TextStyle(fontFamily = Mono, fontSize = size, color = color, fontWeight = weight, letterSpacing = if (size >= Size.Title) (-0.02).em else 0.em, lineHeight = 1.45.em)

/** The only text primitive. Labels are small caps in medium weight; everything else is sentence case. */
@Composable
fun T(
    text: String, modifier: Modifier = Modifier, size: TextUnit = Size.Body, color: Color = p.fg, bold: Boolean = false,
    weight: FontWeight = if (bold) Strong else Regular, label: Boolean = false, lines: Int = Int.MAX_VALUE,
) = BasicText(
    if (label) text.uppercase() else text, modifier,
    if (label) type(Size.Label, color, Medium).copy(letterSpacing = 0.06.em) else type(size, color, weight),
    overflow = TextOverflow.Ellipsis, maxLines = lines,
)
