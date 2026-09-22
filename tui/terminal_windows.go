package tui

import (
	"os"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Upstream detects native Shift+Enter on win32 because the console reports a
// bare "\r" for it.
const nativeShiftEnterDetection = true

const (
	vkShift             = 0x10
	vkMenu              = 0x12
	vkLeftShift         = 0xA0
	vkRightShift        = 0xA1
	keyPressedMask      = 0x8000
	resizePollInterval  = 100 * time.Millisecond
	consoleInputRecords = 128
)

var (
	procGetAsyncKeyState  = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")
	procReadConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")
)

func nativeShiftPressed() bool {
	if procGetAsyncKeyState.Find() != nil {
		return false
	}
	for _, key := range []uintptr{vkShift, vkLeftShift, vkRightShift} {
		state, _, _ := procGetAsyncKeyState.Call(key)
		if uint16(state)&keyPressedMask != 0 {
			return true
		}
	}
	return false
}

// inputRecord is INPUT_RECORD with the KEY_EVENT_RECORD arm of its union.
type inputRecord struct {
	eventType       uint16
	_               uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	unicodeChar     uint16
	controlKeyState uint32
}

type terminalReader struct {
	handle  windows.Handle
	records [consoleInputRecords]inputRecord
	pending []uint16
}

// Console modes match Node's setRawMode (libuv UV_TTY_MODE_RAW sets exactly
// ENABLE_WINDOW_INPUT) plus the ENABLE_VIRTUAL_TERMINAL_INPUT upstream's native
// helper adds afterwards; output gets ENABLE_VIRTUAL_TERMINAL_PROCESSING as
// libuv enables it on tty init and never resets it.
func openTerminalReader(input, output *os.File) (*terminalReader, error) {
	handle := windows.Handle(input.Fd())
	if err := windows.SetConsoleMode(handle, windows.ENABLE_WINDOW_INPUT|windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		return nil, err
	}
	var mode uint32
	if outputHandle := windows.Handle(output.Fd()); windows.GetConsoleMode(outputHandle, &mode) == nil {
		_ = windows.SetConsoleMode(outputHandle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
	return &terminalReader{handle: handle}, nil
}

func (reader *terminalReader) wait(timeout time.Duration) (bool, error) {
	event, err := windows.WaitForSingleObject(reader.handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return false, err
	}
	return event == windows.WAIT_OBJECT_0, nil
}

// read never blocks: it only consumes records already queued. Key-up records
// are dropped except Alt releases, which carry Alt+numpad composed characters.
func (reader *terminalReader) read() (string, error) {
	var available uint32
	if err := windows.GetNumberOfConsoleInputEvents(reader.handle, &available); err != nil || available == 0 {
		return "", err
	}
	var count uint32
	ok, _, err := procReadConsoleInputW.Call(uintptr(reader.handle), uintptr(unsafe.Pointer(&reader.records[0])), uintptr(min(available, consoleInputRecords)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		return "", err
	}
	units := reader.pending
	reader.pending = nil
	for _, record := range reader.records[:count] {
		if record.eventType != windows.KEY_EVENT || record.unicodeChar == 0 {
			continue
		}
		if record.keyDown == 0 {
			if record.virtualKeyCode == vkMenu {
				units = append(units, record.unicodeChar)
			}
			continue
		}
		for range max(record.repeatCount, 1) {
			units = append(units, record.unicodeChar)
		}
	}
	if last := len(units) - 1; last >= 0 && units[last] >= 0xD800 && units[last] < 0xDC00 {
		reader.pending = []uint16{units[last]}
		units = units[:last]
	}
	return string(utf16.Decode(units)), nil
}

func (*terminalReader) close() error { return nil }

func watchTerminalResize(terminal *ProcessTerminal, notify func()) func() {
	stop := make(chan struct{})
	go guarded(func() {
		ticker := time.NewTicker(resizePollInterval)
		defer ticker.Stop()
		columns, rows := terminal.Columns(), terminal.Rows()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if nextColumns, nextRows := terminal.Columns(), terminal.Rows(); nextColumns != columns || nextRows != rows {
					columns, rows = nextColumns, nextRows
					notify()
				}
			}
		}
	})()
	return func() { close(stop) }
}
