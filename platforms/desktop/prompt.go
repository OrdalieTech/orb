package main

import (
	"cmp"
	"path"
	"regexp"
	"runtime"
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
	// The box reaches past the reading column on both sides, and keeps its margin when the window
	// is narrower.
	// A short tile keeps its box to a line, leaving the room to the transcript.
	short := a.building != nil && a.building.height > 0 && a.building.height < 360
	ui.Column(c).FillWidth().PaddingX(margin).Children(func() {
		shade := map[bool]float32{true: 0.35, false: 0.07}[c.Theme().Dark]
		box := ui.Column(c).FillWidth().MaxWidth(column+2*overhang).Margin(0, ui.Auto, map[bool]float32{true: 8, false: 16}[short]).Radius(16).Background(pal.raised).
			Border(1, pal.rule.Alpha(0.7)).Shadow(0, 6, 24, 0, ui.RGBA(0, 0, 0, shade))
		if a.building != nil {
			box.Bind(&a.building.boxAt) // what the explorer floats out of
		}
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
			// Each conversation keeps its own: tiles draw several prompt boxes at once, and
			// another's completions name another folder or machine's files.
			if !citing || t == nil {
				delete(a.found, id)
				delete(a.foundFor, id)
			} else if word != a.foundFor[id] {
				a.foundFor[id] = word
				then(o, func(items []Completion, err error) {
					if a.foundFor[id] == word {
						a.found[id] = items
					}
				}, "complete", "tab", t.ID, "text", at)
			}
			if len(choices) == 0 {
				for _, f := range a.found[id] {
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
				ui.Scroll(c).FillWidth().MaxHeight(220).Padding(6, 6, 2).BorderWidth(0, 0, 1, 0).BorderColor(pal.rule.Alpha(0.6)).Children(func() {
					for _, ch := range choices {
						row := hover(ui.Row(c.Key(ch.name)).FillWidth().Padding(6, 10).Gap(12).Radius(8), pal.bg).Children(func() {
							ui.Text(c, ch.name).FontWeight(strong).SingleLine()
							ui.Text(c, ch.hint).Grow(1).Basis(0).FontSize(small).TextColor(pal.meta).SingleLine()
						})
						if press(row) {
							ch.pick()
						}
					}
				})
			}
			hint := placeholder
			if busy {
				hint = map[bool]string{true: "steer or queue", false: "steer or queue a message"}[a.narrow]
			}
			in := ui.TextAreaBase(c.Key("prompt"+id), &draft).Lines(2, 14).FillWidth().Padding(14, inset, 4)
			if short {
				in.Lines(1, 4).Padding(8, inset, 2)
			}
			in = in.FontSize(size).LineHeight(1.5).
				Placeholder(hint).PlaceholderColor(pal.meta)
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
			// Enter sends (steers a turn under way, as its button says), Shift+Enter breaks the
			// line; Tab takes the first choice.
			in.OnTextKey(func(k ui.TextKey) bool {
				switch {
				case k.Key == ui.KeyEnter && k.Mods == 0:
					text := strings.TrimSpace(k.Text())
					k.SetText("")
					a.drafts[id] = ""
					if busy && text != "" {
						o.send("steer", "tab", id, "text", text)
					} else {
						send(text)
					}
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
			if a.focus != a.focused && a.building == a.active {
				a.focused = a.focus
				in.Focus()
			}
			// Below the text, on one line: where it runs and its model, as quiet controls that open out of
			// themselves, and at right the one action, round, filled when there is something to send.
			ui.Row(c).FillWidth().Padding(0, 10, 10, inset-9).Gap(2).Children(func() {
				peer, name, full := cmp.Or(a.target[0], o.state.Self), device, "Where it runs"
				if t != nil {
					peer = t.Peer
				}
				if m := o.machine(peer); t == nil && m != nil && !m.Self {
					name = m.Name
				}
				if t == nil && a.target[1] != "" { // the folder Home starts in, and where if not here
					name, full = strings.Join(nonEmpty(path.Base(a.target[1]), map[bool]string{true: name}[name != device]), " · "), tidy(a.target[1])
				}
				wh := control(c, func() {
					where(c, o.hue(peer), false, false, pal.fg)
					if t != nil && t.Remote {
						name = t.Where
					}
					// A narrow tile leaves the model the room: its square says where.
					if !a.narrow {
						ui.Text(c, name).FontSize(small).FontWeight(medium).TextColor(pal.mute).SingleLine().MaxWidth(150)
					}
					if t == nil {
						caretDown(c)
					}
				}).Bind(&a.building.whereAt)
				if t == nil {
					if press(wh.Tooltip(full)) {
						a.activate(a.building)
						a.chooseWhere()
					}
				} else {
					wh.Tooltip(cmp.Or(tidy(t.CWD), t.Where))
				}
				model := control(c, func() {
					name, on := "model", t
					if on == nil {
						on = a.starts()
					}
					if on != nil {
						_, id, _ := strings.Cut(on.Model, "/")
						name = cmp.Or(id, on.Model, name)
						if on.Model == "" && len(on.Models) == 0 && on.Loaded {
							name = "connect a provider"
						}
					}
					ui.Text(c, name).FontSize(small).FontWeight(medium).TextColor(pal.mute).SingleLine().Shrink(1)
					// Reasoning as a small meter: one bar per level the model takes, lit up to the current one.
					if on != nil && len(on.Levels) > 1 { // one level is no choice: no meter
						now := -1
						for i, l := range on.Levels {
							if l == on.Thinking {
								now = i
							}
						}
						ui.Row(c).Gap(2).AlignItems(ui.End).MarginStart(2).Children(func() {
							for i, l := range on.Levels {
								ink := pal.rule
								if i <= now && l != "off" {
									ink = pal.mute
								}
								ui.Box(c.Key(l)).Size(2, float32(4+2*i)).Radius(1).Background(ink)
							}
						})
					}
					caretDown(c)
				}).Shrink(1).Bind(&a.building.modelAt)
				if press(model.Tooltip("Model and reasoning  " + map[bool]string{true: "⌃M", false: "Ctrl+M"}[runtime.GOOS == "darwin"])) {
					a.activate(a.building)
					a.chooseModel(t, false)
				}
				ui.Spacer(c)
				empty := strings.TrimSpace(draft) == ""
				switch {
				case busy && empty:
					if press(round(c, "■", rupture, pal.bg).Tooltip("Stop  " + keys("."))) {
						o.send("abort", "tab", id)
					}
				case busy:
					if press(control(c, func() { ui.Text(c, "queue").FontSize(small).FontWeight(medium).TextColor(pal.mute) }).Tooltip("Send after this turn")) {
						send(take())
					}
					if press(round(c, "↑", pal.fg, pal.bg).Tooltip("Steer this turn  ⏎")) {
						if text := take(); text != "" {
							o.send("steer", "tab", id, "text", text)
						}
					}
				default:
					ink := pal.fg
					if empty {
						ink = pal.rule
					}
					// Nothing to send, nothing to press.
					if e := round(c, "↑", ink, pal.bg).Tooltip("Send  ⏎ · new line  ⇧⏎"); !empty && press(e) {
						send(take())
					}
				}
			})
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

// The box reaches past the reading column by overhang a side, and sets its text at inset, so the
// text lines up with the column's and the controls' marks with the text.
const overhang, inset = 8, overhang + margin

// control is a quiet button in the prompt box: its content on no ground until the pointer is over it.
func control(c *ui.Context, body func()) ui.Element {
	return hover(ui.Row(c).Height(30).Padding(0, 9).Gap(7).Radius(8), p(c).bg).Children(body)
}

// round is the prompt box's one action: a filled circle with its mark.
func round(c *ui.Context, mark string, fill, ink ui.Color) ui.Element {
	return ui.Row(c).Size(30, 30).Radius(15).Background(fill).Justify(ui.Center).Children(func() {
		ui.Text(c, mark).FontSize(15).FontWeight(strong).TextColor(ink)
	})
}

// caretDown says a control opens a list.
func caretDown(c *ui.Context) {
	ui.Text(c, "⌄").FontSize(12).TextColor(p(c).meta).Margin(-4, 0, 0, 0)
}
