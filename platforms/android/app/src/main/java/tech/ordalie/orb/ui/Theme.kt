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

/** Ordalie tokens: pure teenage engineering (OP-1 field light & OP-XY matte-black dark) × Evangelion. */
object Ink {
    val Texte = Color(0xFFFFFFFF)
    val Rupture = Color(0xFFEB431E)
    val Blue = Color(0xFF0044CC)
    val Charcoal = Color(0xFF161614)
}

@Immutable
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color)

/** OP-1 field bead-blasted aluminum paper light & OP-XY matte-black dark. */
val LightPalette = Palette(Color(0xFFECEBE6), Ink.Charcoal, Color(0xFF4E4D48), Color(0xFF76746E), Color(0xFFD1CEC6), Color(0xFFF5F4F0))
val DarkPalette = Palette(Color(0xFF0C0C0C), Color(0xFFEAE9E4), Color(0xFF9E9D97), Color(0xFF6D6C67), Color(0xFF292926), Color(0xFF141413))
val LocalPalette = staticCompositionLocalOf { LightPalette }

/** A peer's hue (1 to 6, as the view assigns them; 0, this phone, takes the ink): six vivid hardware
 *  channel LED tones apart from signal vermilion, as on desktop and web. */
private val Hues = listOf(0xFF0052E0 to 0xFF3E9BFF, 0xFF008256 to 0xFF12D687, 0xFFB86800 to 0xFFE5A00D, 0xFF6534CC to 0xFFAC85FF, 0xFF1B7A34 to 0xFF4BD65A, 0xFFBE2574 to 0xFFF554A4)
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

/** Machined teenage engineering chamfer: 2dp / 3dp precision geometry. */
val Soft = RoundedCornerShape(2.dp)
val Pane = RoundedCornerShape(3.dp)
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
