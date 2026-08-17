package modes

import (
	"context"
	"testing"
)

type startupSequenceTerminal struct {
	*configLifecycleTerminal
	inputs []string
}

func (terminal *startupSequenceTerminal) Start(onInput func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.starts++
	err := terminal.startErr
	terminal.mu.Unlock()
	if err != nil {
		return err
	}
	for _, input := range terminal.inputs {
		onInput(input)
	}
	return nil
}

func TestRunStartupSelectorUsesTUIChoiceAndCancellation(t *testing.T) {
	options := StartupSelectorOptions{
		Title: "continue in current cwd",
		Choices: []StartupChoice{
			{Label: "Continue", Value: "/current"},
			{Label: "Cancel", Cancel: true},
		},
	}
	terminal := &configLifecycleTerminal{input: "\r", columns: 80, rows: 24}
	selected, ok, err := RunStartupSelectorWithTerminal(context.Background(), options, terminal)
	if err != nil || !ok || selected != "/current" {
		t.Fatalf("selected=%q ok=%t err=%v", selected, ok, err)
	}

	terminal = &configLifecycleTerminal{input: "\x1b", columns: 80, rows: 24}
	selected, ok, err = RunStartupSelectorWithTerminal(context.Background(), options, terminal)
	if err != nil || ok || selected != "" {
		t.Fatalf("cancel selected=%q ok=%t err=%v", selected, ok, err)
	}

	sequenceTerminal := &startupSequenceTerminal{
		configLifecycleTerminal: &configLifecycleTerminal{columns: 80, rows: 24},
		inputs:                  []string{"\x1b[B", "\r"},
	}
	selected, ok, err = RunStartupSelectorWithTerminal(context.Background(), options, sequenceTerminal)
	if err != nil || ok || selected != "" {
		t.Fatalf("cancel choice selected=%q ok=%t err=%v", selected, ok, err)
	}
}
