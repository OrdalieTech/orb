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

/** Ordalie tokens: Field Paper & OP-XY charcoal ink, Evangelion × teenage engineering vermilion, Pattern Blue. */
object Ink {
    val Texte = Color(0xFFFAF9F6)
    val Rupture = Color(0xFFD94326)
    val Blue = Color(0xFF173E78)
    val Charcoal = Color(0xFF1E211F)
}

@Immutable
data class Palette(val bg: Color, val fg: Color, val mute: Color, val meta: Color, val rule: Color, val raised: Color)

/** Machined field paper, not white; instrument charcoal, not black — printed matter meets hardware. */
val LightPalette = Palette(Color(0xFFE5E7E1), Ink.Charcoal, Color(0xFF464B47), Color(0xFF636964), Color(0xFFA6ACA6), Color(0xFFEFF1EC))
val DarkPalette = Palette(Color(0xFF161817), Color(0xFFE2E6DF), Color(0xFFA4ABA5), Color(0xFF7F8680), Color(0xFF3D433F), Color(0xFF1F2220))
val LocalPalette = staticCompositionLocalOf { LightPalette }

/** A peer's hue (1 to 6, as the view assigns them; 0, this phone, takes the ink): six hardware LED
 *  tones apart from each other and from the signal vermilion, as on desktop and web. */
private val Hues = listOf(0xFF2558A8 to 0xFF6899E6, 0xFF167A6E to 0xFF4CC4B3, 0xFFA36A0B to 0xFFE0A83E, 0xFF6843AD to 0xFFA989E6, 0xFF3D781E to 0xFF87C452, 0xFF9E3A84 to 0xFFDB78C2)
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

/** Two machined radii: 4 for keycaps and inputs, 8 for panels. */
val Soft = RoundedCornerShape(4.dp)
val Pane = RoundedCornerShape(8.dp)
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
