package modes

import (
	"encoding/json"
	"strings"

	"github.com/OrdalieTech/orb/agent/exchange"
	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

// ExchangeComponent draws one lightweight inter-agent message (Mitosis peer or
// local subagent) as a quiet left rail with a one-line header and clean body.
type ExchangeComponent struct {
	msg      exchange.Message
	pad      int
	expanded bool
}

func NewExchangeComponent(msg exchange.Message, pad int, expanded bool) *ExchangeComponent {
	return &ExchangeComponent{msg: msg, pad: pad, expanded: expanded}
}

func (c *ExchangeComponent) Invalidate() {}

func (c *ExchangeComponent) Render(width int) []string {
	if width <= 0 {
		return nil
	}
	indent := strings.Repeat(" ", max(0, c.pad))
	accentColor := "dim"
	if c.msg.Scope == exchange.ScopePeer {
		accentColor = "accent"
	}
	innerW := max(8, width-max(0, c.pad))

	arrow := "→"
	if c.msg.Dir == exchange.DirIn {
		arrow = "←"
	}
	headLeft := theme.FG(accentColor, arrow) + " " + theme.Bold(theme.FG("text", c.msg.Who))
	headRight := ""
	if c.msg.Status != "" {
		switch {
		case c.msg.Failed:
			headRight = theme.FG("error", c.msg.Status)
		case c.msg.Live:
			headRight = theme.FG("accent", "● "+c.msg.Status)
		default:
			headRight = theme.FG("dim", c.msg.Status)
		}
	}

	var lines []string
	lines = append(lines, indent+alignExchangeHeader(headLeft, headRight, innerW))

	bodyIndent := "  "
	bodyW := max(6, innerW-len(bodyIndent))
	if text := strings.TrimSpace(c.msg.Text); text != "" {
		bodyLines := tui.WrapTextWithANSI(text, bodyW)
		if !c.expanded && len(bodyLines) > 6 {
			bodyLines = append(bodyLines[:6], theme.FG("dim", "…"))
		}
		for _, line := range bodyLines {
			lines = append(lines, indent+bodyIndent+theme.FG("text", line))
		}
	}

	if c.msg.Scope == exchange.ScopeSubagent && !c.msg.Live && (c.msg.Reply != "" || c.msg.Steps != "") {
		if c.expanded {
			if c.msg.Steps != "" {
				for step := range strings.SplitSeq(c.msg.Steps, "\n") {
					if s := strings.TrimSpace(step); s != "" {
						lines = append(lines, indent+bodyIndent+theme.FG("dim", "· "+tui.TruncateToWidth(s, bodyW-2, "…", false)))
					}
				}
			}
			if c.msg.Reply != "" {
				lines = append(lines, indent+theme.FG("dim", "← "+c.msg.Who))
				for _, line := range tui.WrapTextWithANSI(c.msg.Reply, bodyW) {
					lines = append(lines, indent+bodyIndent+theme.FG("muted", line))
				}
			}
		} else if c.msg.Reply != "" {
			preview := firstExchangeLine(c.msg.Reply)
			if preview != "" {
				prefix := theme.FG("dim", "← ")
				lines = append(lines, indent+prefix+theme.FG("muted", tui.TruncateToWidth(preview, max(4, innerW-2), "…", false)))
			}
		}
	}
	return lines
}

func alignExchangeHeader(left, right string, width int) string {
	if right == "" {
		return tui.TruncateToWidth(left, width, "…", false)
	}
	rw := tui.VisibleWidth(right)
	maxLeft := width - rw - 2
	if maxLeft < 4 {
		return tui.TruncateToWidth(left, width, "…", false)
	}
	left = tui.TruncateToWidth(left, maxLeft, "…", false)
	gap := max(1, width-tui.VisibleWidth(left)-rw)
	return left + strings.Repeat(" ", gap) + right
}

func firstExchangeLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func toolExchange(c *ToolExecutionComponent) (exchange.Message, bool) {
	lower := strings.ToLower(c.toolName)
	if lower != "agents" && lower != "subagent" {
		return exchange.Message{}, false
	}
	argsMap := toStringMap(c.args)
	var detailsMap map[string]any
	if c.result != nil {
		detailsMap = toStringMap(c.result.Details)
	}
	live := c.result == nil || c.isPartial
	failed := c.result != nil && c.result.IsError
	return exchange.FromTool(c.toolName, argsMap, c.getTextOutput(), detailsMap, live, failed)
}

func toStringMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(map[string]string); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
