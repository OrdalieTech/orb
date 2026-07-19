package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/clipboard"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

// InteractiveModeOptions configures the interactive TUI mode.
type InteractiveModeOptions struct {
	InitialMessage string
	InitialImages  []*ai.ImageContent
	Messages       []string
	SessionHeader  *sessionstore.SessionHeader
	Diagnostics    []string
	Terminal       tui.Terminal
	Host           InteractiveSessionHost
	// StartupVersionCheck is the non-blocking startup seam used by WP-661. The
	// interactive package owns no update transport or policy.
	StartupVersionCheck func(context.Context, extensions.UI)
	Changelog           string
}

type InteractiveMode struct {
	session     *codingagent.SessionRuntime
	ui          *tui.TUI
	keybindings *tui.KeybindingsManager
	editor      *CustomEditor
	mdTheme     tui.MarkdownTheme
	options     InteractiveModeOptions

	// TUI containers
	header          *tui.Container
	chat            *tui.Container
	pendingMessages *tui.Container
	status          *tui.Container
	widgetAbove     *tui.Container
	editorContainer *tui.Container
	widgetBelow     *tui.Container
	footer          *tui.Container
	overlay         *tui.Container

	// Extension UI backing
	interactiveUI *InteractiveUI

	// State
	mu                sync.Mutex
	streaming         bool
	toolsExpanded     bool
	thinkingHidden    bool
	thinkingLabel     string
	bashMode          bool
	shutdownRequested bool
	inputCh           chan inputEntry
	pendingImages     []*ai.ImageContent
	currentStreaming  *AssistantMessageComponent
	toolComponents    map[string]*ToolExecutionComponent
	expandables       []expandableComponent
	statusIndicator   tui.Component
	footerStatuses    map[string]string
	cwd               string
	outputPad         int
	lastEscape        time.Time
	extensionEditor   extensions.EditorComponent
	themeRegistry     *theme.Registry
	themeController   *theme.Controller
	authContext       context.Context
	authCancel        context.CancelFunc

	unsubscribe func()
}

type inputEntry struct {
	text   string
	images []*ai.ImageContent
}

type expandableComponent interface{ SetExpanded(bool) }

// RunInteractiveMode starts the full TUI interactive session.
func RunInteractiveMode(ctx context.Context, session *codingagent.SessionRuntime, options InteractiveModeOptions) int {
	cwd, _ := os.Getwd()
	mode := &InteractiveMode{
		session:        session,
		options:        options,
		inputCh:        make(chan inputEntry, 64),
		toolComponents: make(map[string]*ToolExecutionComponent),
		footerStatuses: make(map[string]string),
		cwd:            cwd,
		outputPad:      1,
	}
	return mode.run(ctx)
}

func (mode *InteractiveMode) run(ctx context.Context) int {
	authContext, authCancel := context.WithCancel(ctx)
	mode.mu.Lock()
	mode.authContext = authContext
	mode.authCancel = authCancel
	mode.mu.Unlock()
	defer authCancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		signal.Stop(signals)
		tools.KillTrackedDetachedChildren()
	}()

	terminal := mode.options.Terminal
	if terminal == nil {
		terminal = tui.NewProcessTerminal()
	}
	mode.ui = tui.NewTUI(terminal)
	settings := mode.session.InteractiveModeSettings()
	userBindings := tui.LoadKeybindingsFile(filepath.Join(settings.AgentDir, "keybindings.json"))
	mode.keybindings = NewAppKeybindings(userBindings)
	tui.SetKeybindings(mode.keybindings)

	mode.init()

	if err := mode.ui.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Error starting TUI:", err)
		return 1
	}
	defer func() {
		_ = mode.ui.Stop()
		if mode.options.Host != nil {
			mode.options.Host.Dispose()
		} else {
			mode.session.Dispose()
		}
	}()

	mode.mu.Lock()
	mode.unsubscribe = mode.session.Subscribe(mode.handleEvent)
	mode.mu.Unlock()
	defer mode.detachSession()
	mode.session.StartExtensions()
	mode.extendExtensionThemes()
	mode.setupAutocomplete()
	versionContext, stopVersionCheck := context.WithCancel(ctx)
	var versionCheck sync.WaitGroup
	if mode.options.StartupVersionCheck != nil {
		versionCheck.Add(1)
		go func() {
			defer versionCheck.Done()
			mode.options.StartupVersionCheck(versionContext, mode.interactiveUI)
		}()
	}
	defer func() {
		stopVersionCheck()
		versionCheck.Wait()
	}()

	// Render initial session entries
	mode.renderInitialMessages()

	// Show startup diagnostics
	for _, diagnostic := range mode.options.Diagnostics {
		mode.chat.AddChild(tui.NewText(theme.FG("warning", "Warning: "+diagnostic), 1, 0, nil))
	}

	// Preserve positional startup message order. The first turn carries decoded
	// startup images; remaining positional messages become subsequent steers.
	go func() {
		if mode.options.InitialMessage != "" {
			mode.inputCh <- inputEntry{text: mode.options.InitialMessage, images: mode.options.InitialImages}
		}
		for _, message := range mode.options.Messages {
			mode.inputCh <- inputEntry{text: message}
		}
	}()

	promptDone := make(chan error, 1)
	prompting := false
	for {
		select {
		case input := <-mode.inputCh:
			mode.mu.Lock()
			shutdown := mode.shutdownRequested
			mode.mu.Unlock()
			if shutdown {
				return 0
			}
			if strings.TrimSpace(input.text) == "" && len(input.images) == 0 {
				continue
			}
			if prompting {
				if err := mode.session.QueueInteractive(ctx, input.text, input.images, extensions.DeliverSteer); err != nil {
					mode.showError(err)
				}
				continue
			}
			prompting = true
			mode.mu.Lock()
			mode.streaming = true
			mode.mu.Unlock()
			mode.setStatus(NewWorkingStatusIndicator(mode.ui, "Working..."))
			go func(entry inputEntry) {
				promptDone <- mode.session.SubmitInteractive(ctx, entry.text, entry.images, extensions.DeliverSteer)
			}(input)
		case err := <-promptDone:
			prompting = false
			mode.mu.Lock()
			mode.streaming = false
			mode.currentStreaming = nil
			mode.mu.Unlock()
			mode.setStatus(&IdleStatus{})
			if err != nil && ctx.Err() == nil {
				mode.showError(err)
			}
		case <-signals:
			mode.session.Abort()
		case <-ctx.Done():
			mode.session.Abort()
			return 0
		}
	}
}

func (mode *InteractiveMode) init() {
	mode.initializeTheme()
	mode.mdTheme = theme.MarkdownTheme()
	mode.header = &tui.Container{}
	mode.chat = &tui.Container{}
	mode.pendingMessages = &tui.Container{}
	mode.status = &tui.Container{}
	mode.widgetAbove = &tui.Container{}
	mode.editorContainer = &tui.Container{}
	mode.widgetBelow = &tui.Container{}
	mode.footer = &tui.Container{}
	mode.overlay = &tui.Container{}

	mode.statusIndicator = &IdleStatus{}
	mode.status.AddChild(mode.statusIndicator)

	editorTheme := theme.EditorTheme()
	mode.editor = NewCustomEditor(mode.ui, editorTheme, mode.keybindings)
	settings := mode.session.InteractiveSettings()
	mode.editor.SetPaddingX(settings.EditorPaddingX)
	mode.editor.SetAutocompleteMaxVisible(settings.AutocompleteMaxVisible)

	// UI tree assembly: header → chat → pendingMessages → status → widgetAbove → editor → widgetBelow
	mode.ui.AddChild(mode.header)
	mode.ui.AddChild(mode.chat)
	mode.ui.AddChild(mode.pendingMessages)
	mode.ui.AddChild(mode.status)
	mode.ui.AddChild(mode.widgetAbove)
	mode.ui.AddChild(mode.editorContainer)
	mode.ui.AddChild(mode.widgetBelow)
	mode.ui.AddChild(mode.footer)
	mode.ui.AddChild(mode.overlay)

	mode.editorContainer.AddChild(mode.editor)

	mode.addDefaultHeader()
	mode.footer.AddChild(NewFooterComponent(mode.session, mode))

	mode.interactiveUI = NewInteractiveUI(mode)
	if runner := mode.session.ExtensionRunner(); runner != nil {
		runner.SetUI(mode.interactiveUI, extensions.ModeTUI)
	}
	if mode.options.Host != nil {
		mode.options.Host.SetBeforeSessionInvalidate(mode.detachSession)
		mode.options.Host.SetRebindSession(mode.rebindHostSession)
	}

	mode.setupAutocomplete()
	mode.setupKeyHandlers()
	mode.setupEditorSubmitHandler()

	mode.ui.SetFocus(mode.editor)
}

