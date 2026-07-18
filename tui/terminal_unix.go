//go:build unix

package tui

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	progressActive                  = "\x1b]9;4;3\x07"
	progressClear                   = "\x1b]9;4;0;\x07"
	keyboardProtocolFragmentTimeout = 150 * time.Millisecond
)

// ProcessTerminal owns raw-mode and protocol state for a pair of terminal
// files. NewProcessTerminal uses the process standard streams.
type ProcessTerminal struct {
	mu                sync.Mutex
	writeMu           sync.Mutex
	input             *os.File
	output            *os.File
	readInput         *os.File
	rawState          *term.State
	started           bool
	kitty             bool
	modifyOther       bool
	protocolPushed    bool
	inputHandler      func(string)
	resizeHandler     func()
	buffer            *StdinBuffer
	resizeSignals     chan os.Signal
	progressStop      chan struct{}
	readerDone        chan struct{}
	writeLogPath      string
	negotiationBuffer string
	negotiationTimer  *time.Timer
	lastInput         time.Time
}

func NewProcessTerminal() *ProcessTerminal { return NewProcessTerminalFiles(os.Stdin, os.Stdout) }
func NewProcessTerminalFiles(input, output *os.File) *ProcessTerminal {
	return &ProcessTerminal{input: input, output: output, writeLogPath: resolveTerminalWriteLogPath(os.Getenv("PI_TUI_WRITE_LOG"))}
}

func resolveTerminalWriteLogPath(value string) string {
	if value == "" {
		return ""
	}
	if info, err := os.Stat(value); err == nil && info.IsDir() {
		name := "tui-" + time.Now().Format("2006-01-02_15-04-05") + "-" + strconv.Itoa(os.Getpid()) + ".log"
		return filepath.Join(value, name)
	}
	return value
}

func (terminal *ProcessTerminal) Start(onInput func(string), onResize func()) error {
	terminal.mu.Lock()
	if terminal.started {
		terminal.mu.Unlock()
		return nil
	}
	if terminal.input == nil || terminal.output == nil {
		terminal.mu.Unlock()
		return errors.New("terminal requires input and output files")
	}
	state, err := term.MakeRaw(int(terminal.input.Fd()))
	if err != nil {
		terminal.mu.Unlock()
		return err
	}
	duplicate, err := unix.Dup(int(terminal.input.Fd()))
	if err != nil {
		_ = term.Restore(int(terminal.input.Fd()), state)
		terminal.mu.Unlock()
		return err
	}
	terminal.rawState, terminal.readInput = state, os.NewFile(uintptr(duplicate), terminal.input.Name()+"-pi-read")
	terminal.started, terminal.inputHandler, terminal.resizeHandler = true, onInput, onResize
	terminal.lastInput = time.Now()
	terminal.readerDone = make(chan struct{})
	terminal.buffer = NewStdinBuffer(10*time.Millisecond, terminal.handleSequence, func(content string) {
		terminal.dispatchInput(bracketedPasteStart + content + bracketedPasteEnd)
	})
	terminal.resizeSignals = make(chan os.Signal, 1)
	signal.Notify(terminal.resizeSignals, syscall.SIGWINCH)
	resizeSignals := terminal.resizeSignals
	reader := terminal.readInput
	done := terminal.readerDone
	buffer := terminal.buffer
	terminal.protocolPushed = true
	terminal.mu.Unlock()

	terminal.Write("\x1b[?2004h" + kittyKeyboardQuery)
	go func() {
		for range resizeSignals {
			terminal.mu.Lock()
			handler := terminal.resizeHandler
			active := terminal.started
			terminal.mu.Unlock()
			if active && handler != nil {
				handler()
			}
		}
	}()
	go func() {
		defer close(done)
		bytes := make([]byte, 4096)
		for {
			count, readErr := reader.Read(bytes)
			if count > 0 {
				terminal.mu.Lock()
				terminal.lastInput = time.Now()
				terminal.mu.Unlock()
				buffer.Process(string(bytes[:count]))
			}
			if readErr != nil {
				return
			}
		}
	}()
	return nil
}

// Run restores terminal state even when body panics, then propagates the panic.
func (terminal *ProcessTerminal) Run(onInput func(string), onResize func(), body func()) (err error) {
	if err = terminal.Start(onInput, onResize); err != nil {
		return err
	}
	defer func() {
		stopErr := terminal.Stop()
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
		if err == nil {
			err = stopErr
		}
	}()
	body()
	return nil
}

