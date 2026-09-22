package theme

import (
	"fmt"
	"math"

	"github.com/OrdalieTech/orb/internal/themefile"
)

type ColorMode string

const (
	TrueColor ColorMode = "truecolor"
	Color256  ColorMode = "256color"
)

// ansiForeground renders a resolved color as an ANSI foreground sequence.
func ansiForeground(color themefile.Color, mode ColorMode) string {
	if color.Index != nil {
		return fmt.Sprintf("\x1b[38;5;%dm", *color.Index)
	}
	if color.Text == "" {
		return "\x1b[39m"
	}
	r, g, b, _ := themefile.ParseHex(color.Text)
	if mode == TrueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", rgbTo256(r, g, b))
}

// ansiBackground renders a resolved color as an ANSI background sequence.
func ansiBackground(color themefile.Color, mode ColorMode) string {
	if color.Index != nil {
		return fmt.Sprintf("\x1b[48;5;%dm", *color.Index)
	}
	if color.Text == "" {
		return "\x1b[49m"
	}
	r, g, b, _ := themefile.ParseHex(color.Text)
	if mode == TrueColor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	}
	return fmt.Sprintf("\x1b[48;5;%dm", rgbTo256(r, g, b))
}

var cubeValues = [...]int{0, 95, 135, 175, 215, 255}

func closestIndex(value int, values []int) int {
	best, distance := 0, math.MaxInt
	for index, candidate := range values {
		if delta := abs(value - candidate); delta < distance {
			best, distance = index, delta
		}
	}
	return best
}

func rgbTo256(r, g, b int) int {
	ri := closestIndex(r, cubeValues[:])
	gi := closestIndex(g, cubeValues[:])
	bi := closestIndex(b, cubeValues[:])
	cubeR, cubeG, cubeB := cubeValues[ri], cubeValues[gi], cubeValues[bi]
	cube := 16 + 36*ri + 6*gi + bi
	cubeDistance := colorDistance(r, g, b, cubeR, cubeG, cubeB)

	gray := int(math.Round(.299*float64(r) + .587*float64(g) + .114*float64(b)))
	grayValues := make([]int, 24)
	for index := range grayValues {
		grayValues[index] = 8 + index*10
	}
	grayIndex := closestIndex(gray, grayValues)
	grayValue := grayValues[grayIndex]
	if max(r, g, b)-min(r, g, b) < 10 && colorDistance(r, g, b, grayValue, grayValue, grayValue) < cubeDistance {
		return 232 + grayIndex
	}
	return cube
}

func colorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr, dg, db := float64(r1-r2), float64(g1-g2), float64(b1-b2)
	return dr*dr*.299 + dg*dg*.587 + db*db*.114
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