func (mode *InteractiveMode) detachSession() {
	mode.mu.Lock()
	unsubscribe := mode.unsubscribe
	mode.unsubscribe = nil
	mode.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (mode *InteractiveMode) rebindHostSession(replacement *codingagent.SessionRuntime) error {
	if replacement == nil {
		return errors.New("session host returned a nil replacement runtime")
	}
	mode.session = replacement
	mode.cwd = replacement.Manager().GetCWD()
	mode.options.SessionHeader = replacement.Manager().GetHeader()
	mode.header.Clear()
	mode.chat.Clear()
	mode.pendingMessages.Clear()
	mode.status.Clear()
	mode.widgetAbove.Clear()
	mode.editorContainer.Clear()
	mode.widgetBelow.Clear()
	mode.footer.Clear()
	mode.overlay.Clear()
	mode.extensionEditor = nil
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.addDefaultHeader()
	mode.restoreEditorComponent()
	mode.footer.AddChild(NewFooterComponent(mode.session, mode))
	if runner := replacement.ExtensionRunner(); runner != nil {
		runner.SetUI(mode.interactiveUI, extensions.ModeTUI)
	}
	mode.initializeTheme()
	mode.mdTheme = theme.MarkdownTheme()
	mode.setupAutocomplete()
	settings := replacement.InteractiveSettings()
	mode.editor.SetPaddingX(settings.EditorPaddingX)
	mode.editor.SetAutocompleteMaxVisible(settings.AutocompleteMaxVisible)
	mode.mu.Lock()
	mode.unsubscribe = replacement.Subscribe(mode.handleEvent)
	mode.mu.Unlock()
	mode.renderInitialMessages()
	mode.ui.SetFocus(mode.activeEditorFocus())
	mode.ui.Terminal().SetTitle("pi - " + filepath.Base(mode.cwd))
	mode.ui.RequestRender()
	return nil
}

func (mode *InteractiveMode) initializeTheme() {
	settings := mode.session.InteractiveModeSettings()
	options := theme.LoadOptions{
		CWD: mode.cwd, AgentDir: settings.AgentDir,
		ProjectTrusted: settings.ProjectTrusted,
		GlobalPaths:    settings.GlobalThemePaths,
		ProjectPaths:   settings.ProjectThemePaths,
	}
	mode.mu.Lock()
	mode.outputPad = settings.OutputPad
	mode.thinkingHidden = settings.HideThinkingBlock
	mode.mu.Unlock()
	mode.ui.SetClearOnShrink(settings.ClearOnShrink)
	mode.ui.SetShowHardwareCursor(settings.ShowHardwareCursor)
	mode.themeRegistry = theme.Load(options)
	mode.themeController = theme.Initialize(mode.themeRegistry, settings.ThemeSetting, theme.DetectBackground(nil).Theme, func() {
		if mode.ui != nil {
			mode.ui.Invalidate()
		}
	})
}

func (mode *InteractiveMode) extendExtensionThemes() {
	if mode.themeRegistry == nil {
		return
	}
	resources := mode.session.ExtensionResources()
	paths := make([]string, 0, len(resources.ThemePaths))
	for _, entry := range resources.ThemePaths {
		paths = append(paths, entry.Path)
	}
	mode.themeRegistry.Extend(paths)
}

func (mode *InteractiveMode) applyTheme() {
	mode.mdTheme = theme.MarkdownTheme()
	mode.ui.Invalidate()
	mode.renderInitialMessages()
}

func (mode *InteractiveMode) addDefaultHeader() {
	if mode.session.InteractiveSettings().QuietStartup {
		return
	}
	if mode.options.SessionHeader != nil {
		mode.header.AddChild(tui.NewText(theme.FG("muted", fmt.Sprintf("pi  %s", mode.options.SessionHeader.CWD)), 1, 0, nil))
	}
}

func (mode *InteractiveMode) Width() int {
	if mode.ui == nil {
		return 0
	}
	return mode.ui.Terminal().Columns()
}
func (mode *InteractiveMode) Height() int {
	if mode.ui == nil {
		return 0
	}
	return mode.ui.Terminal().Rows()
}
func (mode *InteractiveMode) Invalidate() {
	if mode.ui != nil {
		mode.ui.Invalidate()
	}
}

func (mode *InteractiveMode) installEditorFactory(factory extensions.EditorFactory) {
	mode.extensionEditor = nil
	if factory != nil {
		mode.extensionEditor = factory(mode, themeAdapter{value: theme.Current()}, extensionKeybindings{mode.keybindings})
	}
	mode.restoreEditorComponent()
	mode.ui.SetFocus(mode.activeEditorFocus())
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) restoreEditorComponent() {
	mode.editorContainer.Clear()
	if mode.extensionEditor != nil {
		mode.editorContainer.AddChild(mode.extensionEditor)
	} else {
		mode.editorContainer.AddChild(mode.editor)
	}
}

func (mode *InteractiveMode) activeEditorFocus() tui.Component {
	if mode.extensionEditor != nil {
		return extensionEditorAdapter{EditorComponent: mode.extensionEditor}
	}
	return mode.editor
}

func (mode *InteractiveMode) setupAutocomplete() {
	var commands []tui.SlashCommand
	for _, cmd := range codingagent.BuiltinSlashCommands {
		commands = append(commands, tui.SlashCommand{
			Name:         cmd.Name,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
		})
	}
	fdPath, _ := exec.LookPath("fd")
	var provider tui.AutocompleteProvider = tui.NewCombinedAutocompleteProvider(commands, mode.cwd, fdPath)
	if mode.interactiveUI != nil {
		mode.interactiveUI.mu.Lock()
		factories := append([]extensions.AutocompleteProviderFactory(nil), mode.interactiveUI.acProviders...)
		mode.interactiveUI.mu.Unlock()
		wrapped := extensions.AutocompleteProvider(tuiAutocompleteAdapter{provider: provider})
		for _, factory := range factories {
			if factory != nil {
				wrapped = factory(wrapped)
			}
		}
		provider = extensionAutocompleteAdapter{provider: wrapped}
	}
	mode.editor.SetAutocompleteProvider(provider)
}

func (mode *InteractiveMode) setupKeyHandlers() {
	mode.editor.OnEscape = func() {
		mode.mu.Lock()
		streaming := mode.streaming
		mode.mu.Unlock()
		if streaming {
			mode.session.Abort()
			return
		}
		if mode.bashMode {
			mode.bashMode = false
			mode.editor.SetBorderColor(nil)
			return
		}
		if mode.editor.GetText() != "" {
			mode.editor.SetText("")
			mode.mu.Lock()
			mode.lastEscape = time.Time{}
			mode.mu.Unlock()
			return
		}
		now := time.Now()
		mode.mu.Lock()
		double := !mode.lastEscape.IsZero() && now.Sub(mode.lastEscape) <= 500*time.Millisecond
		mode.lastEscape = now
		mode.mu.Unlock()
		if !double {
			return
		}
		action := mode.session.InteractiveSettings().DoubleEscapeAction
		if action == "" {
			action = "tree"
		}
		switch action {
		case "tree":
			mode.showTreeSelector()
		case "fork":
			mode.showForkSelector()
		}
	}

	mode.editor.OnCtrlD = func() {
		mode.shutdown()
	}

	mode.editor.OnPasteImage = func() {
		go func() {
			image := clipboard.ReadImage()
			if image == nil {
				mode.showStatusMessage("No image found on clipboard")
				return
			}
			processed := tools.ProcessImage(image.Bytes, image.MimeType, nil)
			if !processed.OK {
				mode.showStatusMessage(processed.Message)
				return
			}
			content := &ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType}
			mode.mu.Lock()
			mode.pendingImages = append(mode.pendingImages, content)
			index := len(mode.pendingImages)
			mode.mu.Unlock()
			mode.editor.InsertTextAtCursor(fmt.Sprintf("[image #%d]", index))
			mode.ui.RequestRender()
		}()
	}

	// App action handlers
	mode.editor.OnAction("app.clear", func() {
		mode.editor.SetText("")
	})

	mode.editor.OnAction("app.thinking.cycle", func() {
		level, err := mode.session.CycleThinkingLevel()
		if err != nil {
			mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
		} else if level != nil {
			mode.chat.AddChild(newStyledText("dim", "Thinking: "+string(*level)))
		}
		mode.ui.RequestRender()
	})

	mode.editor.OnAction("app.thinking.toggle", func() {
		mode.mu.Lock()
		mode.thinkingHidden = !mode.thinkingHidden
		hidden := mode.thinkingHidden
		streamingComponent := mode.currentStreaming
		label := mode.thinkingLabel
		mode.mu.Unlock()
		mode.session.SetHideThinkingBlock(hidden)
		if streamingComponent != nil {
			streamingComponent.SetHideThinkingBlock(hidden, label)
		}
		mode.renderInitialMessages()
	})

	mode.editor.OnAction("app.tools.expand", func() {
		mode.mu.Lock()
		mode.toolsExpanded = !mode.toolsExpanded
		expanded := mode.toolsExpanded
		mode.mu.Unlock()
		mode.mu.Lock()
		expandables := append([]expandableComponent(nil), mode.expandables...)
		mode.mu.Unlock()
		for _, component := range expandables {
			component.SetExpanded(expanded)
		}
		mode.ui.RequestRender()
	})

	mode.editor.OnAction("app.message.followUp", func() {
		text := mode.editor.GetText()
		if text == "" {
			return
		}
		mode.editor.SetText("")
		_ = mode.session.FollowUp(text)
	})

	mode.editor.OnAction("app.message.dequeue", func() {
		messages := mode.session.DequeueMessages()
		if len(messages) > 0 {
			mode.editor.SetText(strings.Join(messages, "\n"))
		}
	})

	mode.editor.OnAction("app.model.select", func() { mode.selectModel("") })
	mode.editor.OnAction("app.session.new", mode.startNewSession)
	mode.editor.OnAction("app.session.tree", mode.showTreeSelector)
	mode.editor.OnAction("app.session.fork", mode.showForkSelector)
	mode.editor.OnAction("app.session.resume", mode.resumeSession)
	mode.editor.OnAction("app.editor.external", mode.openExternalEditor)

	mode.editor.OnAction("app.message.copy", func() {
		mode.copyLastMessage()
	})

	mode.editor.OnAction("app.model.cycleForward", func() {
		result, err := mode.session.CycleModel(context.Background())
		if err != nil {
			mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
		} else if result != nil {
			mode.chat.AddChild(newStyledText("dim", fmt.Sprintf("Model: %s/%s (thinking: %s)", result.Model.Provider, result.Model.ID, result.ThinkingLevel)))
		}
		mode.ui.RequestRender()
	})

	mode.editor.OnAction("app.model.cycleBackward", func() {
		result, err := mode.session.CycleModelBackward(context.Background())
		if err != nil {
			mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
		} else if result != nil {
			mode.chat.AddChild(newStyledText("dim", fmt.Sprintf("Model: %s/%s (thinking: %s)", result.Model.Provider, result.Model.ID, result.ThinkingLevel)))
		}
		mode.ui.RequestRender()
	})

	mode.editor.OnAction("app.suspend", func() {
		_ = mode.ui.Stop()
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(syscall.SIGTSTP)
		_ = mode.ui.Start()
	})
}

func (mode *InteractiveMode) setupEditorSubmitHandler() {
	mode.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}

		// Bash mode: !command
		if strings.HasPrefix(text, "!") {
			cmd := strings.TrimPrefix(text, "!")
			excludeContext := false
			if strings.HasPrefix(cmd, "!") {
				cmd = strings.TrimPrefix(cmd, "!")
				excludeContext = true
			}
			cmd = strings.TrimSpace(cmd)
			if cmd != "" {
				mode.bashMode = false
				mode.editor.SetBorderColor(nil)
				mode.editor.SetText("")
				mode.editor.AddToHistory(text)
				mode.executeUserBash(cmd, excludeContext)
				return
			}
		}

		// Slash commands
		if strings.HasPrefix(text, "/") {
			name, args := parseSlashCommand(text)
			if mode.handleSlashCommand(name, args) {
				mode.editor.SetText("")
				mode.editor.AddToHistory(text)
				return
			}
		}

		// Detect bash mode toggle
		if text == "!" {
			mode.bashMode = !mode.bashMode
			if mode.bashMode {
				mode.editor.SetBorderColor(theme.BashModeBorderColor())
			} else {
				mode.editor.SetBorderColor(nil)
			}
			mode.editor.SetText("")
			return
		}

		// Normal message submission
		mode.editor.SetText("")
		mode.editor.AddToHistory(text)

		mode.mu.Lock()
		images := mode.pendingImages
		mode.pendingImages = nil
		mode.mu.Unlock()

		mode.inputCh <- inputEntry{text: text, images: images}
	}
}

