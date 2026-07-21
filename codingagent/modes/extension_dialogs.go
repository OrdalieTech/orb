package modes

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

type extensionDialogOptions struct {
	ui                    tui.RenderRequester
	timeout               *int64
	onToggleToolsExpanded func()
}

// ExtensionSelectorComponent is the bordered option-list dialog behind
// ctx.ui.select, exported for extension UI reuse as upstream exports it.
type ExtensionSelectorComponent struct {
	container             *tui.Container
	list                  *tui.Container
	title                 *tui.Text
	baseTitle             string
	options               []tui.SelectItem
	selected              int
	onSelect              func(string)
	onCancel              func()
	onToggleToolsExpanded func()
	countdown             *CountdownTimer
}

// NewExtensionSelectorItemsComponent builds the selector dialog from
// pre-labelled select items.
func NewExtensionSelectorItemsComponent(
	title string,
	options []tui.SelectItem,
	onSelect func(string),
	onCancel func(),
	config *extensionDialogOptions,
) *ExtensionSelectorComponent {
	component := &ExtensionSelectorComponent{
		container: &tui.Container{},
		list:      &tui.Container{},
		baseTitle: title,
		options:   append([]tui.SelectItem(nil), options...),
		onSelect:  onSelect,
		onCancel:  onCancel,
	}
	if config != nil {
		component.onToggleToolsExpanded = config.onToggleToolsExpanded
	}
	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewSpacer(1))
	component.title = tui.NewText(theme.FG("accent", theme.Bold(title)), 1, 0, nil)
	component.container.AddChild(component.title)
	component.container.AddChild(tui.NewSpacer(1))
	if config != nil && config.timeout != nil && *config.timeout > 0 && config.ui != nil {
		component.countdown = NewCountdownTimer(*config.timeout, config.ui, func(seconds int) {
			component.title.SetText(theme.FG("accent", theme.Bold(fmt.Sprintf("%s (%ds)", component.baseTitle, seconds))))
		}, component.cancel)
	}
	component.container.AddChild(component.list)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(
		RawKeyHint("↑↓", "navigate")+"  "+
			KeyHint("tui.select.confirm", "select")+"  "+
			KeyHint("tui.select.cancel", "cancel"),
		1,
		0,
		nil,
	))
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())
	component.updateList()
	return component
}

func (component *ExtensionSelectorComponent) updateList() {
	component.list.Clear()
	for index, option := range component.options {
		label := option.Label
		if label == "" {
			label = option.Value
		}
		value := "  " + theme.FG("text", label)
		if index == component.selected {
			value = theme.FG("accent", "→ ") + theme.FG("accent", label)
		}
		component.list.AddChild(tui.NewText(value, 1, 0, nil))
	}
}

func (component *ExtensionSelectorComponent) HandleInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "app.tools.expand"):
		if component.onToggleToolsExpanded != nil {
			component.onToggleToolsExpanded()
		}
	case bindings.Matches(event.Raw, "tui.select.up") || event.Raw == "k":
		component.selected = max(0, component.selected-1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.down") || event.Raw == "j":
		component.selected = min(len(component.options)-1, component.selected+1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		if component.selected >= 0 && component.selected < len(component.options) && component.onSelect != nil {
			component.onSelect(component.options[component.selected].Value)
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		component.cancel()
	}
}

func (component *ExtensionSelectorComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

func (component *ExtensionSelectorComponent) Dispose() {
	if component.countdown != nil {
		component.countdown.Dispose()
	}
}

func (component *ExtensionSelectorComponent) Invalidate() { component.container.Invalidate() }
func (component *ExtensionSelectorComponent) Render(width int) []string {
	return component.container.Render(width)
}

// ExtensionInputComponent is the bordered single-line input dialog behind
// ctx.ui.input, exported for extension UI reuse.
type ExtensionInputComponent struct {
	container *tui.Container
	input     *tui.Input
	title     *tui.Text
	baseTitle string
	onSubmit  func(string)
	onCancel  func()
	countdown *CountdownTimer
}

// NewExtensionInputComponent builds the single-line input dialog.
func NewExtensionInputComponent(
	title string,
	_ string,
	onSubmit func(string),
	onCancel func(),
	config *extensionDialogOptions,
) *ExtensionInputComponent {
	component := &ExtensionInputComponent{
		container: &tui.Container{},
		input:     tui.NewInput(),
		baseTitle: title,
		onSubmit:  onSubmit,
		onCancel:  onCancel,
	}
	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewSpacer(1))
	component.title = tui.NewText(theme.FG("accent", title), 1, 0, nil)
	component.container.AddChild(component.title)
	component.container.AddChild(tui.NewSpacer(1))
	if config != nil && config.timeout != nil && *config.timeout > 0 && config.ui != nil {
		component.countdown = NewCountdownTimer(*config.timeout, config.ui, func(seconds int) {
			component.title.SetText(theme.FG("accent", fmt.Sprintf("%s (%ds)", component.baseTitle, seconds)))
		}, component.cancel)
	}
	component.container.AddChild(component.input)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(
		KeyHint("tui.select.confirm", "submit")+"  "+KeyHint("tui.select.cancel", "cancel"),
		1,
		0,
		nil,
	))
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())
	return component
}

func (component *ExtensionInputComponent) HandleInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		if component.onSubmit != nil {
			component.onSubmit(component.input.GetValue())
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		component.cancel()
	default:
		component.input.HandleInput(event)
	}
}

