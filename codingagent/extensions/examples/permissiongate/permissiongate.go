package permissiongate

import (
	"context"
	"fmt"
	"regexp"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+(-rf?|--recursive)`),
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)\b(chmod|chown)\b.*777`),
}

func Extension(api extensions.API) error {
	api.On(extensions.EventToolCall, func(
		ctx context.Context,
		raw extensions.Event,
		extensionContext extensions.Context,
	) (any, error) {
		event, ok := raw.(extensions.ToolCallEvent)
		if !ok || event.ToolName != "bash" {
			return nil, nil
		}
		command, _ := event.Input["command"].(string)
		if !dangerous(command) {
			return nil, nil
		}
		if !extensionContext.HasUI() {
			return extensions.ToolCallResult{
				Block: true, Reason: "Dangerous command blocked (no UI for confirmation)",
			}, nil
		}
		choice, selected, err := extensionContext.UI().Select(
			ctx,
			fmt.Sprintf("⚠️ Dangerous command:\n\n  %s\n\nAllow?", command),
			[]string{"Yes", "No"},
			nil,
		)
		if err != nil {
			return nil, err
		}
		if !selected || choice != "Yes" {
			return extensions.ToolCallResult{Block: true, Reason: "Blocked by user"}, nil
		}
		return nil, nil
	})
	return nil
}

func dangerous(command string) bool {
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}
