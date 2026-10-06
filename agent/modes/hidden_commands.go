package modes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
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
			mode.showError(fmt.Errorf("serialize debug rendered line %d: %w", index, err))
			return
		}
		data = append(data, fmt.Sprintf("[%d] (w=%d) %s", index, tui.VisibleWidth(line), encoded))
	}
	data = append(data, "", "=== Agent messages (JSONL) ===")
	for index, message := range mode.session.State().Messages {
		encoded, err := jsonwire.Marshal(message)
		if err != nil {
			mode.showError(fmt.Errorf("serialize debug message %d: %w", index, err))
			return
		}
		data = append(data, string(encoded))
	}
	data = append(data, "")

	if err := os.MkdirAll(filepath.Dir(debugPath), 0o755); err != nil {
		mode.showError(fmt.Errorf("create debug log directory %q: %w", filepath.Dir(debugPath), err))
		return
	}
	if err := os.WriteFile(debugPath, []byte(strings.Join(data, "\n")), 0o644); err != nil {
		mode.showError(fmt.Errorf("write debug log %q: %w", debugPath, err))
		return
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
