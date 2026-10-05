package themefile

// OKLab and OKHSL to sRGB follow Björn Ottosson's reference implementation
// (https://bottosson.github.io/posts/colorpicker/, MIT, © 2021 Björn
// Ottosson), as upstream's packages/tui/src/oklab.ts ports it; constants and
// rounding match so theme colors resolve to the same sRGB values.

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

type vector [3]float64
type matrix [3]vector

func (m matrix) apply(v vector) vector {
	var out vector
	for row := range m {
		out[row] = m[row][0]*v[0] + m[row][1]*v[1] + m[row][2]*v[2]
	}
	return out
}

var (
	labToLMS = matrix{
		{1, 0.3963377773761749, 0.2158037573099136},
		{1, -0.1055613458156586, -0.0638541728258133},
		{1, -0.0894841775298119, -1.2914855480194092},
	}
	lmsToLinearSRGB = matrix{
		{4.0767416360759583, -3.3077115392580629, 0.2309699031821043},
		{-1.2684379732850315, 2.6097573492876882, -0.341319376002657},
		{-0.0041960761386756, -0.7034186179359362, 1.7076146940746117},
	}
	// Per sRGB channel: the (a, b) half-plane where it clips first, and the
	// polynomial approximating the maximum saturation there.
	saturationFit = [3]struct {
		plane  [2]float64
		coeffs [5]float64
	}{
		{[2]float64{-1.8817031, -0.80936501}, [5]float64{1.19086277, 1.76576728, 0.59662641, 0.75515197, 0.56771245}},
		{[2]float64{1.8144408, -1.19445267}, [5]float64{0.73956515, -0.45954404, 0.08285427, 0.12541073, -0.14503204}},
		{[2]float64{0.13110758, 1.81333971}, [5]float64{1.35733652, -0.00915799, -1.1513021, -0.50559606, 0.00692167}},
	}
)

const (
	okK1 = 0.206
	okK2 = 0.03
	okK3 = (1 + okK1) / (1 + okK2)
)

func okhslToOklabLightness(x float64) float64 { return (x*x + okK1*x) / (okK3 * (x + okK2)) }

func linearToSRGB(value float64) float64 {
	if value > 0.0031308 {
		return 1.055*math.Pow(value, 1/2.4) - 0.055
	}
	return 12.92 * value
}

func oklabToLinearSRGB(lab vector) vector {
	lms := labToLMS.apply(lab)
	for index := range lms {
		lms[index] = lms[index] * lms[index] * lms[index]
	}
	return lmsToLinearSRGB.apply(lms)
}

// jsRound is Math.round: halves round toward positive infinity.
func jsRound(value float64) int { return int(math.Floor(value + 0.5)) }

func linearSRGBToHex(linear vector) string {
	channel := func(value float64) int {
		return jsRound(math.Min(1, math.Max(0, linearToSRGB(value))) * 255)
	}
	return fmt.Sprintf("#%02x%02x%02x", channel(linear[0]), channel(linear[1]), channel(linear[2]))
}

func lmsSlopes(a, b float64) vector {
	var out vector
	for row := range labToLMS {
		out[row] = labToLMS[row][1]*a + labToLMS[row][2]*b
	}
	return out
}

func maxSaturation(a, b float64) float64 {
	channel := 2
	for index := 0; index < 2; index++ {
		if saturationFit[index].plane[0]*a+saturationFit[index].plane[1]*b > 1 {
			channel = index
			break
		}
	}
	k := saturationFit[channel].coeffs
	weights := lmsToLinearSRGB[channel]
	saturation := k[0] + k[1]*a + k[2]*b + k[3]*a*a + k[4]*a*b
	slopes := lmsSlopes(a, b)
	var f, f1, f2 float64
	for index := range slopes {
		base := 1 + saturation*slopes[index]
		f += weights[index] * base * base * base
		f1 += weights[index] * 3 * slopes[index] * base * base
		f2 += weights[index] * 6 * slopes[index] * slopes[index] * base
	}
	return saturation - (f*f1)/(f1*f1-0.5*f*f2)
}

func okCusp(a, b float64) (float64, float64) {
	saturation := maxSaturation(a, b)
	linear := oklabToLinearSRGB(vector{1, saturation * a, saturation * b})
	lightness := math.Cbrt(1 / math.Max(linear[0], math.Max(linear[1], linear[2])))
	return lightness, lightness * saturation
}

func maxChroma(a, b, lightness, cuspL, cuspC float64) float64 {
	if lightness <= cuspL {
		return cuspC * lightness / cuspL
	}
	t := cuspC * (lightness - 1) / (cuspL - 1)
	slopes := lmsSlopes(a, b)
	var lms, cubes, first, second vector
	for index := range slopes {
		lms[index] = lightness + t*slopes[index]
		cubes[index] = lms[index] * lms[index] * lms[index]
		first[index] = 3 * slopes[index] * lms[index] * lms[index]
		second[index] = 6 * slopes[index] * slopes[index] * lms[index]
	}
	dot := func(row, values vector) float64 { return row[0]*values[0] + row[1]*values[1] + row[2]*values[2] }
	step := math.MaxFloat64
	for _, row := range lmsToLinearSRGB {
		f := dot(row, cubes) - 1
		f1 := dot(row, first)
		f2 := dot(row, second)
		u := f1 / (f1*f1 - 0.5*f*f2)
		candidate := math.MaxFloat64
		if u >= 0 {
			candidate = -f * u
		}
		step = math.Min(step, candidate)
	}
	return t + step
}

