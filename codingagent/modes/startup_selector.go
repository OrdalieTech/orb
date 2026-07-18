package modes

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/OrdalieTech/pi-go/tui"
)

type StartupChoice struct {
	Label  string
	Value  string
	Cancel bool
}

type StartupSelectorOptions struct {
	Title      string
	Choices    []StartupChoice
	MaxVisible int
}

func RunStartupSelector(ctx context.Context, options StartupSelectorOptions) (string, bool, error) {
	return RunStartupSelectorWithTerminal(ctx, options, tui.NewProcessTerminal())
}

func RunStartupSelectorWithTerminal(ctx context.Context, options StartupSelectorOptions, terminal tui.Terminal) (string, bool, error) {
	if terminal == nil {
		return "", false, errors.New("startup selector requires a terminal")
	}
	if len(options.Choices) == 0 {
		return "", false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	maxVisible := options.MaxVisible
	if maxVisible <= 0 {
		maxVisible = 10
	}

	items := make([]tui.SelectItem, len(options.Choices))
	for index, choice := range options.Choices {
		items[index] = tui.SelectItem{Value: strconv.Itoa(index), Label: choice.Label}
	}
	list := tui.NewSelectList(items, maxVisible, selectListTheme(), tui.SelectListLayoutOptions{})
	type result struct {
		value     string
		cancelled bool
	}
	resolved := make(chan result, 1)
	var resolveOnce sync.Once
	resolve := func(value result) { resolveOnce.Do(func() { resolved <- value }) }
	list.OnSelect = func(item tui.SelectItem) {
		index, err := strconv.Atoi(item.Value)
		if err != nil || index < 0 || index >= len(options.Choices) {
			resolve(result{cancelled: true})
			return
		}
		choice := options.Choices[index]
		resolve(result{value: choice.Value, cancelled: choice.Cancel})
	}
	list.OnCancel = func() { resolve(result{cancelled: true}) }

	container := &tui.Container{}
	container.AddChild(tui.NewText(options.Title, 1, 0, nil))
	container.AddChild(list)
	uiApp := tui.NewTUI(terminal)
	uiApp.AddChild(container)
	uiApp.SetFocus(list)
	if err := uiApp.Start(); err != nil {
		return "", false, err
	}

	var selected result
	var waitErr error
	select {
	case selected = <-resolved:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	stopErr := uiApp.Stop()
	if err := errors.Join(waitErr, stopErr); err != nil {
		return "", false, err
	}
	return selected.value, !selected.cancelled, nil
}
