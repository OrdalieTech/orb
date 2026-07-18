package tui

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	osc11BackgroundQuery                = "\x1b]11;?\x07"
	terminalColorSchemeQuery            = "\x1b[?996n"
	terminalColorSchemeNotificationsOn  = "\x1b[?2031h"
	terminalColorSchemeNotificationsOff = "\x1b[?2031l"
)

// RgbColor is an 8-bit terminal color parsed from an OSC 11 response.
type RgbColor struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// TerminalColorScheme is a terminal-reported dark or light preference.
type TerminalColorScheme string

const (
	TerminalColorSchemeDark  TerminalColorScheme = "dark"
	TerminalColorSchemeLight TerminalColorScheme = "light"
)

type pendingOsc11BackgroundQuery struct {
	settled bool
	result  chan *RgbColor
	timer   *time.Timer
}

type terminalColorSchemeListenerEntry struct {
	id       uint64
	listener func(TerminalColorScheme)
}

func parseOscHexChannel(channel string) (int, bool) {
	if channel == "" {
		return 0, false
	}
	for _, character := range channel {
		if character < '0' || character > '9' {
			lower := character | 0x20
			if lower < 'a' || lower > 'f' {
				return 0, false
			}
		}
	}
	value, err := strconv.ParseUint(channel, 16, 64)
	if err != nil {
		return 0, false
	}
	maximum := math.Pow(16, float64(len(channel))) - 1
	if maximum <= 0 || math.IsInf(maximum, 0) {
		return 0, false
	}
	return int(math.Round(float64(value) / maximum * 255)), true
}

func osc11Payload(data string) (string, bool) {
	const prefix = "\x1b]11;"
	if !strings.HasPrefix(data, prefix) {
		return "", false
	}
	var payload string
	switch {
	case strings.HasSuffix(data, "\x07"):
		payload = data[len(prefix) : len(data)-1]
	case strings.HasSuffix(data, "\x1b\\"):
		payload = data[len(prefix) : len(data)-2]
	default:
		return "", false
	}
	if strings.ContainsAny(payload, "\x07\x1b") {
		return "", false
	}
	return payload, true
}

// IsOsc11BackgroundColorResponse recognizes the strict OSC 11 reply frame,
// including payloads that do not contain a parseable color.
func IsOsc11BackgroundColorResponse(data string) bool {
	_, ok := osc11Payload(data)
	return ok
}

// ParseOsc11BackgroundColor parses #RRGGBB, #RRRRGGGGBBBB, rgb:, and rgba:
// responses, scaling arbitrary hexadecimal channel widths to 8-bit values.
func ParseOsc11BackgroundColor(data string) (RgbColor, bool) {
	payload, ok := osc11Payload(data)
	if !ok {
		return RgbColor{}, false
	}
	value := strings.TrimSpace(payload)
	if strings.HasPrefix(value, "#") {
		hex := value[1:]
		if len(hex) == 6 {
			parsed, err := strconv.ParseUint(hex, 16, 32)
			if err != nil {
				return RgbColor{}, false
			}
			return RgbColor{R: int(parsed >> 16), G: int(parsed >> 8 & 0xff), B: int(parsed & 0xff)}, true
		}
		if len(hex) != 12 {
			return RgbColor{}, false
		}
		red, redOK := parseOscHexChannel(hex[:4])
		green, greenOK := parseOscHexChannel(hex[4:8])
		blue, blueOK := parseOscHexChannel(hex[8:])
		return RgbColor{R: red, G: green, B: blue}, redOK && greenOK && blueOK
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "rgba:") {
		value = value[len("rgba:"):]
	} else if strings.HasPrefix(lower, "rgb:") {
		value = value[len("rgb:"):]
	}
	channels := strings.Split(value, "/")
	if len(channels) < 3 {
		return RgbColor{}, false
	}
	red, redOK := parseOscHexChannel(channels[0])
	green, greenOK := parseOscHexChannel(channels[1])
	blue, blueOK := parseOscHexChannel(channels[2])
	return RgbColor{R: red, G: green, B: blue}, redOK && greenOK && blueOK
}

// ParseTerminalColorSchemeReport parses CSI ? 997 ; 1/2 n reports.
func ParseTerminalColorSchemeReport(data string) (TerminalColorScheme, bool) {
	switch data {
	case "\x1b[?997;1n":
		return TerminalColorSchemeDark, true
	case "\x1b[?997;2n":
		return TerminalColorSchemeLight, true
	default:
		return "", false
	}
}

// QueryTerminalBackgroundColor sends OSC 11 and returns a one-result channel.
// A nil result means timeout or a strict reply with an unparseable payload.
func (ui *TUI) QueryTerminalBackgroundColor(timeout time.Duration) <-chan *RgbColor {
	query := &pendingOsc11BackgroundQuery{result: make(chan *RgbColor, 1)}
	ui.colorMu.Lock()
	ui.pendingOsc11BackgroundQueries = append(ui.pendingOsc11BackgroundQueries, query)
	ui.pendingOsc11BackgroundReplies++
	query.timer = time.AfterFunc(timeout, func() {
		ui.colorMu.Lock()
		defer ui.colorMu.Unlock()
		if query.settled {
			return
		}
		query.settled = true
		query.timer = nil
		query.result <- nil
		close(query.result)
	})
	ui.colorMu.Unlock()
	ui.terminal.Write(osc11BackgroundQuery)
	return query.result
}