func (mode *InteractiveMode) handleSlashCommand(name, args string) bool {
	switch name {
	case "quit":
		mode.shutdown()
		return true
	case "new":
		mode.startNewSession()
		return true
	case "compact":
		go func() {
			_, _ = mode.session.Compact(context.Background(), args)
		}()
		return true
	case "copy":
		mode.copyLastMessage()
		return true
	case "name":
		mode.setSessionName(args)
		return true
	case "hotkeys":
		mode.showHotkeys()
		return true
	case "settings":
		mode.showSettings()
		return true
	case "model":
		mode.selectModel(args)
		return true
	case "scoped-models":
		mode.selectScopedModels()
		return true
	case "export":
		mode.exportSession(args)
		return true
	case "import":
		mode.importSession(args)
		return true
	case "share":
		mode.exportSession("")
		return true
	case "session":
		mode.showSessionInfo()
		return true
	case "changelog":
		text := mode.options.Changelog
		if text == "" {
			text = "No changelog is bundled with this build."
		}
		mode.chat.AddChild(tui.NewMarkdown(text, 1, 0, mode.mdTheme, nil, nil))
		mode.ui.RequestRender()
		return true
	case "fork":
		mode.showForkSelector()
		return true
	case "clone":
		mode.cloneSession()
		return true
	case "tree":
		mode.showTreeSelector()
		return true
	case "trust":
		mode.showTrustSelector()
		return true
	case "login":
		mode.authenticateProvider(args, false)
		return true
	case "logout":
		mode.authenticateProvider(args, true)
		return true
	case "resume":
		mode.resumeSession()
		return true
	case "reload":
		mode.reloadSession()
		return true
	}
	return false
}

func (mode *InteractiveMode) showHotkeys() {
	var lines []string
	lines = append(lines, theme.Bold("Keyboard shortcuts")+"\n")
	for _, def := range AppKeybindingDefinitions {
		keys := mode.keybindings.Keys(def.ID)
		if len(keys) == 0 {
			continue
		}
		keyStr := formatKeyText(string(keys[0]))
		lines = append(lines, fmt.Sprintf("  %s  %s", theme.FG("accent", keyStr), def.Description))
	}
	mode.chat.AddChild(tui.NewText(strings.Join(lines, "\n"), 1, 1, nil))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) executeUserBash(command string, excludeFromContext bool) {
	comp := NewBashExecutionComponent(command, mode.ui, excludeFromContext)
	mode.chat.AddChild(comp)
	mode.ui.RequestRender()

	go func() {
		result, err := mode.session.ExecuteUserBash(
			context.Background(),
			command,
			excludeFromContext,
			func(chunk string) {
				comp.AppendOutput(chunk)
				mode.ui.RequestRender()
			},
		)
		if err != nil {
			exitCode := 1
			comp.SetComplete(&exitCode, false)
		} else {
			comp.SetComplete(result.ExitCode, result.Cancelled)
		}
		mode.ui.RequestRender()
	}()
}

