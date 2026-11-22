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

/** Ordalie tokens: teenage engineering anodized field white & OP-XY matte carbon, International Orange (#FF4F00). */
object Ink {
    val Texte = Color(0xFFFAFAFA)
    val Rupture = Color(0xFFFF4F00)
    val Blue = Color(0xFF1446A0)
    val Charcoal = Color(0xFF141413)
}

@Immutable
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color)

/** Anodized field white & ceramic surfaces in light; matte carbon in dark. */
val LightPalette = Palette(Color(0xFFF4F4F1), Ink.Charcoal, Color(0xFF575854), Color(0xFF8A8B85), Color(0xFFDCDCD7), Color(0xFFFFFFFF))
val DarkPalette = Palette(Color(0xFF0D0E10), Color(0xFFF2F2EE), Color(0xFFA1A39E), Color(0xFF6E706B), Color(0xFF26282C), Color(0xFF16171A))
val LocalPalette = staticCompositionLocalOf { LightPalette }

/** A peer's hue (1 to 6, as the view assigns them; 0, this phone, takes the ink): six rotary encoder
 *  LED tones apart from each other and from International Orange, as on desktop and web. */
private val Hues = listOf(0xFF1D63ED to 0xFF5B9BFF, 0xFF009E7A to 0xFF2DD4BF, 0xFFD98A00 to 0xFFF5B041, 0xFF7048E8 to 0xFFA78BFA, 0xFF379E28 to 0xFF6EE755, 0xFFD63384 to 0xFFF472B6)
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

/** Two clean machined radii: 7 for controls and inputs, 12 for floating cards. */
val Soft = RoundedCornerShape(7.dp)
val Pane = RoundedCornerShape(12.dp)
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
