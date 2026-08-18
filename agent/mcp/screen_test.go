package mcp

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/tui"
)

func TestStatusWindowRendersConfiguredServers(t *testing.T) {
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "files", Command: "mcp-files", Args: []string{"--root"}}})
	panel := &mcpPanel{manager: manager, theme: extensions.NewNoopUI().Theme(), list: tui.NewGridList(nil, 8, tui.GridListTheme{})}
	panel.frame = tui.NewFrame("MCP servers", "", nil, nil, panelChild{panel})
	joined := strings.Join(panel.Render(70), "\n")
	for _, fragment := range []string{"MCP servers", "files", "stopped", "mcp-files --root"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("window lacks %q:\n%s", fragment, joined)
		}
	}
}