func (mode *InteractiveMode) copyLastMessage() {
	text := mode.session.GetLastAssistantText()
	if text == nil || *text == "" {
		mode.chat.AddChild(newStyledText("dim", "Nothing to copy"))
		mode.ui.RequestRender()
		return
	}
	if err := clipboard.CopyToClipboard(*text); err != nil {
		mode.chat.AddChild(newStyledText("error", "Copy failed: "+err.Error()))
	} else {
		mode.chat.AddChild(newStyledText("dim", "Copied to clipboard"))
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) setSessionName(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		mode.chat.AddChild(newStyledText("dim", "Usage: /name <session name>"))
		mode.ui.RequestRender()
		return
	}
	if err := mode.session.SetSessionName(name); err != nil {
		mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
	} else {
		mode.chat.AddChild(newStyledText("dim", "Session renamed to: "+name))
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) selectModel(args string) {
	args = strings.TrimSpace(args)
	if args != "" {
		for _, model := range mode.session.AvailableModels() {
			if fmt.Sprintf("%s/%s", model.Provider, model.ID) == args {
				if err := mode.session.SetModel(context.Background(), model); err != nil {
					mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
				} else {
					mode.chat.AddChild(newStyledText("dim", fmt.Sprintf("Model set to %s/%s", model.Provider, model.ID)))
				}
				mode.ui.RequestRender()
				return
			}
		}
		mode.chat.AddChild(newStyledText("error", "Unknown model: "+args))
		mode.ui.RequestRender()
		return
	}

	// Show model selector
	models := mode.session.AvailableModels()
	if len(models) == 0 {
		mode.chat.AddChild(newStyledText("dim", "No models available"))
		mode.ui.RequestRender()
		return
	}
	options := make([]string, len(models))
	for i, m := range models {
		options[i] = fmt.Sprintf("%s/%s", m.Provider, m.ID)
	}
	go func() {
		selected, ok, _ := mode.interactiveUI.Select(context.Background(), "Select model", options, nil)
		if !ok {
			return
		}
		for _, model := range mode.session.AvailableModels() {
			if fmt.Sprintf("%s/%s", model.Provider, model.ID) == selected {
				if err := mode.session.SetModel(context.Background(), model); err != nil {
					mode.chat.AddChild(newStyledText("error", "Error: "+err.Error()))
				} else {
					mode.chat.AddChild(newStyledText("dim", "Model: "+selected))
				}
				mode.ui.RequestRender()
				return
			}
		}
	}()
}

func (mode *InteractiveMode) showSettings() {
	settings := mode.session.InteractiveModeSettings()
	boolText := func(value bool) string {
		if value {
			return "true"
		}
		return "false"
	}
	items := []tui.SettingItem{
		{ID: "autocompact", Label: "Auto-compact", Description: "Automatically compact context when it gets too large", CurrentValue: boolText(mode.session.AutoCompactionEnabled()), Values: []string{"true", "false"}},
	}
	if tui.GetCapabilities().Images != "" {
		items = append(items,
			tui.SettingItem{ID: "show-images", Label: "Show images", Description: "Render images inline in terminal", CurrentValue: boolText(settings.ShowImages), Values: []string{"true", "false"}},
			tui.SettingItem{ID: "image-width-cells", Label: "Image width", Description: "Preferred inline image width in terminal cells", CurrentValue: strconv.Itoa(settings.ImageWidthCells), Values: []string{"60", "80", "120"}},
		)
	}
	items = append(items,
		tui.SettingItem{ID: "auto-resize-images", Label: "Auto-resize images", Description: "Resize large images to 2000x2000 max for better model compatibility", CurrentValue: boolText(settings.ImageAutoResize), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "block-images", Label: "Block images", Description: "Prevent images from being sent to LLM providers", CurrentValue: boolText(settings.BlockImages), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "skill-commands", Label: "Skill commands", Description: "Register skills as /skill:name commands", CurrentValue: boolText(settings.EnableSkillCommands), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "show-hardware-cursor", Label: "Show hardware cursor", Description: "Show the terminal cursor while still positioning it for IME support", CurrentValue: boolText(settings.ShowHardwareCursor), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "editor-padding", Label: "Editor padding", Description: "Horizontal padding for input editor (0-3)", CurrentValue: strconv.Itoa(settings.EditorPaddingX), Values: []string{"0", "1", "2", "3"}},
		tui.SettingItem{ID: "output-padding", Label: "Output padding", Description: "Horizontal padding for user messages, assistant messages, and thinking", CurrentValue: strconv.Itoa(settings.OutputPad), Values: []string{"0", "1"}},
		tui.SettingItem{ID: "autocomplete-max-visible", Label: "Autocomplete max items", Description: "Max visible items in autocomplete dropdown (3-20)", CurrentValue: strconv.Itoa(settings.AutocompleteMaxVisible), Values: []string{"3", "5", "7", "10", "15", "20"}},
		tui.SettingItem{ID: "clear-on-shrink", Label: "Clear on shrink", Description: "Clear empty rows when content shrinks (may cause flicker)", CurrentValue: boolText(settings.ClearOnShrink), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "terminal-progress", Label: "Terminal progress", Description: "Show OSC 9;4 progress indicators in the terminal tab bar", CurrentValue: boolText(settings.ShowTerminalProgress), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "steering-mode", Label: "Steering mode", Description: "Enter while streaming queues steering messages. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.", CurrentValue: string(settings.SteeringMode), Values: []string{"one-at-a-time", "all"}},
		tui.SettingItem{ID: "follow-up-mode", Label: "Follow-up mode", Description: "Queue follow-up messages until the agent stops", CurrentValue: string(settings.FollowUpMode), Values: []string{"one-at-a-time", "all"}},
		tui.SettingItem{ID: "transport", Label: "Transport", Description: "Preferred transport for providers that support multiple transports", CurrentValue: string(settings.Transport), Values: []string{"sse", "websocket", "websocket-cached", "auto"}},
		tui.SettingItem{ID: "hide-thinking", Label: "Hide thinking", Description: "Hide thinking blocks in assistant responses", CurrentValue: boolText(settings.HideThinkingBlock), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "cache-miss-notices", Label: "Cache miss notices", Description: "Show transcript notices for significant prompt-cache misses", CurrentValue: boolText(settings.ShowCacheMissNotices), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "quiet-startup", Label: "Quiet startup", Description: "Disable verbose printing at startup", CurrentValue: boolText(settings.QuietStartup), Values: []string{"true", "false"}},
		tui.SettingItem{ID: "default-project-trust", Label: "Default project trust", Description: "Fallback behavior when no extension or saved trust decision decides project trust", CurrentValue: settings.DefaultProjectTrust, Values: []string{"ask", "always", "never"}},
		tui.SettingItem{ID: "double-escape-action", Label: "Double-escape action", Description: "Action when pressing Escape twice with empty editor", CurrentValue: settings.DoubleEscapeAction, Values: []string{"tree", "fork", "none"}},
		tui.SettingItem{ID: "tree-filter-mode", Label: "Tree filter mode", Description: "Default filter when opening /tree", CurrentValue: settings.TreeFilterMode, Values: []string{"default", "no-tools", "user-only", "labeled-only", "all"}},
	)
	thinkingValues := make([]string, 0)
	for _, level := range mode.session.AvailableThinkingLevels() {
		thinkingValues = append(thinkingValues, string(level))
	}
	items = append(items, tui.SettingItem{ID: "thinking", Label: "Thinking level", Description: "Reasoning depth for thinking-capable models", CurrentValue: string(mode.session.State().ThinkingLevel), Values: thinkingValues})
	if mode.themeRegistry != nil {
		themes := mode.themeRegistry.Available()
		items = append(items, tui.SettingItem{ID: "theme", Label: "Theme", Description: "Color theme for the interface", CurrentValue: settings.ThemeSetting, Values: themes})
	}

	closeSelector := func() {
		mode.restoreEditorComponent()
		mode.ui.SetFocus(mode.activeEditorFocus())
		mode.ui.RequestRender()
	}
	list := tui.NewSettingsList(items, 10, settingsListTheme(), func(id, value string) {
		mode.applySetting(id, value)
	}, closeSelector, tui.SettingsListOptions{EnableSearch: true})
	selector := &tui.Container{}
	selector.AddChild(NewDynamicBorder())
	selector.AddChild(list)
	selector.AddChild(NewDynamicBorder())
	mode.editorContainer.Clear()
	mode.editorContainer.AddChild(selector)
	mode.ui.SetFocus(list)
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) applySetting(id, value string) {
	enabled := value == "true"
	integer, _ := strconv.Atoi(value)
	switch id {
	case "autocompact":
		mode.session.SetAutoCompactionEnabled(enabled)
	case "show-images":
		mode.session.SetShowImages(enabled)
		mode.renderInitialMessages()
	case "image-width-cells":
		mode.session.SetImageWidthCells(integer)
		mode.renderInitialMessages()
	case "auto-resize-images":
		mode.session.SetImageAutoResize(enabled)
	case "block-images":
		mode.session.SetBlockImages(enabled)
	case "skill-commands":
		mode.session.SetEnableSkillCommands(enabled)
		mode.setupAutocomplete()
	case "show-hardware-cursor":
		mode.session.SetShowHardwareCursor(enabled)
		mode.ui.SetShowHardwareCursor(enabled)
	case "editor-padding":
		mode.session.SetEditorPaddingX(integer)
		mode.editor.SetPaddingX(integer)
	case "output-padding":
		mode.session.SetOutputPad(integer)
		mode.mu.Lock()
		mode.outputPad = integer
		mode.mu.Unlock()
		mode.renderInitialMessages()
	case "autocomplete-max-visible":
		mode.session.SetAutocompleteMaxVisible(integer)
		mode.editor.SetAutocompleteMaxVisible(integer)
	case "clear-on-shrink":
		mode.session.SetClearOnShrink(enabled)
		mode.ui.SetClearOnShrink(enabled)
	case "terminal-progress":
		mode.session.SetShowTerminalProgress(enabled)
	case "steering-mode":
		mode.session.SetSteeringMode(agent.QueueMode(value))
	case "follow-up-mode":
		mode.session.SetFollowUpMode(agent.QueueMode(value))
	case "transport":
		mode.session.SetTransport(ai.Transport(value))
	case "hide-thinking":
		mode.mu.Lock()
		mode.thinkingHidden = enabled
		mode.mu.Unlock()
		mode.session.SetHideThinkingBlock(enabled)
		mode.renderInitialMessages()
	case "cache-miss-notices":
		mode.session.SetShowCacheMissNotices(enabled)
		mode.renderInitialMessages()
	case "quiet-startup":
		mode.session.SetQuietStartup(enabled)
	case "default-project-trust":
		mode.session.SetDefaultProjectTrust(value)
	case "double-escape-action":
		mode.session.SetDoubleEscapeAction(value)
	case "tree-filter-mode":
		mode.session.SetTreeFilterMode(value)
	case "thinking":
		if err := mode.session.SetThinkingLevel(ai.ModelThinkingLevel(value)); err != nil {
			mode.showError(err)
		}
	case "theme":
		if result := mode.interactiveUI.SetTheme(value); result.Error != "" {
			mode.showError(errors.New(result.Error))
		}
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) exportSession(args string) {
	outputPath := strings.TrimSpace(args)
	path, err := mode.session.ExportHTML(outputPath)
	if err != nil {
		mode.chat.AddChild(newStyledText("error", "Export failed: "+err.Error()))
	} else {
		mode.chat.AddChild(newStyledText("dim", "Exported to: "+path))
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) showForkSelector() {
	messages := mode.session.GetUserMessagesForForking()
	options := make([]string, 0, len(messages))
	ids := make(map[string]string, len(messages))
	for _, message := range messages {
		preview := strings.ReplaceAll(message.Text, "\n", " ")
		if len(preview) > 60 {
			preview = preview[:57] + "..."
		}
		label := preview + "  [" + message.EntryID[:min(8, len(message.EntryID))] + "]"
		options = append(options, label)
		ids[label] = message.EntryID
	}
	if len(options) == 0 {
		mode.chat.AddChild(newStyledText("dim", "No user messages to fork from"))
		mode.ui.RequestRender()
		return
	}
	go func() {
		selected, ok, _ := mode.interactiveUI.Select(context.Background(), "Fork from message", options, nil)
		if !ok {
			return
		}
		if mode.options.Host == nil {
			mode.showError(errors.New("session host is unavailable"))
			return
		}
		result, err := mode.options.Host.Fork(context.Background(), ids[selected], &extensions.ForkOptions{Position: extensions.ForkBefore})
		if err != nil {
			mode.showError(err)
			return
		}
		if result.Cancelled {
			return
		}
		mode.editor.SetText(result.SelectedText)
		mode.showStatusMessage("Forked to new session")
	}()
}

func (mode *InteractiveMode) showTreeSelector() {
	tree := mode.session.Manager().GetTree()
	if len(tree) == 0 {
		mode.showStatusMessage("No entries in session")
		return
	}
	var options []string
	ids := map[string]string{}
	filterMode := mode.session.InteractiveModeSettings().TreeFilterMode
	leaf := mode.session.Manager().GetLeafID()
	var walk func([]*sessionstore.SessionTreeNode, int)
	walk = func(nodes []*sessionstore.SessionTreeNode, depth int) {
		for _, node := range nodes {
			current := leaf != nil && *leaf == node.Entry.ID
			if !treeEntryVisible(node, current, filterMode) {
				walk(node.Children, depth)
				continue
			}
			label := strings.Repeat("  ", depth) + sessionEntryLabel(node.Entry)
			if node.Label != nil && *node.Label != "" {
				label += " [" + *node.Label + "]"
			}
			label += "  {" + node.Entry.ID[:min(8, len(node.Entry.ID))] + "}"
			options = append(options, label)
			ids[label] = node.Entry.ID
			walk(node.Children, depth+1)
		}
	}
	walk(tree, 0)
	go func() {
		selected, ok, err := mode.interactiveUI.Select(context.Background(), "Session tree", options, nil)
		if err != nil || !ok {
			return
		}
		currentLeaf := mode.session.Manager().GetLeafID()
		if currentLeaf != nil && *currentLeaf == ids[selected] {
			mode.showStatusMessage("Already at this point")
			return
		}
		summarize, err := mode.interactiveUI.Confirm(context.Background(), "Summarize branch?", "Create a summary of the abandoned branch?", nil)
		if err != nil {
			mode.showError(err)
			return
		}
		result, err := mode.session.NavigateTree(context.Background(), ids[selected], codingagent.NavigateTreeOptions{Summarize: summarize})
		if err != nil {
			mode.showError(err)
			return
		}
		if result.Cancelled || result.Aborted {
			return
		}
		mode.editor.SetText(result.EditorText)
		mode.renderInitialMessages()
	}()
}

func treeEntryVisible(node *sessionstore.SessionTreeNode, current bool, filterMode string) bool {
	entry := node.Entry
	if entry.Type == "message" {
		role, text := sessionMessageRoleText(entry.Message)
		if role == "assistant" && text == "" && !current {
			var message struct {
				StopReason string `json:"stopReason"`
			}
			_ = json.Unmarshal(entry.Message, &message)
			if message.StopReason == "" || message.StopReason == "stop" || message.StopReason == "toolUse" {
				return false
			}
		}
		if filterMode == "user-only" {
			return role == "user"
		}
		if filterMode == "no-tools" && role == "toolResult" {
			return false
		}
	}
	if filterMode == "labeled-only" {
		return node.Label != nil
	}
	if filterMode == "all" {
		return true
	}
	if filterMode == "user-only" {
		return false
	}
	switch entry.Type {
	case "label", "custom", "model_change", "thinking_level_change", "session_info":
		return false
	default:
		return true
	}
}

func (mode *InteractiveMode) showTrustSelector() {
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	state, err := mode.options.Host.TrustState()
	if err != nil {
		mode.showError(err)
		return
	}
	trustOptions := state.Options
	options := make([]string, len(trustOptions))
	byLabel := make(map[string]config.ProjectTrustOption, len(trustOptions))
	for index, option := range trustOptions {
		options[index] = option.Label
		byLabel[option.Label] = option
	}
	go func() {
		selected, ok, _ := mode.interactiveUI.Select(context.Background(), "Project trust", options, nil)
		if !ok {
			return
		}
		option := byLabel[selected]
		if err := mode.options.Host.SetProjectTrust(context.Background(), option.Updates); err != nil {
			mode.showError(err)
			return
		}
		mode.showStatusMessage(selected)
	}()
}

func sessionEntryLabel(entry sessionstore.SessionEntry) string {
	switch entry.Type {
	case "message":
		role, text := sessionMessageRoleText(entry.Message)
		text = strings.ReplaceAll(text, "\n", " ")
		if len(text) > 50 {
			text = text[:47] + "..."
		}
		if text != "" {
			return role + ": " + text
		}
	case "compaction":
		return "compaction: " + entry.Summary
	case "branch_summary":
		return "branch summary: " + entry.Summary
	case "custom_message":
		return "custom: " + entry.CustomType
	case "custom":
		return "entry: " + entry.CustomType
	}
	return entry.Type
}

func sessionMessageRoleText(raw json.RawMessage) (string, string) {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return "", ""
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return message.Role, text
	}
	var blocks []struct{ Type, Text string }
	_ = json.Unmarshal(message.Content, &blocks)
	var result strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return message.Role, result.String()
}

func (mode *InteractiveMode) showStatusMessage(text string) {
	mode.chat.AddChild(newStyledText("dim", text))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) sessionBusy() bool {
	mode.mu.Lock()
	streaming := mode.streaming
	mode.mu.Unlock()
	return streaming || mode.session.IsCompacting()
}

func (mode *InteractiveMode) startNewSession() {
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	if mode.sessionBusy() {
		mode.showStatusMessage("Wait for the current operation to finish before starting a new session.")
		return
	}
	go func() {
		result, err := mode.options.Host.NewSession(context.Background(), nil)
		if err != nil {
			mode.showError(err)
			return
		}
		if result.Cancelled {
			return
		}
		mode.showStatusMessage("Started new session")
	}()
}

func (mode *InteractiveMode) cloneSession() {
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	leaf := mode.session.Manager().GetLeafID()
	if leaf == nil {
		mode.showStatusMessage("Nothing to clone yet")
		return
	}
	if mode.sessionBusy() {
		mode.showStatusMessage("Wait for the current operation to finish before cloning.")
		return
	}
	go func(entryID string) {
		result, err := mode.options.Host.Fork(context.Background(), entryID, &extensions.ForkOptions{Position: extensions.ForkAt})
		if err != nil {
			mode.showError(err)
			return
		}
		if result.Cancelled {
			return
		}
		mode.editor.SetText("")
		mode.showStatusMessage("Cloned to new session")
	}(*leaf)
}

func (mode *InteractiveMode) resumeSession() {
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	if mode.sessionBusy() {
		mode.showStatusMessage("Wait for the current operation to finish before resuming.")
		return
	}
	sessions := mode.options.Host.ListProjectSessions(nil)
	if len(sessions) == 0 {
		mode.showStatusMessage("No sessions found")
		return
	}
	options := make([]string, 0, len(sessions))
	paths := map[string]string{}
	for _, info := range sessions {
		name := info.FirstMessage
		if info.Name != nil && *info.Name != "" {
			name = *info.Name
		}
		name = strings.ReplaceAll(name, "\n", " ")
		if len(name) > 54 {
			name = name[:51] + "..."
		}
		label := name + "  [" + info.ID[:min(8, len(info.ID))] + "]"
		options = append(options, label)
		paths[label] = info.Path
	}
	go func() {
		selected, ok, err := mode.interactiveUI.Select(context.Background(), "Resume session", options, nil)
		if err != nil || !ok {
			return
		}
		ctx := context.Background()
		result, err := mode.options.Host.SwitchSession(ctx, paths[selected], "", nil)
		if err != nil {
			var missingCWD *MissingSessionCwdError
			if !errors.As(err, &missingCWD) {
				mode.showError(err)
				return
			}
			selectedCWD, confirmed, confirmErr := mode.promptForMissingSessionCwd(ctx, missingCWD)
			if confirmErr != nil {
				mode.showError(confirmErr)
				return
			}
			if !confirmed {
				mode.showStatusMessage("Resume cancelled")
				return
			}
			result, err = mode.options.Host.SwitchSession(ctx, paths[selected], selectedCWD, nil)
			if err != nil {
				mode.showError(err)
				return
			}
			if !result.Cancelled {
				mode.showStatusMessage("Resumed session in current cwd")
			}
			return
		}
		if result.Cancelled {
			return
		}
		mode.showStatusMessage("Resumed session")
	}()
}

func (mode *InteractiveMode) importSession(path string) {
	path = strings.TrimSpace(strings.Trim(path, "\""))
	if path == "" {
		mode.showError(errors.New("Usage: /import <path.jsonl>")) //nolint:staticcheck // Upstream command text is observable.
		return
	}
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	if mode.sessionBusy() {
		mode.showStatusMessage("Wait for the current operation to finish before importing.")
		return
	}
	go func() {
		confirmed, err := mode.interactiveUI.Confirm(context.Background(), "Import session", "Replace current session with "+path+"?", nil)
		if err != nil || !confirmed {
			mode.showStatusMessage("Import cancelled")
			return
		}
		ctx := context.Background()
		result, err := mode.options.Host.ImportSession(ctx, path, "")
		if err != nil {
			var missingCWD *MissingSessionCwdError
			if !errors.As(err, &missingCWD) {
				mode.showError(err)
				return
			}
			selectedCWD, confirmed, confirmErr := mode.promptForMissingSessionCwd(ctx, missingCWD)
			if confirmErr != nil {
				mode.showError(confirmErr)
				return
			}
			if !confirmed {
				mode.showStatusMessage("Import cancelled")
				return
			}
			result, err = mode.options.Host.ImportSession(ctx, path, selectedCWD)
			if err != nil {
				mode.showError(err)
				return
			}
		}
		if result.Cancelled {
			mode.showStatusMessage("Import cancelled")
			return
		}
		mode.showStatusMessage("Session imported from: " + path)
	}()
}

func (mode *InteractiveMode) promptForMissingSessionCwd(ctx context.Context, err *MissingSessionCwdError) (string, bool, error) {
	confirmed, confirmErr := mode.interactiveUI.Confirm(ctx, "Session cwd not found", formatMissingSessionCwdPrompt(err), nil)
	if confirmErr != nil || !confirmed {
		return "", confirmed, confirmErr
	}
	return err.FallbackCWD, true, nil
}

func (mode *InteractiveMode) reloadSession() {
	if mode.options.Host == nil {
		mode.showError(errors.New("session host is unavailable"))
		return
	}
	if mode.sessionBusy() {
		mode.showStatusMessage("Wait for the current operation to finish before reloading.")
		return
	}
	go func() {
		mode.showStatusMessage("Reloading keybindings, extensions, skills, prompts, themes, and context files...")
		if err := mode.options.Host.Reload(context.Background()); err != nil {
			mode.showError(err)
			return
		}
		mode.showStatusMessage("Reload complete")
	}()
}

func (mode *InteractiveMode) authenticateProvider(argument string, logout bool) {
	verb := "login"
	if logout {
		verb = "logout"
	}
	if mode.options.Host == nil {
		mode.showError(fmt.Errorf("%s is unavailable without a session host", verb))
		return
	}
	provider := strings.TrimSpace(argument)
	go func() {
		ctx := mode.authenticationContext()
		options, err := mode.options.Host.AuthOptions(ctx)
		if err != nil {
			mode.showError(err)
			return
		}
		if logout {
			candidates := options.Logout
			if provider != "" {
				for _, candidate := range candidates {
					if strings.EqualFold(candidate.ID, provider) || strings.EqualFold(candidate.Name, provider) {
						mode.runAuthentication(candidate.ID, candidate.AuthType, true)
						return
					}
				}
				mode.showError(fmt.Errorf("provider %q has no stored credential", provider))
				return
			}
			selected, ok := mode.selectAuthProvider(ctx, "Provider to logout", candidates, false)
			if ok {
				mode.runAuthentication(selected.ID, selected.AuthType, true)
			}
			return
		}

		candidates := options.Login
		if provider != "" {
			matched := matchingAuthProviders(candidates, provider)
			if len(matched) == 0 {
				mode.showError(fmt.Errorf("provider %q does not support login", provider))
				return
			}
			selected := matched[0]
			if len(matched) > 1 {
				var ok bool
				if allAuthOptionsForSameProvider(matched) {
					selected, ok = mode.selectAuthMethod(ctx, matched)
				} else {
					selected, ok = mode.selectAuthProvider(ctx, "Provider to login", matched, true)
				}
				if !ok {
					return
				}
			}
			mode.startAuthentication(selected)
			return
		}

		types := make([]aiauth.AuthType, 0, 2)
		for _, authType := range []aiauth.AuthType{aiauth.AuthTypeOAuth, aiauth.AuthTypeAPIKey} {
			if slices.ContainsFunc(candidates, func(candidate InteractiveAuthProvider) bool { return candidate.AuthType == authType }) {
				types = append(types, authType)
			}
		}
		if len(types) == 0 {
			mode.showStatusMessage("No providers available to login")
			return
		}
		authType := types[0]
		if len(types) > 1 {
			labels := []string{"Sign in with an account", "Sign in with an API key"}
			selected, ok, selectErr := mode.interactiveUI.Select(ctx, "Select authentication method", labels, nil)
			if selectErr != nil || !ok {
				return
			}
			if selected == labels[1] {
				authType = aiauth.AuthTypeAPIKey
			}
		}
		filtered := make([]InteractiveAuthProvider, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.AuthType == authType {
				filtered = append(filtered, candidate)
			}
		}
		selected, ok := mode.selectAuthProvider(ctx, "Provider to login", filtered, true)
		if ok {
			mode.startAuthentication(selected)
		}
	}()
}

func matchingAuthProviders(options []InteractiveAuthProvider, provider string) []InteractiveAuthProvider {
	matched := make([]InteractiveAuthProvider, 0)
	for _, option := range options {
		if strings.EqualFold(option.ID, provider) || strings.EqualFold(option.Name, provider) {
			matched = append(matched, option)
		}
	}
	return matched
}

func allAuthOptionsForSameProvider(options []InteractiveAuthProvider) bool {
	if len(options) == 0 {
		return false
	}
	id := options[0].ID
	for _, option := range options[1:] {
		if option.ID != id {
			return false
		}
	}
	return true
}

func (mode *InteractiveMode) selectAuthMethod(ctx context.Context, options []InteractiveAuthProvider) (InteractiveAuthProvider, bool) {
	labels := make([]string, len(options))
	for index, option := range options {
		labels[index] = authMethodLabel(option)
	}
	selected, ok, err := mode.interactiveUI.Select(ctx, "Select authentication method for "+options[0].Name, labels, nil)
	if err != nil || !ok {
		return InteractiveAuthProvider{}, false
	}
	for index, label := range labels {
		if label == selected {
			return options[index], true
		}
	}
	return InteractiveAuthProvider{}, false
}

func authMethodLabel(option InteractiveAuthProvider) string {
	if option.AuthType == aiauth.AuthTypeOAuth {
		if option.LoginLabel != "" {
			return option.LoginLabel
		}
		return "Sign in with an account"
	}
	return "Sign in with an API key"
}

func (mode *InteractiveMode) selectAuthProvider(ctx context.Context, title string, options []InteractiveAuthProvider, showConfigured bool) (InteractiveAuthProvider, bool) {
	if len(options) == 0 {
		mode.showStatusMessage("No providers available")
		return InteractiveAuthProvider{}, false
	}
	selected, ok, err := mode.interactiveUI.selectItems(ctx, title, authProviderSelectItems(options, showConfigured), nil)
	if err != nil || !ok {
		return InteractiveAuthProvider{}, false
	}
	index, err := strconv.Atoi(selected)
	if err != nil || index < 0 || index >= len(options) {
		return InteractiveAuthProvider{}, false
	}
	return options[index], true
}

func authProviderSelectItems(options []InteractiveAuthProvider, showStatus bool) []tui.SelectItem {
	showAuthType := false
	if len(options) > 1 {
		first := options[0].AuthType
		showAuthType = slices.ContainsFunc(options[1:], func(option InteractiveAuthProvider) bool { return option.AuthType != first })
	}
	items := make([]tui.SelectItem, len(options))
	for index, option := range options {
		display := option
		if showAuthType {
			authType := "API key"
			if option.AuthType == aiauth.AuthTypeOAuth {
				authType = "subscription"
			}
			display.Name += " [" + authType + "]"
		}
		items[index] = tui.SelectItem{Value: strconv.Itoa(index), Label: authProviderLabel(display, showStatus)}
	}
	return items
}

func authProviderLabel(option InteractiveAuthProvider, showStatus bool) string {
	if !showStatus {
		return option.Name
	}
	if option.Status == nil {
		return option.Name + " • unconfigured"
	}
	if option.Status.Type != option.AuthType {
		configured := "API key configured"
		if option.Status.Type == aiauth.AuthTypeOAuth {
			configured = "subscription configured"
		}
		return option.Name + " • " + configured
	}
	source := option.Status.Source
	if source == "" || source == "OAuth" || source == "stored credential" {
		return option.Name + " ✓ configured"
	}
	if isAuthEnvironmentSource(source) {
		source = "env: " + source
	}
	return option.Name + " ✓ " + source
}

func isAuthEnvironmentSource(source string) bool {
	for _, name := range strings.Split(source, ", ") {
		if name == "" || name[0] < 'A' || name[0] > 'Z' {
			return false
		}
		for index := 1; index < len(name); index++ {
			character := name[index]
			if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
				return false
			}
		}
	}
	return true
}

func (mode *InteractiveMode) startAuthentication(provider InteractiveAuthProvider) {
	if !provider.LoginAvailable {
		method := provider.MethodName
		if method == "" {
			method = "Authentication"
		}
		mode.showStatusMessage(method + " is configured outside pi.")
		return
	}
	mode.runAuthentication(provider.ID, provider.AuthType, false)
}

func (mode *InteractiveMode) runAuthentication(provider string, authType aiauth.AuthType, logout bool) {
	ctx := mode.authenticationContext()
	verb := "login"
	var err error
	if logout {
		verb = "logout"
		err = mode.options.Host.Logout(ctx, provider)
	} else {
		err = mode.options.Host.Login(ctx, provider, authType, tuiAuthInteraction{mode: mode})
	}
	if err != nil {
		mode.showError(err)
		return
	}
	label := verb
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	mode.showStatusMessage(label + " successful for " + provider)
}

func (mode *InteractiveMode) authenticationContext() context.Context {
	mode.mu.Lock()
	defer mode.mu.Unlock()
	if mode.authContext != nil {
		return mode.authContext
	}
	return context.Background()
}

type tuiAuthInteraction struct{ mode *InteractiveMode }

func (interaction tuiAuthInteraction) Prompt(ctx context.Context, prompt aiauth.AuthPrompt) (string, error) {
	if prompt.Type == aiauth.PromptSelect {
		labels := make([]string, 0, len(prompt.Options))
		ids := make(map[string]string, len(prompt.Options))
		for _, option := range prompt.Options {
			label := option.Label
			if option.Description != "" {
				label += " — " + option.Description
			}
			labels = append(labels, label)
			ids[label] = option.ID
		}
		selected, ok, err := interaction.mode.interactiveUI.Select(ctx, prompt.Message, labels, nil)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", context.Canceled
		}
		return ids[selected], nil
	}
	placeholder := prompt.Placeholder
	value, ok, err := interaction.mode.interactiveUI.Input(ctx, prompt.Message, &placeholder, nil)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", context.Canceled
	}
	return value, nil
}

