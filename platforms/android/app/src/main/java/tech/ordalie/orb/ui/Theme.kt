package tech.ordalie.orb.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontVariation
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
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
val p: Palette @Composable get() = LocalPalette.current

/** Ubuntu Sans for words, Ubuntu Sans Mono for what a machine reads; both variable, so weights are real. */
private fun variable(res: Int, vararg weights: Int) = FontFamily(weights.map { Font(res, FontWeight(it), variationSettings = FontVariation.Settings(FontVariation.weight(it))) })
val Sans = variable(R.font.ubuntu_sans, 400, 500, 600, 700)
val Mono = variable(R.font.ubuntu_sans_mono, 400, 600)

/** One scale — title 20 · body 16 · label 12 — and three weights: regular reads, medium marks, semibold names. */
object Size { val Title = 20.sp; val Body = 16.sp; val Label = 12.sp }
val Regular = FontWeight.Normal
val Medium = FontWeight.Medium
val Strong = FontWeight.SemiBold

/** Two radii: fully round for what you press, 28 for what you hold. */
object Radius { val Card = 28.dp }
val Margin = 20.dp

@Composable
fun OrbTheme(content: @Composable () -> Unit) =
    CompositionLocalProvider(LocalPalette provides if (isSystemInDarkTheme()) DarkPalette else LightPalette, content = content)

fun type(size: TextUnit = Size.Body, color: Color = Color.Unspecified, weight: FontWeight = FontWeight.Normal, mono: Boolean = false) =
    TextStyle(fontFamily = if (mono) Mono else Sans, fontSize = size, color = color, fontWeight = weight, letterSpacing = if (mono) (-0.02).em else 0.sp, lineHeight = 1.4.em)

/** The only text primitive. Labels are small caps in medium weight; everything else is sentence case. */
@Composable
fun T(
    text: String, modifier: Modifier = Modifier, size: TextUnit = Size.Body, color: Color = p.fg, bold: Boolean = false,
    weight: FontWeight = if (bold) Strong else Regular, label: Boolean = false, mono: Boolean = false, lines: Int = Int.MAX_VALUE,
) = BasicText(
    if (label) text.uppercase() else text, modifier,
    if (label) type(11.sp, color, Medium).copy(letterSpacing = 0.06.em) else type(size, color, weight, mono),
    overflow = TextOverflow.Ellipsis, maxLines = lines,
)
