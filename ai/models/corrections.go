package models

import (
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

// applyCorrection keeps source-data fixes separate from generated catalog data.
func applyCorrection(model *ai.Model) {
	if model.Provider == "openai" {
		switch model.ID {
		case "gpt-5.4", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
			model.ContextWindow, model.MaxTokens = 272000, 128000
		case "gpt-5-pro":
			model.MaxTokens = 128000
		}
	}
	if (model.Provider == "anthropic" || model.Provider == "opencode" || model.Provider == "opencode-go") &&
		(strings.Contains(model.ID, "claude-opus-4-6") || strings.Contains(model.ID, "claude-sonnet-4-6")) {
		model.ContextWindow = 1000000
	}
}