func (component *ExtensionInputComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

func (component *ExtensionInputComponent) SetFocused(focused bool) {
	component.input.SetFocused(focused)
}
func (component *ExtensionInputComponent) Dispose() {
	if component.countdown != nil {
		component.countdown.Dispose()
	}
}
func (component *ExtensionInputComponent) Invalidate() { component.container.Invalidate() }
func (component *ExtensionInputComponent) Render(width int) []string {
	return component.container.Render(width)
}

// ExtensionEditorComponent is the bordered multi-line editor dialog behind
// ctx.ui.editor, exported for extension UI reuse.
type ExtensionEditorComponent struct {
	container             *tui.Container
	editor                *tui.Editor
	ui                    *tui.TUI
	bindings              *tui.KeybindingsManager
	externalEditorCommand string
	onCancel              func()
}

// NewExtensionEditorComponent builds the multi-line editor dialog.
func NewExtensionEditorComponent(
	uiInstance *tui.TUI,
	bindings *tui.KeybindingsManager,
	title string,
	prefill string,
	onSubmit func(string),
	onCancel func(),
	externalEditorCommand string,
) *ExtensionEditorComponent {
	if bindings == nil {
		bindings = tui.GetKeybindings()
	}
	component := &ExtensionEditorComponent{
		container:             &tui.Container{},
		ui:                    uiInstance,
		bindings:              bindings,
		externalEditorCommand: externalEditorCommand,
		onCancel:              onCancel,
	}
	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(theme.FG("accent", title), 1, 0, nil))
	component.container.AddChild(tui.NewSpacer(1))
	component.editor = tui.NewEditor(uiInstance, theme.EditorTheme())
	if prefill != "" {
		component.editor.SetText(prefill)
	}
	component.editor.OnSubmit = onSubmit
	component.container.AddChild(component.editor)
	component.container.AddChild(tui.NewSpacer(1))
	hint :=
		KeyHint("tui.select.confirm", "submit") + "  " +
			KeyHint("tui.input.newLine", "newline") + "  " +
			KeyHint("tui.select.cancel", "cancel")
	if component.getExternalEditorCommand() != "" {
		hint += "  " + KeyHint("app.editor.external", "external editor")
	}
	component.container.AddChild(tui.NewText(hint, 1, 0, nil))
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())
	return component
}

func (component *ExtensionEditorComponent) HandleInput(event tui.KeyEvent) {
	if tui.GetKeybindings().Matches(event.Raw, "tui.select.cancel") {
		component.cancel()
		return
	}
	if component.bindings.Matches(event.Raw, "app.editor.external") {
		component.openExternalEditor()
		return
	}
	component.editor.HandleInput(event)
}

func (component *ExtensionEditorComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

func (component *ExtensionEditorComponent) getExternalEditorCommand() string {
	if component.externalEditorCommand != "" {
		return component.externalEditorCommand
	}
	if command := os.Getenv("VISUAL"); command != "" {
		return command
	}
	if command := os.Getenv("EDITOR"); command != "" {
		return command
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "nano"
}

func (component *ExtensionEditorComponent) openExternalEditor() {
	command := component.getExternalEditorCommand()
	if command == "" {
		return
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("pi-extension-editor-%d.md", time.Now().UnixMilli()))
	if err := os.WriteFile(path, []byte(component.editor.GetText()), 0o666); err != nil {
		component.finishExternalEditor(path)
		return
	}

	_ = component.ui.Stop()
	parts := strings.Split(command, " ")
	arguments := append(parts[1:], path)
	var process *exec.Cmd
	if runtime.GOOS == "windows" {
		process = exec.Command("cmd", "/C", command+` "`+path+`"`)
	} else {
		process = exec.Command(parts[0], arguments...)
	}
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	_, _ = fmt.Fprintf(os.Stdout, "Launching external editor: %s\npigo will resume when the editor exits.\n", command)
	if err := process.Start(); err != nil {
		component.finishExternalEditor(path)
		return
	}
	go func() {
		if process.Wait() == nil {
			if content, readErr := os.ReadFile(path); readErr == nil {
				component.editor.SetText(strings.TrimSuffix(string(content), "\n"))
			}
		}
		component.finishExternalEditor(path)
	}()
}

func (component *ExtensionEditorComponent) finishExternalEditor(path string) {
	_ = os.Remove(path)
	_ = component.ui.Start()
	component.ui.ForceRender()
}

func (component *ExtensionEditorComponent) SetFocused(focused bool) {
	component.editor.SetFocused(focused)
}
func (component *ExtensionEditorComponent) Invalidate() { component.container.Invalidate() }
func (component *ExtensionEditorComponent) Render(width int) []string {
	return component.container.Render(width)
}

func extensionDialogBorder() *DynamicBorder {
	return NewDynamicBorderWithColor(func(value string) string { return theme.FG("border", value) })
}

// KeyHint renders a dim key label plus muted description for the resolved
// keybinding, mirroring upstream's exported keyHint helper.
func KeyHint(binding, description string) string {
	return theme.FG("dim", KeyText(binding)) + theme.FG("muted", " "+description)
}

// RawKeyHint renders a dim literal key plus muted description without
// keybinding resolution, mirroring upstream's exported rawKeyHint helper.
func RawKeyHint(key, description string) string {
	return theme.FG("dim", formatKeyText(key)) + theme.FG("muted", " "+description)
}
