package main

import (
	"cmp"
	"regexp"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// promptPattern finds what the box draws as chips while you write; tokens stay plain text on the wire.
var promptPattern = regexp.MustCompile(`^/[\w:.-]+|(^|\s)(/skill:[\w.-]+|@[^\s]+|#[\w-]+)`)

// promptBox is where a message is written: the slash palette and @ completions above it, where it
// runs and its model below, and send (stop, queue or steer while it works). t is nil on Home.
func (a *app) promptBox(c *ui.Context, t *Tab, placeholder string) {
	o, pal := a.o, p(c)
	id, busy := "", false
	if t != nil {
		id, busy = t.ID, t.Busy
	}
	draft := a.drafts[id]
	take := func() string {
		text := strings.TrimSpace(draft)
		draft = ""
		a.drafts[id] = ""
		return text
	}
	send := func(text string) {
		if text == "" {
			return
		}
		if ls := a.lists[id]; ls != nil {
			ls.ScrollToEnd()
		}
		a.send(t, text)
	}
	box := ui.Column(c).FillWidth().MaxWidth(column).Margin(0, ui.Auto, 12, ui.Auto).Radius(pane+4).Background(pal.raised).Border(1, pal.rule)
	box.Transition(ui.ElementTransition{Size: true, Animation: &ui.Snappy})
	box.Children(func() {
		// Typing / opens the palette: the app's commands and the core's, filtered as you type.
		type choice struct {
			name, hint string
			pick       func()
		}
		var choices []choice
		if q, ok := strings.CutPrefix(draft, "/"); ok && !strings.ContainsFunc(draft, unicode.IsSpace) {
			commands := append(append([]Command(nil), o.state.Commands...), tabCommands(t)...)
			for _, pass := range []bool{true, false} {
				for _, cm := range commands {
					if starts := strings.HasPrefix(strings.ToLower(cm.Name), strings.ToLower(q)); starts == pass && (pass || strings.Contains(strings.ToLower(cm.Name), strings.ToLower(q))) {
						choices = append(choices, choice{"/" + cm.Name, cm.Hint, func() {
							if cm.Now {
								draft = ""
								send("/" + cm.Name)
							} else {
								draft = "/" + cm.Name + " "
							}
						}})
					}
				}
			}
		}
		// An @ token asks the Orb what it completes to, as the TUI does: skills, then files.
		word := draft[strings.LastIndexFunc(draft, unicode.IsSpace)+1:]
		at, citing := strings.CutPrefix(word, "@")
		if !citing || t == nil {
			a.found, a.foundFor = nil, ""
		} else if word != a.foundFor {
			a.foundFor = word
			then(o, func(items []Completion, err error) {
				if a.foundFor == word {
					a.found = items
				}
			}, "complete", "tab", t.ID, "text", at)
		}
		if len(choices) == 0 {
			for _, f := range a.found {
				name := f.Label
				if strings.HasPrefix(f.Text, "/skill:") {
					name = "◆ " + name
				}
				choices = append(choices, choice{name, f.Detail, func() {
					// A folder keeps the token open, so completion goes on inside it.
					gap := " "
					if strings.HasSuffix(f.Text, "/") || strings.HasSuffix(f.Text, `/"`) {
						gap = ""
					}
					draft = strings.TrimSuffix(draft, word) + f.Text + gap
				}})
			}
		}
		if len(choices) > 0 {
			ui.Scroll(c).FillWidth().MaxHeight(220).PaddingY(4).Children(func() {
				for _, ch := range choices {
					row := ui.Row(c.Key(ch.name)).FillWidth().Padding(7, 18).Gap(12).Children(func() {
						ui.Text(c, ch.name).FontSize(15).FontWeight(strong).SingleLine()
						ui.Text(c, ch.hint).Grow(1).Basis(0).FontSize(small).TextColor(pal.meta).SingleLine()
					})
					if row.Hovered() {
						row.Background(pal.bg)
					}
					if press(row) {
						ch.pick()
					}
				}
			})
		}
		hint := placeholder
		if busy {
			hint = "steer or queue a message"
		}
		in := ui.TextAreaBase(c.Key("prompt"+id), &draft).Lines(1, 12).FillWidth().Padding(14, 16, 6).FontSize(size + 1).
			Placeholder(hint).PlaceholderColor(pal.meta).AutoFocus()
		for _, m := range promptPattern.FindAllStringSubmatchIndex(draft, -1) {
			start, end := m[0], m[1]
			if m[4] >= 0 {
				start, end = m[4], m[5]
			}
			r := ui.TextRange{Start: len([]rune(draft[:start])), End: len([]rune(draft[:end])), Weight: medium}
			switch draft[start] {
			case '/':
				r.Weight = strong
			case '#':
				r.Color = pal.meta
			}
			in.TextRanges(r)
		}
		// Enter sends, Shift+Enter breaks the line; Tab takes the first choice.
		in.OnTextKey(func(k ui.TextKey) bool {
			switch {
			case k.Key == ui.KeyEnter && k.Mods == 0:
				text := strings.TrimSpace(k.Text())
				k.SetText("")
				a.drafts[id] = ""
				send(text)
				return true
			case k.Key == ui.KeyTab && k.Mods == 0 && len(choices) > 0:
				draft = k.Text()
				choices[0].pick()
				k.SetText(draft)
				a.drafts[id] = draft
				return true
			}
			return false
		})
		// Files dropped on the box are cited, as the TUI cites them with @.
		for _, f := range in.DroppedFiles() {
			draft = strings.TrimRight(draft, " ") + " @" + quotePath(f) + " "
		}
		if a.focus != a.focused {
			a.focused = a.focus
			in.Focus()
		}
		ui.Row(c).FillWidth().Padding(2, 8, 8, 14).Gap(12).Children(func() {
			peer := o.state.Self
			if t != nil {
				peer = t.Peer
			}
			wh := ui.Row(c).PaddingY(6).Gap(7).Children(func() {
				where(c, o.hue(peer), false, false, pal.fg)
				name := device
				if t != nil && t.Remote {
					name = t.Where
				}
				if len([]rune(name)) > 16 {
					name = string([]rune(name)[:15]) + "…"
				}
				ui.Text(c, name+" ▾").FontWeight(strong).SingleLine()
			})
			if t == nil {
				if press(wh.Tooltip("Where it runs")) {
					a.chooseWhere()
				}
			} else {
				wh.Tooltip(cmp.Or(t.CWD, t.Where))
			}
			model := ui.Row(c).Grow(1).Basis(0).PaddingY(6).Gap(8).Children(func() {
				name := "model"
				if t != nil {
					_, id, _ := strings.Cut(t.Model, "/")
					name = cmp.Or(id, t.Model, name)
				}
				ui.Text(c, name+" ▾").FontWeight(medium).TextColor(pal.mute).SingleLine().Shrink(1)
				// Reasoning as a small meter: one bar per level the model takes, lit up to the current one.
				if t != nil && len(t.Levels) > 0 {
					now := -1
					for i, l := range t.Levels {
						if l == t.Thinking {
							now = i
						}
					}
					ui.Row(c).Gap(2).AlignItems(ui.End).Children(func() {
						for i, l := range t.Levels {
							ink := pal.rule
							if i <= now && l != "off" {
								ink = pal.fg
							}
							ui.Box(c.Key(l)).Size(3, float32(5+2*i)).Background(ink)
						}
					})
				}
			})
			if press(model.Tooltip("Model and reasoning  " + keys("⇧M"))) {
				a.chooseModel(t)
			}
			switch {
			case busy && strings.TrimSpace(draft) == "":
				if btn(c, "stop", false, rupture, pal.bg) {
					o.send("abort", "tab", id)
				}
			case busy:
				if btn(c, "queue", false, pal.fg, pal.bg) {
					send(take())
				}
				if btn(c, "steer", true, pal.fg, pal.bg) {
					if text := take(); text != "" {
						o.send("steer", "tab", id, "text", text)
					}
				}
			default:
				if btn(c, "send", true, pal.fg, pal.bg) {
					send(take())
				}
			}
		})
	})
	a.drafts[id] = draft
}

func tabCommands(t *Tab) []Command {
	if t == nil {
		return nil
	}
	return t.Commands
}

// quotePath cites a path the way the TUI does: quoted when it holds a space.
func quotePath(p string) string {
	if strings.ContainsFunc(p, unicode.IsSpace) {
		return `"` + p + `"`
	}
	return p
}