func (ui *TUI) consumeOsc11BackgroundResponse(data string) bool {
	ui.colorMu.Lock()
	defer ui.colorMu.Unlock()
	if ui.pendingOsc11BackgroundReplies <= 0 || !IsOsc11BackgroundColorResponse(data) {
		return false
	}
	color, parsed := ParseOsc11BackgroundColor(data)
	ui.pendingOsc11BackgroundReplies--
	var query *pendingOsc11BackgroundQuery
	if len(ui.pendingOsc11BackgroundQueries) > 0 {
		query = ui.pendingOsc11BackgroundQueries[0]
		ui.pendingOsc11BackgroundQueries = ui.pendingOsc11BackgroundQueries[1:]
	}
	if query == nil || query.settled {
		return true
	}
	query.settled = true
	if query.timer != nil {
		query.timer.Stop()
		query.timer = nil
	}
	if parsed {
		result := color
		query.result <- &result
	} else {
		query.result <- nil
	}
	close(query.result)
	return true
}

// OnTerminalColorSchemeChange registers an insertion-ordered scheme listener.
func (ui *TUI) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	ui.colorMu.Lock()
	ui.nextTerminalColorSchemeListener++
	id := ui.nextTerminalColorSchemeListener
	ui.terminalColorSchemeListeners = append(ui.terminalColorSchemeListeners, terminalColorSchemeListenerEntry{id: id, listener: listener})
	ui.colorMu.Unlock()
	return func() {
		ui.colorMu.Lock()
		defer ui.colorMu.Unlock()
		for index, entry := range ui.terminalColorSchemeListeners {
			if entry.id == id {
				ui.terminalColorSchemeListeners = append(ui.terminalColorSchemeListeners[:index], ui.terminalColorSchemeListeners[index+1:]...)
				return
			}
		}
	}
}

func (ui *TUI) consumeTerminalColorSchemeReport(data string) bool {
	scheme, ok := ParseTerminalColorSchemeReport(data)
	if !ok {
		return false
	}
	var lastID uint64
	for {
		ui.colorMu.Lock()
		var next terminalColorSchemeListenerEntry
		found := false
		for _, entry := range ui.terminalColorSchemeListeners {
			if entry.id > lastID {
				next, found = entry, true
				break
			}
		}
		ui.colorMu.Unlock()
		if !found {
			break
		}
		lastID = next.id
		next.listener(scheme)
	}
	return true
}

// QueryTerminalColorScheme sends the DSR query and resolves from the same
// notification path used by persistent listeners. An empty result is timeout.
func (ui *TUI) QueryTerminalColorScheme(timeout time.Duration) <-chan TerminalColorScheme {
	result := make(chan TerminalColorScheme, 1)
	var stateMu sync.Mutex
	settled := false
	var timer *time.Timer
	var unsubscribe func()
	settle := func(scheme TerminalColorScheme) {
		stateMu.Lock()
		if settled {
			stateMu.Unlock()
			return
		}
		settled = true
		currentTimer, currentUnsubscribe := timer, unsubscribe
		stateMu.Unlock()
		if currentTimer != nil {
			currentTimer.Stop()
		}
		if currentUnsubscribe != nil {
			currentUnsubscribe()
		}
		result <- scheme
		close(result)
	}
	registeredUnsubscribe := ui.OnTerminalColorSchemeChange(settle)
	stateMu.Lock()
	unsubscribe = registeredUnsubscribe
	alreadySettled := settled
	stateMu.Unlock()
	if alreadySettled {
		registeredUnsubscribe()
	}
	stateMu.Lock()
	if settled {
		stateMu.Unlock()
		ui.terminal.Write(terminalColorSchemeQuery)
		return result
	}
	timer = time.AfterFunc(timeout, func() { settle("") })
	stateMu.Unlock()
	ui.terminal.Write(terminalColorSchemeQuery)
	return result
}

// SetTerminalColorSchemeNotifications enables or disables CSI ? 2031 reports.
func (ui *TUI) SetTerminalColorSchemeNotifications(enabled bool) {
	ui.notificationMu.Lock()
	defer ui.notificationMu.Unlock()
	ui.colorMu.Lock()
	if ui.terminalColorSchemeNotificationsEnabled == enabled {
		ui.colorMu.Unlock()
		return
	}
	ui.terminalColorSchemeNotificationsEnabled = enabled
	ui.colorMu.Unlock()
	ui.lifecycleMu.RLock()
	shouldWrite := !ui.stopped || !ui.hasStarted
	ui.lifecycleMu.RUnlock()
	if shouldWrite {
		if enabled {
			ui.terminal.Write(terminalColorSchemeNotificationsOn)
		} else {
			ui.terminal.Write(terminalColorSchemeNotificationsOff)
		}
	}
}
