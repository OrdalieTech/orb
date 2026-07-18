package pirate

import (
	"context"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

const PromptSuffix = `

IMPORTANT: You are now in PIRATE MODE. You must:
- Speak like a stereotypical pirate in all responses
- Use phrases like "Arrr!", "Ahoy!", "Shiver me timbers!", "Avast!", "Ye scurvy dog!"
- Replace "my" with "me", "you" with "ye", "your" with "yer"
- Refer to the user as "matey" or "landlubber"
- End sentences with nautical expressions
- Still complete the actual task correctly, just in pirate speak
`

func Extension(api extensions.API) error {
	var mu sync.RWMutex
	enabled := false
	api.RegisterCommand("pirate", extensions.Command{
		Description: "Toggle pirate mode (agent speaks like a pirate)",
		Handler: func(_ context.Context, _ string, ctx extensions.CommandContext) error {
			mu.Lock()
			enabled = !enabled
			active := enabled
			mu.Unlock()
			message := "Pirate mode disabled"
			if active {
				message = "Arrr! Pirate mode enabled!"
			}
			ctx.UI().Notify(message, extensions.NotifyInfo)
			return nil
		},
	})
	api.On(extensions.EventBeforeAgentStart, func(
		_ context.Context,
		raw extensions.Event,
		_ extensions.Context,
	) (any, error) {
		mu.RLock()
		active := enabled
		mu.RUnlock()
		if !active {
			return nil, nil
		}
		event := raw.(extensions.BeforeAgentStartEvent)
		prompt := event.SystemPrompt + PromptSuffix
		return extensions.BeforeAgentStartResult{SystemPrompt: &prompt}, nil
	})
	return nil
}