func (terminal *ProcessTerminal) handleSequence(sequence string) {
	terminal.mu.Lock()
	replay := ""
	if terminal.negotiationBuffer != "" {
		combined := terminal.negotiationBuffer + sequence
		if negotiation, ok := ParseKeyboardProtocolNegotiation(combined); ok {
			terminal.clearNegotiationBufferLocked()
			terminal.handleNegotiationLocked(negotiation)
			terminal.mu.Unlock()
			return
		}
		if isKeyboardProtocolNegotiationPrefix(combined) {
			terminal.setNegotiationBufferLocked(combined)
			terminal.mu.Unlock()
			return
		}
		replay = terminal.negotiationBuffer
		terminal.clearNegotiationBufferLocked()
	}
	negotiation, isNegotiation := ParseKeyboardProtocolNegotiation(sequence)
	if isNegotiation {
		terminal.handleNegotiationLocked(negotiation)
	} else if isKeyboardProtocolNegotiationPrefix(sequence) {
		terminal.setNegotiationBufferLocked(sequence)
	}
	terminal.mu.Unlock()
	if replay != "" {
		terminal.dispatchInput(replay)
	}
	if isNegotiation || isKeyboardProtocolNegotiationPrefix(sequence) {
		return
	}
	terminal.dispatchInput(sequence)
}

func (terminal *ProcessTerminal) handleNegotiationLocked(negotiation KeyboardProtocolNegotiation) {
	if negotiation.Type == "kitty-flags" {
		if negotiation.Flags != 0 {
			terminal.kitty = true
			SetKittyProtocolActive(true)
			terminal.disableModifyOtherLocked()
		} else {
			terminal.enableModifyOtherLocked()
		}
	} else if !terminal.kitty {
		terminal.enableModifyOtherLocked()
	}
}

func (terminal *ProcessTerminal) setNegotiationBufferLocked(sequence string) {
	if terminal.negotiationTimer != nil {
		terminal.negotiationTimer.Stop()
	}
	terminal.negotiationBuffer = sequence
	terminal.negotiationTimer = time.AfterFunc(keyboardProtocolFragmentTimeout, func() {
		terminal.mu.Lock()
		buffered := terminal.negotiationBuffer
		terminal.negotiationBuffer, terminal.negotiationTimer = "", nil
		terminal.mu.Unlock()
		if buffered != "" {
			terminal.dispatchInput(buffered)
		}
	})
}

func (terminal *ProcessTerminal) clearNegotiationBufferLocked() {
	if terminal.negotiationTimer != nil {
		terminal.negotiationTimer.Stop()
		terminal.negotiationTimer = nil
	}
	terminal.negotiationBuffer = ""
}

func (terminal *ProcessTerminal) dispatchInput(sequence string) {
	terminal.mu.Lock()
	handler := terminal.inputHandler
	active := terminal.started
	terminal.mu.Unlock()
	if active && handler != nil {
		handler(NormalizeAppleTerminalInput(sequence, false, false))
	}
}

func (terminal *ProcessTerminal) enableModifyOtherLocked() {
	if terminal.kitty || terminal.modifyOther {
		return
	}
	terminal.writeLocked("\x1b[>4;2m")
	terminal.modifyOther = true
}

func (terminal *ProcessTerminal) disableModifyOtherLocked() {
	if !terminal.modifyOther {
		return
	}
	terminal.writeLocked("\x1b[>4;0m")
	terminal.modifyOther = false
}

func (terminal *ProcessTerminal) Stop() error {
	terminal.mu.Lock()
	if !terminal.started {
		terminal.mu.Unlock()
		return nil
	}
	terminal.started = false
	if terminal.progressStop != nil {
		close(terminal.progressStop)
		terminal.progressStop = nil
		terminal.writeLocked(progressClear)
	}
	terminal.writeLocked("\x1b[?2004l")
	terminal.clearNegotiationBufferLocked()
	if terminal.protocolPushed || terminal.kitty {
		terminal.writeLocked("\x1b[<u")
	}
	terminal.protocolPushed, terminal.kitty = false, false
	SetKittyProtocolActive(false)
	terminal.disableModifyOtherLocked()
	if terminal.buffer != nil {
		terminal.buffer.Close()
		terminal.buffer = nil
	}
	if terminal.resizeSignals != nil {
		signal.Stop(terminal.resizeSignals)
		close(terminal.resizeSignals)
		terminal.resizeSignals = nil
	}
	if terminal.readInput != nil {
		_ = terminal.readInput.Close()
		terminal.readInput = nil
	}
	state, input, done := terminal.rawState, terminal.input, terminal.readerDone
	terminal.rawState, terminal.readerDone, terminal.inputHandler, terminal.resizeHandler = nil, nil, nil, nil
	terminal.mu.Unlock()
	var restoreErr error
	if state != nil {
		restoreErr = term.Restore(int(input.Fd()), state)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
		}
	}
	return restoreErr
}

