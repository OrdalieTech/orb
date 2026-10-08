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

/** Ordalie tokens: soft ink on sage paper, one rupture, a blue kept for remote Orbs acting here. */
object Ink {
    val Texte = Color(0xFFFAF9F6)
    val Rupture = Color(0xFFC94A3D)
    val Blue = Color(0xFF173E78)
    val Charcoal = Color(0xFF2A2D2B)
}

@Immutable
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color)

/** Paper, not white; charcoal, not black — the contrast of printed matter. Rules are drawn, not hinted. */
val LightPalette = Palette(Color(0xFFE3E7E0), Ink.Charcoal, Color(0xFF4F5550), Color(0xFF7E847F), Color(0xFF9EA49F), Color(0xFFECEFE9))
val DarkPalette = Palette(Color(0xFF1C201E), Color(0xFFDADFD8), Color(0xFFA8AEA9), Color(0xFF7A817C), Color(0xFF4E5550), Color(0xFF242927))
val LocalPalette = staticCompositionLocalOf { LightPalette }

/** A peer's hue (1 to 6, as the view assigns them; 0, this phone, takes the ink): six tones apart
 *  from each other and from the rupture red, as on macOS. */
private val Hues = listOf(0xFF2F5FA8 to 0xFF6E9BE0, 0xFF1F7F73 to 0xFF4FBFAE, 0xFF9A6A12 to 0xFFD9A441, 0xFF6A47A8 to 0xFFA88BE0, 0xFF4A7A22 to 0xFF8CC255, 0xFF9C3F86 to 0xFFD77CC4)
@Composable
fun hue(n: Int, phone: Color = p.mute): Color = if (n <= 0) phone else Hues[(n - 1) % Hues.size].let { (light, dark) -> Color(if (p == DarkPalette) dark else light) }
val p: Palette @Composable get() = LocalPalette.current

/** Ubuntu Sans Mono, variable, so its weights are real; set a touch tight, as words rather than a grid. */
val Mono = FontFamily(listOf(400, 500, 600, 700).map { Font(R.font.ubuntu_sans_mono, FontWeight(it), variationSettings = FontVariation.Settings(FontVariation.weight(it))) })

/** One scale — title 19 · body 15 · label 11 — and three weights: regular reads, medium marks, semibold names. */
object Size { val Title = 19.sp; val Body = 15.sp; val Label = 11.sp }
val Regular = FontWeight.Normal
val Medium = FontWeight.Medium
val Strong = FontWeight.SemiBold

/** Two calm radii: 6 for what you press or type in, 10 for what holds something. */
val Soft = RoundedCornerShape(6.dp)
val Pane = RoundedCornerShape(10.dp)
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
    TextStyle(fontFamily = Mono, fontSize = size, color = color, fontWeight = weight, letterSpacing = (-0.03).em, lineHeight = 1.4.em)

/** The only text primitive. Labels are small caps in medium weight; everything else is sentence case. */
@Composable
fun T(
    text: String, modifier: Modifier = Modifier, size: TextUnit = Size.Body, color: Color = p.fg, bold: Boolean = false,
    weight: FontWeight = if (bold) Strong else Regular, label: Boolean = false, lines: Int = Int.MAX_VALUE,
) = BasicText(
    if (label) text.uppercase() else text, modifier,
    if (label) type(Size.Label, color, Medium).copy(letterSpacing = 0.02.em) else type(size, color, weight),
    overflow = TextOverflow.Ellipsis, maxLines = lines,
)
