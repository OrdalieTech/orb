package main

import (
	"cmp"
	"os"
	"slices"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// shell is the terminal where a conversation runs: a login shell in its folder on this computer, a
// peer's through `orb bridge shell`. It lives while its conversation is open; hidden, it keeps its
// state.
type shell struct {
	term *terminal.Terminal
	err  string // why it did not start
}

func (s *shell) close() {
	if s.term != nil {
		_ = s.term.Close()
	}
}

// terminalTheme is the app's paper and ink, the rupture for the cursor, over the palette of
// programs the plugin's own theme has.
func terminalTheme(base *terminal.Theme, pal palette) *terminal.Theme {
	base.Foreground, base.Background, base.Cursor, base.CursorText = pal.fg, pal.bg, rupture, pal.bg
	base.Selection = pal.fg.Alpha(0.25)
	return base
}

func (a *app) newShell(t *Tab) *shell {
	dir := t.CWD
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = "" // gone since: the home directory
	}
	opts := terminal.Options{
		Dir:       dir,
		Env:       termEnv(),
		Font:      terminal.Font{Family: mono + ", monospace", Size: small},
		Theme:     terminalTheme(terminal.LightTheme(), light),
		DarkTheme: terminalTheme(terminal.DarkTheme(), dark),
		Cursor:    terminal.CursorBar,
	}
	if t.Remote {
		opts.Command, opts.Dir = []string{binary(), "bridge", "shell", t.Peer, t.CWD}, ""
	}
	s := &shell{}
	id := t.ID
	// A shell that exits closes its pane.
	opts.OnExit = func(int) {
		a.o.win.Update(func() {
			if a.shells[id] == s {
				delete(a.shells, id)
				for _, x := range a.root.tiles(nil) {
					if x.tab == id && x.shell {
						a.unsplit(x)
					}
				}
			}
		})
	}
	term, err := terminal.New(opts)
	if err != nil {
		s.err = err.Error()
	}
	s.term = term
	return s
}

// terminal is the tile of a conversation's terminal: where it runs, and the terminal filling the
// rest. Closed, its shell keeps its state while the conversation is open.
func (a *app) terminal(c *ui.Context, t *Tab, x *tile) {
	pal := p(c)
	s := a.shells[t.ID]
	if s == nil {
		s = a.newShell(t)
		a.shells[t.ID] = s
	}
	ui.Column(c).Fill().Background(pal.bg).Children(func() {
		a.titleBar(c, func() {
			at := device
			if t.Remote {
				at = t.Where
			}
			a.carry(label(c, "terminal").TextColor(pal.meta), carried{tile: x})
			ui.Text(c, at+" · "+cmp.Or(tidy(t.CWD), "~")).Grow(1).Basis(0).MinWidth(0).FontSize(12).TextColor(pal.meta).SingleLine()
		})
		if s.term == nil {
			ui.Text(c, s.err).FontSize(small).TextColor(rupture).Padding(8, margin)
			return
		}
		terminal.View(c.Key(t.ID), s.term).Grow(1).Basis(0).FillWidth().AutoFocus()
	})
}

// termEnv is the login shell's environment but what describes a terminal: the plugin says what it is.
func termEnv() []string {
	return slices.DeleteFunc(slices.Clone(environment()), func(kv string) bool {
		return strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM=")
	})
}