func (interaction tuiAuthInteraction) Notify(event aiauth.AuthEvent) {
	message := event.Message
	switch event.Type {
	case aiauth.EventAuthURL:
		message = strings.TrimSpace(event.Instructions + "\n" + event.URL)
	case aiauth.EventDeviceCode:
		message = strings.TrimSpace(event.VerificationURI + "\nCode: " + event.UserCode)
	}
	if message != "" {
		interaction.mode.showStatusMessage(message)
	}
}

func (mode *InteractiveMode) openExternalEditor() {
	command := mode.session.InteractiveModeSettings().ExternalEditor
	if command == "" {
		command = os.Getenv("VISUAL")
	}
	if command == "" {
		command = os.Getenv("EDITOR")
	}
	if command == "" {
		mode.showStatusMessage("No editor configured. Set externalEditor in settings.json or $VISUAL/$EDITOR.")
		return
	}
	initial := mode.editor.GetText()
	go func() {
		file, err := os.CreateTemp("", "pi-editor-*.md")
		if err != nil {
			mode.showError(err)
			return
		}
		path := file.Name()
		defer func() { _ = os.Remove(path) }()
		if _, err = file.WriteString(initial); err != nil {
			_ = file.Close()
			mode.showError(err)
			return
		}
		if err = file.Close(); err != nil {
			mode.showError(err)
			return
		}
		if err = mode.ui.Stop(); err != nil {
			mode.showError(err)
			return
		}
		var process *exec.Cmd
		if runtime.GOOS == "windows" {
			process = exec.Command("cmd", "/C", command+" \""+path+"\"")
		} else {
			process = exec.Command("sh", "-c", command+` "$1"`, "pi-editor", path)
		}
		process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := process.Run()
		startErr := mode.ui.Start()
		if runErr != nil {
			mode.showError(runErr)
			return
		}
		if startErr != nil {
			mode.showError(startErr)
			return
		}
		content, err := os.ReadFile(path)
		if err != nil {
			mode.showError(err)
			return
		}
		mode.editor.SetText(strings.TrimRight(string(content), "\r\n"))
		mode.ui.RequestRender()
	}()
}

