package tui

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)

type sequenceStatus uint8

const (
	sequenceComplete sequenceStatus = iota
	sequenceIncomplete
	sequencePlain
)

type stdinEventKind uint8

const (
	stdinDataEvent stdinEventKind = iota
	stdinPasteEvent
	stdinResetKittyEvent
)

type stdinEvent struct {
	kind  stdinEventKind
	value string
}

func completeSequence(data string) sequenceStatus {
	if !strings.HasPrefix(data, "\x1b") {
		return sequencePlain
	}
	if len(data) == 1 {
		return sequenceIncomplete
	}
	switch data[1] {
	case '[':
		if strings.HasPrefix(data, "\x1b[M") {
			if len(data) >= 6 {
				return sequenceComplete
			}
			return sequenceIncomplete
		}
		if len(data) < 3 {
			return sequenceIncomplete
		}
		payload := data[2:]
		final := payload[len(payload)-1]
		if final < 0x40 || final > 0x7e {
			return sequenceIncomplete
		}
		if payload[0] == '<' {
			body := payload[1 : len(payload)-1]
			parts := strings.Split(body, ";")
			if (final == 'M' || final == 'm') && len(parts) == 3 {
				for _, part := range parts {
					if part == "" {
						return sequenceIncomplete
					}
					if _, err := strconv.Atoi(part); err != nil {
						return sequenceIncomplete
					}
				}
				return sequenceComplete
			}
			return sequenceIncomplete
		}
		return sequenceComplete
	case ']':
		if strings.HasSuffix(data, "\a") || strings.HasSuffix(data, "\x1b\\") {
			return sequenceComplete
		}
		return sequenceIncomplete
	case 'P', '_':
		if strings.HasSuffix(data, "\x1b\\") {
			return sequenceComplete
		}
		return sequenceIncomplete
	case 'O':
		if len(data) >= 3 {
			return sequenceComplete
		}
		return sequenceIncomplete
	default:
		if !utf8.FullRuneInString(data[1:]) {
			return sequenceIncomplete
		}
		return sequenceComplete
	}
}

func extractCompleteSequences(buffer string) ([]string, string) {
	sequences := make([]string, 0)
	for pos := 0; pos < len(buffer); {
		remaining := buffer[pos:]
		if remaining[0] != '\x1b' {
			if !utf8.FullRuneInString(remaining) {
				return sequences, remaining
			}
			_, size := utf8.DecodeRuneInString(remaining)
			sequences = append(sequences, remaining[:size])
			pos += size
			continue
		}
		completed := false
		for end := 1; end <= len(remaining); end++ {
			candidate := remaining[:end]
			switch completeSequence(candidate) {
			case sequenceComplete:
				if candidate == "\x1b\x1b" && end < len(remaining) && strings.ContainsRune("[]OP_", rune(remaining[end])) {
					sequences = append(sequences, "\x1b")
					pos++
					completed = true
					break
				}
				sequences = append(sequences, candidate)
				pos += end
				completed = true
			case sequencePlain:
				sequences = append(sequences, candidate)
				pos += end
				completed = true
			}
			if completed {
				break
			}
		}
		if !completed {
			return sequences, remaining
		}
	}
	return sequences, ""
}

var unmodifiedKittyPrintable = regexp.MustCompile(`^\x1b\[([0-9]+)(:[0-9]*)?(:[0-9]+)?u$`)

func kittyPrintableCodepoint(sequence string) (rune, bool) {
	match := unmodifiedKittyPrintable.FindStringSubmatch(sequence)
	if match == nil {
		return 0, false
	}
	codepoint := number(match[1], 0)
	if codepoint < 32 || !utf8.ValidRune(rune(codepoint)) {
		return 0, false
	}
	return rune(codepoint), true
}

// StdinBuffer turns arbitrarily chunked terminal bytes into individual escape
// sequences and bracketed-paste payloads.
type StdinBuffer struct {
	mu           sync.Mutex
	timeout      time.Duration
	buffer       string
	timer        *time.Timer
	pasteMode    bool
	pasteBuffer  string
	pendingKitty rune
	onData       func(string)
	onPaste      func(string)
}

func NewStdinBuffer(timeout time.Duration, onData, onPaste func(string)) *StdinBuffer {
	if timeout <= 0 {
		timeout = 10 * time.Millisecond
	}
	return &StdinBuffer{timeout: timeout, onData: onData, onPaste: onPaste}
}

// ProcessBytes preserves upstream's Buffer input compatibility: a single
// high byte is decoded as the legacy meta-key form ESC + (byte - 128).
func (buffer *StdinBuffer) ProcessBytes(data []byte) {
	if len(data) == 1 && data[0] > 127 {
		buffer.Process(string([]byte{'\x1b', data[0] - 128}))
		return
	}
	buffer.Process(string(data))
}

