package main

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/localecompare"
	"github.com/OrdalieTech/orb/tui"
)

type modelListRow struct{ provider, model, context, maxOut, thinking, images string }
type modelListWidths struct{ provider, model, context, maxOut, thinking, images int }

func formatModelList(models []ai.Model, search string) string {
	if search != "" {
		models = tui.FuzzyFilter(models, search, func(model ai.Model) string { return string(model.Provider) + " " + model.ID })
	}
	if len(models) == 0 {
		if search != "" {
			return fmt.Sprintf("No models matching %q\n", search)
		}
		return agent.FormatNoModelsAvailableMessage() + "\n"
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
	for _, unit := range []struct {
		size   float64
		suffix string
	}{{1_000_000, "M"}, {1_000, "K"}} {
		if count < unit.size {
			continue
		}
		scaled := count / unit.size
		if math.Trunc(scaled) == scaled {
			return strconv.FormatFloat(scaled, 'f', -1, 64) + unit.suffix
		}
		return jsToFixedOne(scaled) + unit.suffix
	}
	if count == 0 {
		return "0"
	}
	return strconv.FormatFloat(count, 'f', -1, 64)
}

// jsToFixedOne is JavaScript's value.toFixed(1) for a positive value.
func jsToFixedOne(value float64) string {
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
	return digits[:len(digits)-1] + "." + digits[len(digits)-1:]
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