func (mode *InteractiveMode) selectScopedModels() {
	models := mode.session.AvailableModels()
	if len(models) == 0 {
		mode.showStatusMessage("No models available")
		return
	}
	selected := map[string]bool{}
	current := mode.session.ScopedModels()
	if len(current) == 0 {
		for _, model := range models {
			selected[fmt.Sprintf("%s/%s", model.Provider, model.ID)] = true
		}
	} else {
		for _, scoped := range current {
			selected[fmt.Sprintf("%s/%s", scoped.Model.Provider, scoped.Model.ID)] = true
		}
	}
	go func() {
		for {
			options := []string{"Save and close", "Enable all", "Clear all"}
			ids := map[string]string{}
			for _, model := range models {
				id := fmt.Sprintf("%s/%s", model.Provider, model.ID)
				mark := "[ ] "
				if selected[id] {
					mark = "[x] "
				}
				label := mark + id
				options = append(options, label)
				ids[label] = id
			}
			choice, ok, err := mode.interactiveUI.Select(context.Background(), "Scoped models", options, nil)
			if err != nil || !ok {
				return
			}
			switch choice {
			case "Enable all":
				for _, model := range models {
					selected[fmt.Sprintf("%s/%s", model.Provider, model.ID)] = true
				}
			case "Clear all":
				clear(selected)
			case "Save and close":
				mode.applyScopedModelSelection(models, selected, true)
				mode.showStatusMessage("Model selection saved to settings")
				return
			default:
				id := ids[choice]
				selected[id] = !selected[id]
				mode.applyScopedModelSelection(models, selected, false)
			}
		}
	}()
}

