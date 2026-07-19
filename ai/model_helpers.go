package ai

import "slices"

// Public model helpers mirroring packages/ai/src/models.ts exports.

// HasAPI reports whether the model streams through the given API shape.
func HasAPI(model *Model, api API) bool {
	return model != nil && model.API == api
}

// CalculateCost fills usage.Cost from the model's rates, applying tiered
// pricing above each tier's input-token threshold and Anthropic's 2x base
// input rate for 1h cache writes.
func CalculateCost(model *Model, usage *Usage) {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matchedThreshold := float64(-1)
	if model.Cost.Tiers != nil {
		for _, tier := range *model.Cost.Tiers {
			if float64(inputTokens) > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
				rates = tier.ModelCostRates
				matchedThreshold = tier.InputTokensAbove
			}
		}
	}
	longWrite := int64(0)
	if usage.CacheWrite1h != nil {
		longWrite = *usage.CacheWrite1h
	}
	shortWrite := usage.CacheWrite - longWrite
	usage.Cost.Input = rates.Input / 1_000_000 * float64(usage.Input)
	usage.Cost.Output = rates.Output / 1_000_000 * float64(usage.Output)
	usage.Cost.CacheRead = rates.CacheRead / 1_000_000 * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (rates.CacheWrite*float64(shortWrite) + rates.Input*2*float64(longWrite)) / 1_000_000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

var extendedThinkingLevels = []ModelThinkingLevel{
	ModelThinkingOff, ModelThinkingMinimal, ModelThinkingLow, ModelThinkingMedium,
	ModelThinkingHigh, ModelThinkingXHigh, ModelThinkingMax,
}

// SupportedThinkingLevels lists the model's usable thinking levels: a mapped
// null removes a level, and xhigh/max require an explicit mapping. A nil
// model reports every level (Go seam tolerance).
func SupportedThinkingLevels(model *Model) []ModelThinkingLevel {
	if model == nil {
		return slices.Clone(extendedThinkingLevels)
	}
	if !model.Reasoning {
		return []ModelThinkingLevel{ModelThinkingOff}
	}
	result := make([]ModelThinkingLevel, 0, len(extendedThinkingLevels))
	for _, level := range extendedThinkingLevels {
		present := false
		var value *string
		if model.ThinkingLevelMap != nil {
			value, present = (*model.ThinkingLevelMap)[level]
		}
		if present && value == nil {
			continue
		}
		if (level == ModelThinkingXHigh || level == ModelThinkingMax) && !present {
			continue
		}
		result = append(result, level)
	}
	return result
}

// ClampThinkingLevel returns the requested level when supported, otherwise
// the nearest supported level upward first, then downward.
func ClampThinkingLevel(model *Model, requested ModelThinkingLevel) ModelThinkingLevel {
	levels := SupportedThinkingLevels(model)
	if slices.Contains(levels, requested) {
		return requested
	}
	index := slices.Index(extendedThinkingLevels, requested)
	if index < 0 {
		if len(levels) == 0 {
			return ModelThinkingOff
		}
		return levels[0]
	}
	for candidate := index; candidate < len(extendedThinkingLevels); candidate++ {
		if slices.Contains(levels, extendedThinkingLevels[candidate]) {
			return extendedThinkingLevels[candidate]
		}
	}
	for candidate := index - 1; candidate >= 0; candidate-- {
		if slices.Contains(levels, extendedThinkingLevels[candidate]) {
			return extendedThinkingLevels[candidate]
		}
	}
	if len(levels) == 0 {
		return ModelThinkingOff
	}
	return levels[0]
}

// ModelsAreEqual compares models by id and provider; nil never equals.
func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}
