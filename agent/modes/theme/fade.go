package theme

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/themefile"
)

// Fade draws lines as if at opacity over the page: every truecolor
// foreground and background blends toward the page color, the terminal's
// default foreground included. A palette without a known page (256 colors, a
// theme without pageBg) uses the terminal's faint attribute instead.
func (theme *Theme) Fade(lines []string, opacity float64) []string {
	palette := theme.Palette()
	page, pageOK := hexRGB(palette.export["pageBg"].Text)
	ink, inkOK := hexRGB(palette.resolved["toolTitle"].Text)
	blend := pageOK && inkOK && palette.mode == TrueColor
	faded := make([]string, len(lines))
	for index, line := range lines {
		if blend {
			faded[index] = fadeLine(line, page, ink, opacity)
		} else {
			faded[index] = faintLine(line)
		}
	}
	return faded
}

// A shimmer's band sweeps shimmerSpeed cells a second, shimmerBand cells to
// either side of its center at full strength fading to none, and starts and
// ends shimmerPad cells off the text, so its first frame shows no band.
const (
	shimmerSpeed = 12.0
	shimmerBand  = 4.0
	shimmerPad   = 10
)

// Shimmer draws plain text in the muted color with a band of the text color
// sweeping across it, elapsed after the sweep began. A palette without
// truecolor for both, or text that brings its own styling, stays muted.
func Shimmer(text string, elapsed time.Duration) string {
	palette := Current().Palette()
	if palette == nil || palette.mode != TrueColor || strings.Contains(text, "\x1b") {
		return FG("muted", text)
	}
	base, baseOK := hexRGB(palette.resolved["muted"].Text)
	glow, glowOK := hexRGB(palette.resolved["text"].Text)
	if !baseOK || !glowOK {
		return FG("muted", text)
	}
	cells := []rune(text)
	center := math.Mod(elapsed.Seconds()*shimmerSpeed, float64(len(cells)+2*shimmerPad)) - shimmerPad
	var out strings.Builder
	last := ""
	for index, cell := range cells {
		strength := 0.0
		if distance := math.Abs(float64(index) - center); distance < shimmerBand {
			strength = (1 + math.Cos(math.Pi*distance/shimmerBand)) / 2
		}
		if color := glow.toward(base, strength, "38"); color != last {
			out.WriteString("\x1b[" + color + "m")
			last = color
		}
		out.WriteRune(cell)
	}
	out.WriteString("\x1b[39m")
	return out.String()
}

type rgb [3]float64

func hexRGB(value string) (rgb, bool) {
	r, g, b, err := themefile.ParseHex(value)
	return rgb{float64(r), float64(g), float64(b)}, err == nil && value != ""
}

// toward returns color seen at opacity over page, as a truecolor SGR parameter list.
func (color rgb) toward(page rgb, opacity float64, layer string) string {
	var out strings.Builder
	out.WriteString(layer)
	out.WriteString(";2")
	for i := range color {
		out.WriteByte(';')
		out.WriteString(strconv.Itoa(int(page[i] + (color[i]-page[i])*opacity + .5)))
	}
	return out.String()
}

// fadeLine rewrites every SGR sequence in line; other escapes pass through.
func fadeLine(line string, page, ink rgb, opacity float64) string {
	inkFG := "\x1b[" + ink.toward(page, opacity, "38") + "m"
	var out strings.Builder
	out.Grow(len(line) + 32)
	out.WriteString(inkFG)
	for index := 0; index < len(line); {
		end := sgrEnd(line, index)
		if end < 0 {
			out.WriteByte(line[index])
			index++
			continue
		}
		params := strings.Split(line[index+2:end], ";")
		rewritten := make([]string, 0, len(params)+4)
		for i := 0; i < len(params); i++ {
			switch param := params[i]; {
			case param == "" || param == "0":
				rewritten = append(rewritten, "0", ink.toward(page, opacity, "38"))
			case param == "39":
				rewritten = append(rewritten, ink.toward(page, opacity, "38"))
			case (param == "38" || param == "48") && i+4 < len(params) && params[i+1] == "2":
				r, _ := strconv.Atoi(params[i+2])
				g, _ := strconv.Atoi(params[i+3])
				b, _ := strconv.Atoi(params[i+4])
				rewritten = append(rewritten, rgb{float64(r), float64(g), float64(b)}.toward(page, opacity, param))
				i += 4
			case (param == "38" || param == "48") && i+2 < len(params) && params[i+1] == "5":
				// ponytail: indexed colors keep their value; a truecolor palette emits none.
				rewritten = append(rewritten, params[i:i+3]...)
				i += 2
			default:
				rewritten = append(rewritten, param)
			}
		}
		out.WriteString("\x1b[")
		out.WriteString(strings.Join(rewritten, ";"))
		out.WriteByte('m')
		index = end + 1
	}
	out.WriteString("\x1b[39m")
	return out.String()
}

// faintLine keeps the faint attribute on across the resets inside line.
func faintLine(line string) string {
	var out strings.Builder
	out.WriteString("\x1b[2m")
	for index := 0; index < len(line); {
		end := sgrEnd(line, index)
		if end < 0 {
			out.WriteByte(line[index])
			index++
			continue
		}
		out.WriteString(line[index : end+1])
		params := strings.Split(line[index+2:end], ";")
		for i := 0; i < len(params); i++ {
			if params[i] == "38" || params[i] == "48" {
				i += 4 // a color's own values are not resets
				if i-3 < len(params) && params[i-3] == "5" {
					i -= 2
				}
				continue
			}
			if params[i] == "" || params[i] == "0" || params[i] == "22" {
				out.WriteString("\x1b[2m")
				break
			}
		}
		index = end + 1
	}
	out.WriteString("\x1b[22m")
	return out.String()
}

// sgrEnd returns the index of the final "m" of an SGR sequence starting at index, or -1.
func sgrEnd(line string, index int) int {
	if line[index] != '\x1b' || index+1 >= len(line) || line[index+1] != '[' {
		return -1
	}
	for end := index + 2; end < len(line); end++ {
		switch c := line[end]; {
		case c == 'm':
			return end
		case c != ';' && (c < '0' || c > '9'):
			return -1
		}
	}
	return -1
}