func (mode *InteractiveMode) applyScopedModelSelection(models []ai.Model, selected map[string]bool, persist bool) {
	patterns := make([]string, 0, len(selected))
	scoped := make([]codingagent.ScopedModel, 0, len(selected))
	for _, model := range models {
		id := fmt.Sprintf("%s/%s", model.Provider, model.ID)
		if selected[id] {
			patterns = append(patterns, id)
			scoped = append(scoped, codingagent.ScopedModel{Model: model})
		}
	}
	if len(scoped) == 0 || len(scoped) == len(models) {
		mode.session.SetScopedModels(nil)
	} else {
		mode.session.SetScopedModels(scoped)
	}
	if persist {
		if len(patterns) == len(models) {
			patterns = nil
		}
		mode.session.SetEnabledModels(patterns)
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) GitBranch() string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = mode.cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (mode *InteractiveMode) AvailableProviderCount() int {
	seen := make(map[ai.ProviderID]struct{})
	for _, model := range mode.session.AvailableModels() {
		seen[model.Provider] = struct{}{}
	}
	return len(seen)
}

func (mode *InteractiveMode) Statuses() map[string]string {
	mode.mu.Lock()
	defer mode.mu.Unlock()
	result := make(map[string]string, len(mode.footerStatuses))
	for k, v := range mode.footerStatuses {
		result[k] = v
	}
	return result
}

func (mode *InteractiveMode) CurrentCWD() string { return mode.cwd }
func (mode *InteractiveMode) SessionName() string {
	if mode.session == nil || mode.session.Manager() == nil {
		return ""
	}
	name := mode.session.Manager().GetSessionName()
	if name == nil {
		return ""
	}
	return *name
}

func (mode *InteractiveMode) showSessionInfo() {
	stats := mode.session.GetSessionStats()
	state := mode.session.State()
	var lines []string
	lines = append(lines, theme.Bold("Session info"))
	if state.Model != nil {
		lines = append(lines, fmt.Sprintf("  Model: %s/%s", state.Model.Provider, state.Model.ID))
	}
	lines = append(lines, fmt.Sprintf("  Thinking: %s", state.ThinkingLevel))
	lines = append(lines, fmt.Sprintf("  Messages: %d (%d user, %d assistant)", stats.TotalMessages, stats.UserMessages, stats.AssistantMessages))
	lines = append(lines, fmt.Sprintf("  Tool calls: %d", stats.ToolCalls))
	lines = append(lines, fmt.Sprintf("  Tokens: %s (in: %s, out: %s)", formatTokens(stats.Tokens.Total), formatTokens(stats.Tokens.Input), formatTokens(stats.Tokens.Output)))
	if stats.Cost > 0 {
		lines = append(lines, fmt.Sprintf("  Cost: $%.4f", stats.Cost))
	}
	if stats.ContextUsage != nil && stats.ContextUsage.Percent != nil {
		lines = append(lines, fmt.Sprintf("  Context: %.0f%%", *stats.ContextUsage.Percent))
	}
	if stats.SessionFile != "" {
		lines = append(lines, fmt.Sprintf("  File: %s", stats.SessionFile))
	}
	mode.chat.AddChild(tui.NewText(strings.Join(lines, "\n"), 1, 1, nil))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) handleEvent(event any) {
	switch ev := event.(type) {
	case agent.AgentStartEvent:
		mode.setStatus(NewWorkingStatusIndicator(mode.ui, "Working..."))
		if mode.session.InteractiveModeSettings().ShowTerminalProgress {
			mode.ui.Terminal().SetProgress(true)
		}

	case agent.MessageStartEvent:
		if !isAssistantMessage(ev.Message) {
			mode.renderAgentMessage(ev.Message)
			return
		}
		assistant := asAssistantMessage(ev.Message)
		if assistant == nil {
			return
		}
		mode.mu.Lock()
		hidden := mode.thinkingHidden
		label := mode.thinkingLabel
		mode.mu.Unlock()
		comp := NewAssistantMessageComponent(assistant, hidden, mode.mdTheme, label, mode.currentOutputPad())
		mode.mu.Lock()
		mode.currentStreaming = comp
		mode.mu.Unlock()
		mode.chat.AddChild(comp)
		mode.ui.RequestRender()

	case agent.MessageUpdateEvent:
		assistant := asAssistantMessage(ev.Message)
		if assistant == nil {
			return
		}
		mode.mu.Lock()
		comp := mode.currentStreaming
		mode.mu.Unlock()
		if comp != nil {
			comp.UpdateContent(assistant)
			mode.ui.RequestRender()
		}

	case agent.MessageEndEvent:
		assistant := asAssistantMessage(ev.Message)
		if assistant == nil {
			return
		}
		mode.mu.Lock()
		comp := mode.currentStreaming
		mode.mu.Unlock()
		if comp != nil {
			comp.UpdateContent(assistant)
		}
		mode.maybeShowCacheMiss(assistant)
		mode.ui.RequestRender()

	case agent.ToolExecutionStartEvent:
		tc := NewToolExecutionComponent(ev.ToolName, ev.ToolCallID, ev.Args, mode.showImages(), mode.toolDefinition(ev.ToolName), mode.ui, mode.cwd)
		tc.SetArgsComplete()
		mode.mu.Lock()
		mode.toolComponents[ev.ToolCallID] = tc
		tc.SetExpanded(mode.toolsExpanded)
		mode.expandables = append(mode.expandables, tc)
		mode.mu.Unlock()
		mode.chat.AddChild(tc)
		mode.ui.RequestRender()

	case agent.ToolExecutionUpdateEvent:
		mode.mu.Lock()
		tc := mode.toolComponents[ev.ToolCallID]
		mode.mu.Unlock()
		if tc != nil {
			tc.MarkExecutionStarted()
			if ev.PartialResult.Content != nil {
				tc.UpdateResult(ev.PartialResult.Content, false, ev.PartialResult.Details, true)
			}
			mode.ui.RequestRender()
		}

	case agent.ToolExecutionEndEvent:
		mode.mu.Lock()
		tc := mode.toolComponents[ev.ToolCallID]
		mode.mu.Unlock()
		if tc != nil {
			tc.UpdateResult(ev.Result.Content, ev.IsError, ev.Result.Details, false)
			mode.ui.RequestRender()
		}

	case codingagent.AgentSettledEvent:
		mode.setStatus(&IdleStatus{})
		mode.ui.Terminal().SetProgress(false)

	case codingagent.QueueUpdateEvent:
		mode.pendingMessages.Clear()
		for _, text := range ev.Steering {
			mode.pendingMessages.AddChild(tui.NewText(theme.FG("warning", "steer queued: "+text), 1, 0, nil))
		}
		for _, text := range ev.FollowUp {
			mode.pendingMessages.AddChild(tui.NewText(theme.FG("dim", "follow-up queued: "+text), 1, 0, nil))
		}
		mode.ui.RequestRender()

	case codingagent.CompactionStartEvent:
		mode.setStatus(NewCompactionStatusIndicator(mode.ui, ev.Reason))

	case codingagent.CompactionEndEvent:
		mode.renderInitialMessages()
		mode.setStatus(&IdleStatus{})

	case codingagent.AutoRetryStartEvent:
		mode.setStatus(NewRetryStatusIndicator(mode.ui, ev.Attempt, ev.MaxAttempts, ev.DelayMS))

	case codingagent.AutoRetryEndEvent:
		mode.setStatus(&IdleStatus{})

	case codingagent.ThinkingLevelChangedEvent:
		mode.ui.RequestRender()

	case codingagent.SessionInfoChangedEvent:
		mode.ui.RequestRender()
	}
}

func (mode *InteractiveMode) setStatus(indicator tui.Component) {
	mode.mu.Lock()
	if prev, ok := mode.statusIndicator.(*StatusIndicator); ok {
		prev.Dispose()
	}
	mode.statusIndicator = indicator
	mode.mu.Unlock()

	mode.status.Clear()
	mode.status.AddChild(indicator)
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) showError(err error) {
	if err == nil {
		return
	}
	mode.chat.AddChild(tui.NewText(theme.FG("error", "Error: "+err.Error()), 1, 0, nil))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) addUserMessageToChat(text string) {
	mode.chat.AddChild(NewUserMessageComponent(text, mode.mdTheme, mode.currentOutputPad()))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) showImages() bool {
	return mode.session.InteractiveSettings().ShowImages
}

func (mode *InteractiveMode) toolDefinition(name string) *extensions.ToolDefinition {
	builtIn := nativeToolDefinition(name, mode.session.RegisteredTool(name))
	definition := mode.session.GetToolDefinition(name)
	if definition == nil {
		return builtIn
	}
	if builtIn == nil {
		return definition
	}
	merged := *definition
	if merged.RenderCall == nil {
		merged.RenderCall = builtIn.RenderCall
	}
	if merged.RenderResult == nil {
		merged.RenderResult = builtIn.RenderResult
	}
	return &merged
}

func nativeToolDefinition(name string, registered agent.AgentTool) *extensions.ToolDefinition {
	renderer, ok := registered.(tools.PlainTextRenderer)
	if !ok {
		return nil
	}
	return &extensions.ToolDefinition{
		Name: name,
		RenderCall: func(args any, palette extensions.Theme, context extensions.ToolRenderContext) extensions.Component {
			container := &tui.Container{}
			container.AddChild(tui.NewText(palette.FG("toolTitle", renderer.RenderCall(args)), 0, 0, nil))
			if name != "edit" || !context.ArgsComplete {
				return container
			}
			path, edits, ok := editPreviewInput(args)
			if !ok {
				return container
			}
			preview, err := tools.ComputeEditsDiff(path, edits, context.CWD)
			container.AddChild(tui.NewSpacer(1))
			if err != nil {
				context.State["editPreviewError"] = err.Error()
				container.AddChild(tui.NewText(palette.FG("error", err.Error()), 0, 0, nil))
				return container
			}
			context.State["editPreviewDiff"] = preview.Diff
			container.AddChild(tui.NewText(strings.Join(theme.Highlight(preview.Diff, "diff", theme.Current()), "\n"), 0, 0, nil))
			return container
		},
		RenderResult: func(result agent.AgentToolResult, _ extensions.ToolRenderResultOptions, palette extensions.Theme, context extensions.ToolRenderContext) extensions.Component {
			if name == "edit" {
				if diff := editResultDiff(result.Details); diff != "" {
					if preview, _ := context.State["editPreviewDiff"].(string); preview == diff {
						return &tui.Container{}
					}
					return tui.NewText(strings.Join(theme.Highlight(diff, "diff", theme.Current()), "\n"), 0, 0, nil)
				}
				if previewError, _ := context.State["editPreviewError"].(string); previewError == renderer.RenderResult(result) {
					return &tui.Container{}
				}
			}
			return tui.NewText(palette.FG("toolOutput", renderer.RenderResult(result)), 0, 0, nil)
		},
	}
}

func editPreviewInput(args any) (string, []tools.Edit, bool) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", nil, false
	}
	var input tools.EditToolInput
	if json.Unmarshal(encoded, &input) != nil || input.Path == "" || len(input.Edits) == 0 {
		return "", nil, false
	}
	return input.Path, input.Edits, true
}

func editResultDiff(details any) string {
	switch value := details.(type) {
	case tools.EditToolDetails:
		return value.Diff
	case *tools.EditToolDetails:
		if value != nil {
			return value.Diff
		}
	case json.RawMessage:
		var decoded tools.EditToolDetails
		if json.Unmarshal(value, &decoded) == nil {
			return decoded.Diff
		}
	case map[string]any:
		if diff, ok := value["diff"].(string); ok {
			return diff
		}
	}
	return ""
}

func (mode *InteractiveMode) renderInitialMessages() {
	mode.chat.Clear()
	mode.mu.Lock()
	mode.toolComponents = make(map[string]*ToolExecutionComponent)
	mode.expandables = nil
	mode.mu.Unlock()
	for _, entry := range mode.session.Manager().BuildContextEntries() {
		switch entry.Type {
		case "message":
			message, err := ai.UnmarshalMessage(entry.Message)
			if err == nil {
				mode.renderAgentMessage(message)
			} else {
				mode.renderRawAgentMessage(entry.Message)
			}
		case "custom_message":
			if entry.Display {
				mode.renderCustomMessage(entry.CustomType, entry.Content, entry.Details)
			}
		case "custom":
			mode.renderCustomEntry(entry.CustomType, entry.Data)
		case "compaction":
			component := NewCompactionSummaryMessage(entry.Summary, int64(entry.TokensBefore), mode.mdTheme)
			mode.addExpandable(component)
			mode.chat.AddChild(component)
		case "branch_summary":
			component := NewBranchSummaryMessage(entry.Summary, mode.mdTheme)
			mode.addExpandable(component)
			mode.chat.AddChild(component)
		}
	}
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) currentOutputPad() int {
	mode.mu.Lock()
	defer mode.mu.Unlock()
	return mode.outputPad
}

func isAssistantMessage(message any) bool { return asAssistantMessage(message) != nil }

func (mode *InteractiveMode) addExpandable(component expandableComponent) {
	mode.mu.Lock()
	component.SetExpanded(mode.toolsExpanded)
	mode.expandables = append(mode.expandables, component)
	mode.mu.Unlock()
}

