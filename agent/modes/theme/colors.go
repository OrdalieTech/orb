package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type ColorMode string

const (
	TrueColor ColorMode = "truecolor"
	Color256  ColorMode = "256color"
)

type ColorValue struct {
	String *string
	Index  *int
}

func (value *ColorValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value.String, value.Index = &text, nil
		return nil
	}
	var index int
	if err := json.Unmarshal(data, &index); err == nil {
		if index < 0 || index > 255 {
			return fmt.Errorf("256-color index %d is outside 0-255", index)
		}
		value.String, value.Index = nil, &index
		return nil
	}
	return errors.New("color must be a string or an integer from 0 to 255")
}

func (value ColorValue) resolve(variables map[string]ColorValue, visited map[string]bool) (resolvedColor, error) {
	if value.Index != nil {
		return resolvedColor{index: value.Index}, nil
	}
	if value.String == nil {
		return resolvedColor{}, errors.New("empty color value")
	}
	text := *value.String
	if text == "" || strings.HasPrefix(text, "#") {
		return resolvedColor{text: &text}, nil
	}
	if visited[text] {
		return resolvedColor{}, fmt.Errorf("circular variable reference detected: %s", text)
	}
	next, ok := variables[text]
	if !ok {
		return resolvedColor{}, fmt.Errorf("variable reference not found: %s", text)
	}
	visited[text] = true
	return next.resolve(variables, visited)
}

type resolvedColor struct {
	text  *string
	index *int
}

func (color resolvedColor) foreground(mode ColorMode) (string, error) {
	if color.index != nil {
		return fmt.Sprintf("\x1b[38;5;%dm", *color.index), nil
	}
	if color.text == nil {
		return "", errors.New("empty color")
	}
	if *color.text == "" {
		return "\x1b[39m", nil
	}
	r, g, b, err := parseHex(*color.text)
	if err != nil {
		return "", err
	}
	if mode == TrueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b), nil
	}
	return fmt.Sprintf("\x1b[38;5;%dm", rgbTo256(r, g, b)), nil
}

func (color resolvedColor) background(mode ColorMode) (string, error) {
	if color.index != nil {
		return fmt.Sprintf("\x1b[48;5;%dm", *color.index), nil
	}
	if color.text == nil {
		return "", errors.New("empty color")
	}
	if *color.text == "" {
		return "\x1b[49m", nil
	}
	r, g, b, err := parseHex(*color.text)
	if err != nil {
		return "", err
	}
	if mode == TrueColor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b), nil
	}
	return fmt.Sprintf("\x1b[48;5;%dm", rgbTo256(r, g, b)), nil
}

func (color resolvedColor) hex(defaultColor string) (string, error) {
	if color.index != nil {
		return ansi256ToHex(*color.index), nil
	}
	if color.text == nil || *color.text == "" {
		return defaultColor, nil
	}
	_, _, _, err := parseHex(*color.text)
	return *color.text, err
}

func parseHex(value string) (int, int, int, error) {
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", value)
	}
	parsed, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", value)
	}
	return int(parsed >> 16), int((parsed >> 8) & 255), int(parsed & 255), nil
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

func ansi256ToHex(index int) string {
	basic := [...]string{"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}
	if index < 16 {
		return basic[index]
	}
	if index < 232 {
		cube := index - 16
		component := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		return fmt.Sprintf("#%02x%02x%02x", component(cube/36), component((cube%36)/6), component(cube%6))
	}
	gray := 8 + (index-232)*10
	return fmt.Sprintf("#%02x%02x%02x", gray, gray, gray)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