func (buffer *StdinBuffer) Process(data string) {
	buffer.mu.Lock()
	if buffer.timer != nil {
		buffer.timer.Stop()
		buffer.timer = nil
	}
	if data == "" && buffer.buffer == "" {
		callback := buffer.onData
		buffer.mu.Unlock()
		if callback != nil {
			callback("")
		}
		return
	}
	buffer.buffer += data
	events := buffer.processLocked()
	if buffer.buffer != "" {
		buffer.timer = time.AfterFunc(buffer.timeout, func() {
			for _, sequence := range buffer.Flush() {
				buffer.emitData(sequence)
			}
		})
	}
	buffer.mu.Unlock()
	for _, event := range events {
		switch event.kind {
		case stdinDataEvent:
			buffer.emitData(event.value)
		case stdinPasteEvent:
			buffer.emitPaste(event.value)
		case stdinResetKittyEvent:
			buffer.resetPendingKitty()
		}
	}
}

func (buffer *StdinBuffer) processLocked() []stdinEvent {
	if buffer.pasteMode {
		buffer.pasteBuffer += buffer.buffer
		buffer.buffer = ""
		if end := strings.Index(buffer.pasteBuffer, bracketedPasteEnd); end >= 0 {
			events := []stdinEvent{{kind: stdinPasteEvent, value: buffer.pasteBuffer[:end]}}
			remaining := buffer.pasteBuffer[end+len(bracketedPasteEnd):]
			buffer.pasteMode, buffer.pasteBuffer = false, ""
			buffer.buffer = remaining
			events = append(events, buffer.processLocked()...)
			return events
		}
		return nil
	}
	if start := strings.Index(buffer.buffer, bracketedPasteStart); start >= 0 {
		events := make([]stdinEvent, 0)
		if start > 0 {
			sequences, _ := extractCompleteSequences(buffer.buffer[:start])
			for _, sequence := range sequences {
				events = append(events, stdinEvent{kind: stdinDataEvent, value: sequence})
			}
		}
		events = append(events, stdinEvent{kind: stdinResetKittyEvent})
		buffer.buffer = buffer.buffer[start+len(bracketedPasteStart):]
		buffer.pasteMode = true
		buffer.pasteBuffer = buffer.buffer
		buffer.buffer = ""
		if end := strings.Index(buffer.pasteBuffer, bracketedPasteEnd); end >= 0 {
			events = append(events, stdinEvent{kind: stdinPasteEvent, value: buffer.pasteBuffer[:end]})
			remaining := buffer.pasteBuffer[end+len(bracketedPasteEnd):]
			buffer.pasteMode, buffer.pasteBuffer = false, ""
			buffer.buffer = remaining
			events = append(events, buffer.processLocked()...)
		}
		return events
	}
	sequences, remainder := extractCompleteSequences(buffer.buffer)
	buffer.buffer = remainder
	events := make([]stdinEvent, 0, len(sequences))
	for _, sequence := range sequences {
		events = append(events, stdinEvent{kind: stdinDataEvent, value: sequence})
	}
	return events
}

func (buffer *StdinBuffer) emitData(sequence string) {
	buffer.mu.Lock()
	runes := []rune(sequence)
	if len(runes) == 1 && runes[0] == buffer.pendingKitty {
		buffer.pendingKitty = 0
		buffer.mu.Unlock()
		return
	}
	buffer.pendingKitty = 0
	if codepoint, ok := kittyPrintableCodepoint(sequence); ok {
		buffer.pendingKitty = codepoint
	}
	callback := buffer.onData
	buffer.mu.Unlock()
	if callback != nil {
		callback(sequence)
	}
}

func (buffer *StdinBuffer) emitPaste(content string) {
	buffer.mu.Lock()
	buffer.pendingKitty = 0
	callback := buffer.onPaste
	buffer.mu.Unlock()
	if callback != nil {
		callback(content)
	}
}

func (buffer *StdinBuffer) resetPendingKitty() {
	buffer.mu.Lock()
	buffer.pendingKitty = 0
	buffer.mu.Unlock()
}

func (buffer *StdinBuffer) Flush() []string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.timer != nil {
		buffer.timer.Stop()
		buffer.timer = nil
	}
	if buffer.buffer == "" {
		return nil
	}
	result := []string{buffer.buffer}
	buffer.buffer, buffer.pendingKitty = "", 0
	return result
}

func (buffer *StdinBuffer) Clear() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.timer != nil {
		buffer.timer.Stop()
		buffer.timer = nil
	}
	buffer.buffer, buffer.pasteBuffer, buffer.pendingKitty = "", "", 0
	buffer.pasteMode = false
}

func (buffer *StdinBuffer) Close() { buffer.Clear() }
func (buffer *StdinBuffer) Buffered() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer
}