func (mode *InteractiveMode) renderAgentMessage(message any) {
	if assistant := asAssistantMessage(message); assistant != nil {
		mode.mu.Lock()
		hidden, label := mode.thinkingHidden, mode.thinkingLabel
		mode.mu.Unlock()
		component := NewAssistantMessageComponent(assistant, hidden, mode.mdTheme, label, mode.currentOutputPad())
		mode.chat.AddChild(component)
		for _, block := range assistant.Content {
			call, ok := block.(*ai.ToolCall)
			if !ok || call == nil {
				continue
			}
			toolComponent := NewToolExecutionComponent(call.Name, call.ID, call.Arguments, mode.showImages(), mode.toolDefinition(call.Name), mode.ui, mode.cwd)
			toolComponent.SetArgsComplete()
			mode.mu.Lock()
			toolComponent.SetExpanded(mode.toolsExpanded)
			mode.toolComponents[call.ID] = toolComponent
			mode.expandables = append(mode.expandables, toolComponent)
			mode.mu.Unlock()
			mode.chat.AddChild(toolComponent)
		}
		mode.maybeShowCacheMiss(assistant)
		return
	}
	switch value := message.(type) {
	case *ai.UserMessage:
		mode.renderUserMessage(value)
	case ai.UserMessage:
		copy := value
		mode.renderUserMessage(&copy)
	case *ai.ToolResultMessage:
		mode.renderToolResult(value)
	case ai.ToolResultMessage:
		copy := value
		mode.renderToolResult(&copy)
	case harness.BashExecutionMessage:
		mode.renderBashMessage(value)
	case *harness.BashExecutionMessage:
		if value != nil {
			mode.renderBashMessage(*value)
		}
	}
}

type cacheRequest struct {
	prompt    int64
	model     string
	timestamp int64
	reported  bool
}
type cacheMiss struct {
	tokens       int64
	cost         float64
	idle         int64
	modelChanged bool
}

func (mode *InteractiveMode) maybeShowCacheMiss(message *ai.AssistantMessage) {
	if !mode.session.InteractiveSettings().ShowCacheMissNotices || message == nil {
		return
	}
	miss := mode.detectCacheMiss(message)
	if miss == nil || miss.tokens < 20_000 && miss.cost < .1 {
		return
	}
	label := "Cache miss"
	if miss.modelChanged {
		label = "Cache miss after model switch"
	} else if miss.idle >= int64(5*time.Minute/time.Millisecond) {
		label = fmt.Sprintf("Cache miss after %dm idle", (miss.idle+30_000)/60_000)
	}
	cost := ""
	if miss.cost >= .01 {
		cost = fmt.Sprintf(" (~$%.2f)", miss.cost)
	}
	mode.chat.AddChild(tui.NewText(theme.FG("warning", fmt.Sprintf("%s: %s tokens re-billed%s", label, formatTokens(miss.tokens), cost)), 1, 0, nil))
}

func (mode *InteractiveMode) detectCacheMiss(target *ai.AssistantMessage) *cacheMiss {
	var previous *cacheRequest
	reported := false
	for _, entry := range mode.session.Manager().GetEntries() {
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			previous = nil
			reported = false
			continue
		}
		if entry.Type != "message" {
			continue
		}
		decoded, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		assistant := asAssistantMessage(decoded)
		if assistant == nil {
			continue
		}
		if assistant.Timestamp == target.Timestamp && assistant.Provider == target.Provider && assistant.Model == target.Model {
			return computeCacheMiss(previous, target, mode.session.AvailableModels())
		}
		prompt := assistant.Usage.Input + assistant.Usage.CacheRead + assistant.Usage.CacheWrite
		if prompt > 0 {
			reported = reported || assistant.Usage.CacheRead+assistant.Usage.CacheWrite > 0
			previous = &cacheRequest{prompt: prompt, model: string(assistant.Provider) + "/" + assistant.Model, timestamp: assistant.Timestamp, reported: reported}
		}
	}
	return computeCacheMiss(previous, target, mode.session.AvailableModels())
}

func computeCacheMiss(previous *cacheRequest, message *ai.AssistantMessage, models []ai.Model) *cacheMiss {
	prompt := message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheWrite
	if previous == nil || prompt <= 0 || message.Usage.CacheRead+message.Usage.CacheWrite == 0 && !previous.reported {
		return nil
	}
	missed := min(previous.prompt, prompt) - message.Usage.CacheRead
	if missed <= 1024 {
		return nil
	}
	paidTokens := message.Usage.Input + message.Usage.CacheWrite
	paidRate := 0.0
	if paidTokens > 0 {
		paidRate = (message.Usage.Cost.Input + message.Usage.Cost.CacheWrite) / float64(paidTokens)
	}
	readRate := 0.0
	if message.Usage.CacheRead > 0 {
		readRate = message.Usage.Cost.CacheRead / float64(message.Usage.CacheRead)
	} else {
		for _, model := range models {
			if model.Provider == message.Provider && model.ID == message.Model {
				readRate = model.Cost.CacheRead / 1_000_000
				break
			}
		}
	}
	return &cacheMiss{tokens: missed, cost: float64(missed) * max(0, paidRate-readRate), idle: max(0, message.Timestamp-previous.timestamp), modelChanged: previous.model != string(message.Provider)+"/"+message.Model}
}

func (mode *InteractiveMode) renderRawAgentMessage(raw json.RawMessage) {
	var envelope struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	switch envelope.Role {
	case "bashExecution":
		var message harness.BashExecutionMessage
		if json.Unmarshal(raw, &message) == nil {
			mode.renderBashMessage(message)
		}
	case "custom":
		var message harness.CustomMessage
		if json.Unmarshal(raw, &message) == nil && message.Display {
			mode.renderCustomMessage(message.CustomType, message.Content, message.Details)
		}
	}
}

func (mode *InteractiveMode) renderUserMessage(message *ai.UserMessage) {
	if message == nil {
		return
	}
	if text := userMessageText(message); text != "" {
		mode.addUserMessageToChat(text)
	}
	if !mode.showImages() || message.Content.Text != nil {
		return
	}
	maxWidth := mode.session.InteractiveSettings().ImageWidthCells
	if maxWidth <= 0 {
		maxWidth = 60
	}
	for _, block := range message.Content.Blocks {
		image, ok := block.(*ai.ImageContent)
		if !ok || image == nil {
			continue
		}
		mode.chat.AddChild(tui.NewImage(image.Data, image.MimeType, tui.ImageTheme{}, &tui.ImageOptions{MaxWidthCells: &maxWidth}, tui.GetImageDimensions(image.Data, image.MimeType)))
	}
}

func (mode *InteractiveMode) renderToolResult(message *ai.ToolResultMessage) {
	if message == nil {
		return
	}
	mode.mu.Lock()
	component := mode.toolComponents[message.ToolCallID]
	mode.mu.Unlock()
	if component == nil {
		component = NewToolExecutionComponent(message.ToolName, message.ToolCallID, nil, mode.showImages(), mode.toolDefinition(message.ToolName), mode.ui, mode.cwd)
		mode.mu.Lock()
		mode.toolComponents[message.ToolCallID] = component
		mode.expandables = append(mode.expandables, component)
		mode.mu.Unlock()
		mode.chat.AddChild(component)
	}
	component.UpdateResult(message.Content, message.IsError, message.Details, false)
}

func (mode *InteractiveMode) renderBashMessage(message harness.BashExecutionMessage) {
	component := NewBashExecutionComponent(message.Command, mode.ui, message.ExcludeFromContext != nil && *message.ExcludeFromContext)
	if message.Output != "" {
		component.AppendOutput(message.Output)
	}
	component.SetComplete(message.ExitCode, message.Cancelled)
	mode.addExpandable(component)
	mode.chat.AddChild(component)
}

func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func decodeMaybeJSON(value any) any {
	if raw, ok := value.(json.RawMessage); ok {
		return decodeJSONValue(raw)
	}
	return value
}

func (mode *InteractiveMode) renderCustomMessage(customType string, content, details any) {
	value := decodeMaybeJSON(content)
	if runner := mode.session.ExtensionRunner(); runner != nil {
		if renderer := runner.MessageRenderer(customType); renderer != nil {
			component := renderer(extensions.CustomMessage{CustomType: customType, Content: value, Display: true, Details: decodeMaybeJSON(details)}, extensions.MessageRenderOptions{Expanded: mode.toolsExpanded}, themeAdapter{value: theme.Current()})
			if component != nil {
				mode.chat.AddChild(component)
				return
			}
		}
	}
	component := NewCustomMessageComponent(customType, value, mode.mdTheme)
	mode.addExpandable(component)
	mode.chat.AddChild(component)
}

func (mode *InteractiveMode) renderCustomEntry(customType string, data json.RawMessage) {
	runner := mode.session.ExtensionRunner()
	if runner == nil {
		return
	}
	renderer := runner.EntryRenderer(customType)
	if renderer == nil {
		return
	}
	component := renderer(decodeJSONValue(data), extensions.EntryRenderOptions{Expanded: mode.toolsExpanded}, themeAdapter{value: theme.Current()})
	if component != nil {
		mode.chat.AddChild(component)
	}
}

func (mode *InteractiveMode) shutdown() {
	mode.mu.Lock()
	mode.shutdownRequested = true
	authCancel := mode.authCancel
	mode.mu.Unlock()
	if authCancel != nil {
		authCancel()
	}
	mode.session.Abort()
	// Unblock getUserInput
	select {
	case mode.inputCh <- inputEntry{}:
	default:
	}
}

// parseSlashCommand splits "/name arg1 arg2" into (name, "arg1 arg2").
func parseSlashCommand(text string) (string, string) {
	text = strings.TrimPrefix(text, "/")
	parts := strings.SplitN(text, " ", 2)
	name := parts[0]
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args
}

func asAssistantMessage(message any) *ai.AssistantMessage {
	switch m := message.(type) {
	case *ai.AssistantMessage:
		return m
	case ai.AssistantMessage:
		return &m
	}
	return nil
}

func userMessageText(message any) string {
	var content ai.UserContent
	switch m := message.(type) {
	case *ai.UserMessage:
		content = m.Content
	case ai.UserMessage:
		content = m.Content
	default:
		return ""
	}
	if content.Text != nil {
		return *content.Text
	}
	var parts []string
	for _, block := range content.Blocks {
		if tb, ok := block.(*ai.TextContent); ok {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}