func chromaStops(l, a, b float64) (float64, float64, float64) {
	cuspL, cuspC := okCusp(a, b)
	cMax := maxChroma(a, b, l, cuspL, cuspC)
	k := cMax / math.Min(l*(cuspC/cuspL), (1-l)*(cuspC/(1-cuspL)))
	midS := 0.11516993 + 1/(7.4477897+4.1590124*b+
		a*(-2.19557347+1.75198401*b+a*(-2.13704948-10.02301043*b+a*(-4.24894561+5.38770819*b+4.69891013*a))))
	midT := 0.11239642 + 1/(1.6132032-0.68124379*b+
		a*(0.40370612+0.90148123*b+a*(-0.27087943+0.6122399*b+a*(0.00299215-0.45399568*b-0.14661872*a))))
	cMid := 0.9 * k * math.Sqrt(math.Sqrt(1/(1/math.Pow(l*midS, 4)+1/math.Pow((1-l)*midT, 4))))
	low, high := l*0.4, (1-l)*0.8
	c0 := math.Sqrt(1 / (1/(low*low) + 1/(high*high)))
	return c0, cMid, cMax
}

func okhslToHex(hue, saturation, lightness float64) string {
	l := okhslToOklabLightness(lightness)
	lab := vector{l, 0, 0}
	if l > 0 && l < 1 && saturation > 0 {
		angle := 2 * math.Pi * math.Mod(math.Mod(hue, 360)+360, 360) / 360
		a, b := math.Cos(angle), math.Sin(angle)
		c0, cMid, cMax := chromaStops(l, a, b)
		var chroma float64
		if saturation < 0.8 {
			t := 1.25 * saturation
			k1 := 0.8 * c0
			chroma = t * k1 / (1 - (1-k1/cMid)*t)
		} else {
			t := 5 * (saturation - 0.8)
			k1 := 0.2 * cMid * cMid * 1.25 * 1.25 / c0
			chroma = cMid + t*k1/(1-(1-k1/(cMax-cMid))*t)
		}
		lab = vector{l, chroma * a, chroma * b}
	}
	return linearSRGBToHex(oklabToLinearSRGB(lab))
}

func inSRGBGamut(linear vector) bool {
	const epsilon = 1e-7
	for _, channel := range linear {
		if channel < -epsilon || channel > 1+epsilon {
			return false
		}
	}
	return true
}

// oklchToHex keeps the hue and reduces chroma until the color fits sRGB.
func oklchToHex(l, c, h float64) string {
	radians := h * math.Pi / 180
	cos, sin := math.Cos(radians), math.Sin(radians)
	atChroma := func(chroma float64) vector { return oklabToLinearSRGB(vector{l, chroma * cos, chroma * sin}) }
	if direct := atChroma(c); inSRGBGamut(direct) {
		return linearSRGBToHex(direct)
	}
	linear := atChroma(0)
	low, high := 0.0, c
	for range 20 {
		chroma := (low + high) / 2
		if candidate := atChroma(chroma); inSRGBGamut(candidate) {
			low, linear = chroma, candidate
		} else {
			high = chroma
		}
	}
	return linearSRGBToHex(linear)
}

const numberPattern = `[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?`

var (
	oklchPattern = lazyregexp.New(`(?i)^oklch\(\s*(` + numberPattern + `)(%)?\s+(` + numberPattern + `)\s+(` + numberPattern + `)(?:deg)?\s*\)$`)
	okhslPattern = lazyregexp.New(`(?i)^okhsl\(\s*(` + numberPattern + `)(?:deg)?\s+(` + numberPattern + `)(%)?\s+(` + numberPattern + `)(%)?\s*\)$`)
	hex3Pattern  = lazyregexp.New(`(?i)^#[\da-f]{3}$`)
)

// isColorLiteral reports whether a theme string is a color rather than a
// variable reference.
func isColorLiteral(text string) bool {
	lower := strings.ToLower(text)
	return strings.HasPrefix(text, "#") || strings.HasPrefix(lower, "oklch(") || strings.HasPrefix(lower, "okhsl(")
}

// normalizeColor turns #rgb, oklch() and okhsl() into #rrggbb; other text is
// returned unchanged for ParseHex to validate.
func normalizeColor(text string) (string, error) {
	if hex3Pattern().MatchString(text) {
		return fmt.Sprintf("#%c%c%c%c%c%c", text[1], text[1], text[2], text[2], text[3], text[3]), nil
	}
	number := func(value string) float64 { parsed, _ := strconv.ParseFloat(value, 64); return parsed }
	if match := oklchPattern().FindStringSubmatch(text); match != nil {
		l := number(match[1])
		if match[2] != "" {
			l /= 100
		}
		c, h := number(match[3]), number(match[4])
		if l < 0 || l > 1 || c < 0 || math.IsInf(h, 0) {
			return "", fmt.Errorf("invalid color value: %s", text)
		}
		return oklchToHex(l, c, math.Mod(math.Mod(h, 360)+360, 360)), nil
	}
	if match := okhslPattern().FindStringSubmatch(text); match != nil {
		s, l := number(match[2]), number(match[4])
		if match[3] != "" {
			s /= 100
		}
		if match[5] != "" {
			l /= 100
		}
		if s < 0 || s > 1 || l < 0 || l > 1 {
			return "", fmt.Errorf("invalid color value: %s", text)
		}
		return okhslToHex(number(match[1]), s, l), nil
	}
	if !strings.HasPrefix(text, "#") {
		return "", fmt.Errorf("invalid color value: %s", text)
	}
	return text, nil
}