func (terminal *ProcessTerminal) DrainInput(maxDuration, idleDuration time.Duration) {
	if maxDuration <= 0 {
		maxDuration = time.Second
	}
	if idleDuration <= 0 {
		idleDuration = 50 * time.Millisecond
	}
	terminal.mu.Lock()
	terminal.clearNegotiationBufferLocked()
	if terminal.protocolPushed || terminal.kitty {
		terminal.writeLocked("\x1b[<u")
		terminal.protocolPushed, terminal.kitty = false, false
		SetKittyProtocolActive(false)
	}
	terminal.disableModifyOtherLocked()
	previousHandler := terminal.inputHandler
	terminal.inputHandler = nil
	terminal.lastInput = time.Now()
	terminal.mu.Unlock()
	deadline := time.Now().Add(maxDuration)
	for {
		terminal.mu.Lock()
		lastInput := terminal.lastInput
		terminal.mu.Unlock()
		now := time.Now()
		remaining, idleRemaining := time.Until(deadline), idleDuration-now.Sub(lastInput)
		if remaining <= 0 || idleRemaining <= 0 {
			break
		}
		time.Sleep(min(10*time.Millisecond, remaining, idleRemaining))
	}
	terminal.mu.Lock()
	if terminal.started && terminal.inputHandler == nil {
		terminal.inputHandler = previousHandler
	}
	terminal.mu.Unlock()
}

func (terminal *ProcessTerminal) Write(data string) {
	terminal.writeMu.Lock()
	defer terminal.writeMu.Unlock()
	terminal.writeOutput(data)
}
func (terminal *ProcessTerminal) writeOutput(data string) {
	if terminal.output != nil {
		_, _ = terminal.output.WriteString(data)
	}
	if terminal.writeLogPath != "" {
		if file, err := os.OpenFile(terminal.writeLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
			_, _ = file.WriteString(data)
			_ = file.Close()
		}
	}
}
func (terminal *ProcessTerminal) writeLocked(data string) {
	terminal.writeMu.Lock()
	defer terminal.writeMu.Unlock()
	terminal.writeOutput(data)
}

func envDimension(name string, fallback int) int {
	if parsed, err := strconv.Atoi(os.Getenv(name)); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}
func (terminal *ProcessTerminal) Columns() int {
	if terminal.output != nil {
		if width, _, err := term.GetSize(int(terminal.output.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return envDimension("COLUMNS", 80)
}
func (terminal *ProcessTerminal) Rows() int {
	if terminal.output != nil {
		if _, height, err := term.GetSize(int(terminal.output.Fd())); err == nil && height > 0 {
			return height
		}
	}
	return envDimension("LINES", 24)
}
func (terminal *ProcessTerminal) KittyProtocolActive() bool {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.kitty
}
func (terminal *ProcessTerminal) MoveBy(lines int) {
	if lines > 0 {
		terminal.Write("\x1b[" + strconv.Itoa(lines) + "B")
	} else if lines < 0 {
		terminal.Write("\x1b[" + strconv.Itoa(-lines) + "A")
	}
}
func (terminal *ProcessTerminal) HideCursor()           { terminal.Write("\x1b[?25l") }
func (terminal *ProcessTerminal) ShowCursor()           { terminal.Write("\x1b[?25h") }
func (terminal *ProcessTerminal) ClearLine()            { terminal.Write("\x1b[K") }
func (terminal *ProcessTerminal) ClearFromCursor()      { terminal.Write("\x1b[J") }
func (terminal *ProcessTerminal) ClearScreen()          { terminal.Write("\x1b[2J\x1b[H") }
func (terminal *ProcessTerminal) SetTitle(title string) { terminal.Write("\x1b]0;" + title + "\x07") }

func (terminal *ProcessTerminal) SetProgress(active bool) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if !active {
		if terminal.progressStop != nil {
			close(terminal.progressStop)
			terminal.progressStop = nil
		}
		terminal.writeLocked(progressClear)
		return
	}
	terminal.writeLocked(progressActive)
	if terminal.progressStop != nil {
		return
	}
	stop := make(chan struct{})
	terminal.progressStop = stop
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				terminal.mu.Lock()
				active := terminal.progressStop == stop
				terminal.mu.Unlock()
				if active {
					terminal.Write(progressActive)
				}
			case <-stop:
				return
			}
		}
	}()
}

func getenv(name string) string { return os.Getenv(name) }
