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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
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
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color, val dark: Boolean)

/** Paper, not white; charcoal, not black — the contrast of printed matter. */
val LightPalette = Palette(Color(0xFFE3E7E0), Ink.Charcoal, Color(0xFF585E59), Color(0xFF8A908B), Color(0xFFB4BAB3), Color(0xFFEBEEE8), false)
val DarkPalette = Palette(Color(0xFF1C201E), Color(0xFFDADFD8), Color(0xFFA2A8A3), Color(0xFF727974), Color(0xFF3F4541), Color(0xFF232826), true)
val LocalPalette = staticCompositionLocalOf { LightPalette }
val p: Palette @Composable get() = LocalPalette.current

val Mono = FontFamily(Font(R.font.ubuntu_mono_regular), Font(R.font.ubuntu_mono_bold, FontWeight.Bold))

/** One scale for words — title 22 · body 16 · label 12. Numbers and names use the stretched readout. */
object Size { val Title = 22.sp; val Body = 16.sp; val Label = 12.sp }

/** Two radii: fully round for what you press, 28 for what you hold. */
object Radius { val Card = 28.dp }
val Margin = 20.dp

@Composable
fun OrbTheme(content: @Composable () -> Unit) =
    CompositionLocalProvider(LocalPalette provides if (isSystemInDarkTheme()) DarkPalette else LightPalette, content = content)

fun mono(size: TextUnit = Size.Body, color: Color = Color.Unspecified, bold: Boolean = false, spacing: TextUnit = 0.sp) =
    TextStyle(fontFamily = Mono, fontSize = size, color = color, fontWeight = if (bold) FontWeight.Bold else FontWeight.Normal, letterSpacing = spacing, lineHeight = 1.35.em)

/** The only text primitive. Labels are small caps in the ink colour; sentences are sentence case. */
@Composable
fun T(
    text: String, modifier: Modifier = Modifier, size: TextUnit = Size.Body, color: Color = p.fg, bold: Boolean = false,
    label: Boolean = false, strike: Boolean = false, lines: Int = Int.MAX_VALUE,
) = BasicText(
    if (label) text.uppercase() else text, modifier,
    mono(if (label) Size.Label else size, color, bold, if (label) 0.03.em else 0.sp).copy(textDecoration = if (strike) TextDecoration.LineThrough else null),
    overflow = TextOverflow.Ellipsis, maxLines = lines,
)
