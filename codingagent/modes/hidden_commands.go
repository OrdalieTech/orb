package modes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

func (mode *InteractiveMode) handleDebugCommand() {
	width := mode.ui.Terminal().Columns()
	height := mode.ui.Terminal().Rows()
	lines := mode.ui.Render(width)
	debugPath := filepath.Join(mode.session.InteractiveModeSettings().AgentDir, "pi-debug.log")

	data := make([]string, 0, len(lines)+len(mode.session.State().Messages)+9)
	data = append(data,
		"Debug output at "+time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		fmt.Sprintf("Terminal: %dx%d", width, height),
		fmt.Sprintf("Total lines: %d", len(lines)),
		"",
		"=== All rendered lines with visible widths ===",
	)
	for index, line := range lines {
		encoded, err := jsonwire.Marshal(line)
		if err != nil {
			panic(err)
		}
		data = append(data, fmt.Sprintf("[%d] (w=%d) %s", index, tui.VisibleWidth(line), encoded))
	}
	data = append(data, "", "=== Agent messages (JSONL) ===")
	for _, message := range mode.session.State().Messages {
		encoded, err := jsonwire.Marshal(message)
		if err != nil {
			panic(err)
		}
		data = append(data, string(encoded))
	}
	data = append(data, "")

	if err := os.MkdirAll(filepath.Dir(debugPath), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(debugPath, []byte(strings.Join(data, "\n")), 0o644); err != nil {
		panic(err)
	}

	mode.chat.AddChild(tui.NewSpacer(1))
	mode.chat.AddChild(tui.NewText(
		theme.FG("accent", "✓ Debug log written")+"\n"+theme.FG("muted", debugPath),
		1,
		1,
		nil,
	))
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) handleArminSaysHi() {
	mode.chat.AddChild(tui.NewSpacer(1))
	var component *ArminComponent
	if mode.arminRandom != nil || mode.arminScheduler != nil {
		scheduler := mode.arminScheduler
		if scheduler == nil {
			scheduler = scheduleArminAnimation
		}
		component = newArminComponentWithHooks(mode.ui, mode.arminRandom, scheduler)
	} else {
		component = NewArminComponent(mode.ui)
	}
	mode.chat.AddChild(component)
	mode.ui.RequestRender()
}

func (mode *InteractiveMode) handleDementedDelves() {
	mode.chat.AddChild(tui.NewSpacer(1))
	mode.chat.AddChild(NewEarendilAnnouncementComponent())
	mode.ui.RequestRender()
}
