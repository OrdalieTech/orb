package main

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/internal/localecompare"
)

type modelListRow struct{ provider, model, context, maxOut, thinking, images string }
type modelListWidths struct{ provider, model, context, maxOut, thinking, images int }

func formatModelList(models []ai.Model, search string) string {
	if search != "" {
		models = slices.DeleteFunc(append([]ai.Model(nil), models...), func(model ai.Model) bool {
			return !fuzzyModelMatch(search, string(model.Provider)+" "+model.ID)
		})
	}
	if len(models) == 0 {
		if search != "" {
			return fmt.Sprintf("No models matching %q\n", search)
		}
		return codingagent.FormatNoModelsAvailableMessage() + "\n"
	}
	collator := localecompare.New()
	slices.SortFunc(models, func(left, right ai.Model) int {
		if compared := collator.CompareString(string(left.Provider), string(right.Provider)); compared != 0 {
			return compared
		}
		return collator.CompareString(left.ID, right.ID)
	})
	rows := make([]modelListRow, 0, len(models))
	widths := modelListWidths{provider: len("provider"), model: len("model"), context: len("context"), maxOut: len("max-out"), thinking: len("thinking"), images: len("images")}
	for _, model := range models {
		entry := modelListRow{provider: string(model.Provider), model: model.ID, context: formatTokenCount(model.ContextWindow), maxOut: formatTokenCount(model.MaxTokens), thinking: yesNo(model.Reasoning), images: yesNo(slices.Contains(model.Input, ai.InputImage))}
		rows = append(rows, entry)
		widths.provider = max(widths.provider, len(entry.provider))
		widths.model = max(widths.model, len(entry.model))
		widths.context = max(widths.context, len(entry.context))
		widths.maxOut = max(widths.maxOut, len(entry.maxOut))
		widths.thinking = max(widths.thinking, len(entry.thinking))
		widths.images = max(widths.images, len(entry.images))
	}
	var output strings.Builder
	writeModelRow(&output, modelListRow{"provider", "model", "context", "max-out", "thinking", "images"}, widths)
	for _, entry := range rows {
		writeModelRow(&output, entry, widths)
	}
	return output.String()
}

func writeModelRow(output *strings.Builder, value modelListRow, widths modelListWidths) {
	_, _ = fmt.Fprintf(output, "%-*s  %-*s  %-*s  %-*s  %-*s  %-*s\n", widths.provider, value.provider, widths.model, value.model, widths.context, value.context, widths.maxOut, value.maxOut, widths.thinking, value.thinking, widths.images, value.images)
}

func formatTokenCount(count float64) string {
	if count >= 1_000_000 {
		millions := count / 1_000_000
		if math.Trunc(millions) == millions {
			return strconv.FormatFloat(millions, 'f', -1, 64) + "M"
		}
		return jsToFixedOne(millions) + "M"
	}
	if count >= 1_000 {
		thousands := count / 1_000
		if math.Trunc(thousands) == thousands {
			return strconv.FormatFloat(thousands, 'f', -1, 64) + "K"
		}
		return jsToFixedOne(thousands) + "K"
	}
	if count == 0 {
		return "0"
	}
	return strconv.FormatFloat(count, 'f', -1, 64)
}

func jsToFixedOne(value float64) string {
	negative := math.Signbit(value)
	if negative {
		value = -value
	}
	scaled := new(big.Rat).SetFloat64(value)
	scaled.Mul(scaled, big.NewRat(10, 1))
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(scaled.Denom()) >= 0 {
		integer.Add(integer, big.NewInt(1))
	}
	digits := integer.String()
	if len(digits) == 1 {
		digits = "0" + digits
	}
	result := digits[:len(digits)-1] + "." + digits[len(digits)-1:]
	if negative {
		return "-" + result
	}
	return result
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func fuzzyModelMatch(query, text string) bool {
	text = strings.ToLower(text)
	for _, token := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(query)), func(character rune) bool {
		return character == '/' || unicode.IsSpace(character)
	}) {
		if !subsequence(token, text) {
			swapped := swapAlphaNumeric(token)
			if swapped == "" || !subsequence(swapped, text) {
				return false
			}
		}
	}
	return true
}

func subsequence(query, text string) bool {
	index := 0
	characters := []rune(query)
	for _, character := range text {
		if index < len(characters) && character == characters[index] {
			index++
		}
	}
	return index == len(characters)
}

func swapAlphaNumeric(value string) string {
	if swapped := swapAlphaNumericParts(value, true); swapped != "" {
		return swapped
	}
	return swapAlphaNumericParts(value, false)
}

func swapAlphaNumericParts(value string, lettersFirst bool) string {
	index := 0
	for index < len(value) && isASCIIAlphaNumericPart(value[index], lettersFirst) {
		index++
	}
	if index == 0 || index == len(value) {
		return ""
	}
	for other := index; other < len(value); other++ {
		if !isASCIIAlphaNumericPart(value[other], !lettersFirst) {
			return ""
		}
	}
	return value[index:] + value[:index]
}

func isASCIIAlphaNumericPart(character byte, letters bool) bool {
	if letters {
		return character >= 'a' && character <= 'z'
	}
	return character >= '0' && character <= '9'
}
