package statusline

import (
	"context"
	"fmt"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

func Extension(api extensions.API) error {
	var mu sync.Mutex
	turnCount := 0
	api.On(extensions.EventSessionStart, func(
		_ context.Context,
		_ extensions.Event,
		ctx extensions.Context,
	) (any, error) {
		status := ctx.UI().Theme().FG("dim", "Ready")
		ctx.UI().SetStatus("status-demo", &status)
		return nil, nil
	})
	api.On(extensions.EventTurnStart, func(
		_ context.Context,
		_ extensions.Event,
		ctx extensions.Context,
	) (any, error) {
		mu.Lock()
		turnCount++
		current := turnCount
		mu.Unlock()
		theme := ctx.UI().Theme()
		status := theme.FG("accent", "●") + theme.FG("dim", fmt.Sprintf(" Turn %d...", current))
		ctx.UI().SetStatus("status-demo", &status)
		return nil, nil
	})
	api.On(extensions.EventTurnEnd, func(
		_ context.Context,
		_ extensions.Event,
		ctx extensions.Context,
	) (any, error) {
		mu.Lock()
		current := turnCount
		mu.Unlock()
		theme := ctx.UI().Theme()
		status := theme.FG("success", "✓") + theme.FG("dim", fmt.Sprintf(" Turn %d complete", current))
		ctx.UI().SetStatus("status-demo", &status)
		return nil, nil
	})
	return nil
}
