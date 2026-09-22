package questions

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/tui"
)

// Panel is shared by the local dialog and the remote conversation view.
type Panel struct {
	mu                 sync.Mutex
	request            Request
	answers            []Answer
	index, cursor      int
	writing, completed bool
	selected           map[string]bool
	input              *tui.Input
	frame              *tui.Frame
	theme              extensions.Theme
	height             func() int
	invalidate         func()
	done               func(Result)
	ready              *Result
	message            string
	hits               map[int]int
	tabs               []struct{ row, column, width, index int }
	pressed, hover     int
}

func NewPanel(request Request, theme extensions.Theme, height func() int, invalidate func(), done func(Result)) *Panel {
	p := &Panel{request: request, theme: theme, height: height, invalidate: invalidate, done: done, answers: make([]Answer, len(request.Questions)), input: tui.NewInput()}
	p.input.Prompt = "Your answer › "
	p.frame = tui.NewPanel("", "", nil, nil, func() string { return theme.BGANSI("toolPendingBg") }, questionBody{p})
	p.load()
	return p
}
func (p *Panel) single() bool {
	return len(p.request.Questions) == 1 && !p.request.Questions[0].MultiSelect
}
func (p *Panel) review() bool { return p.index == len(p.request.Questions) }
func (p *Panel) load() {
	p.cursor, p.message, p.writing, p.pressed, p.hover = 0, "", false, -1, -1
	if p.review() {
		p.input.SetFocused(false)
		return
	}
	q := p.request.Questions[p.index]
	p.selected = map[string]bool{}
	for _, label := range p.answers[p.index].Selected {
		p.selected[label] = true
	}
	p.input.SetValue(p.answers[p.index].Custom)
	p.writing = len(q.Options) == 0
	p.input.SetFocused(p.writing)
}
func (p *Panel) save() {
	if p.review() {
		return
	}
	q := p.request.Questions[p.index]
	a := Answer{ID: q.ID, Selected: []string{}, Custom: strings.TrimSpace(p.input.GetValue())}
	for _, o := range q.Options {
		if p.selected[o.Label] {
			a.Selected = append(a.Selected, o.Label)
		}
	}
	if !q.MultiSelect && a.Custom != "" {
		a.Selected = []string{}
	}
	p.answers[p.index] = a
}
func (p *Panel) move(delta int) {
	p.save()
	tabs := len(p.request.Questions) + 1
	p.index = (p.index + delta + tabs) % tabs
	p.load()
}
func (p *Panel) pick() {
	q := p.request.Questions[p.index]
	if p.cursor == len(q.Options) {
		p.writing = true
		p.input.SetFocused(true)
		return
	}
	label := q.Options[p.cursor].Label
	if q.MultiSelect {
		p.selected[label] = !p.selected[label]
		return
	}
	p.selected = map[string]bool{label: true}
	p.input.SetValue("")
	p.submit()
}
func (p *Panel) submit() {
	p.save()
	if !p.review() {
		a := p.answers[p.index]
		if len(a.Custom) > 16384 {
			p.message = "Keep your answer under 16 KiB."
			return
		}
		if len(a.Selected) == 0 && a.Custom == "" {
			p.message = "Choose an option or write an answer."
			return
		}
		if !p.single() {
			p.move(1)
			return
		}
	}
	result := Result{Answers: p.answers}
	raw, _ := json.Marshal(result)
	if err := p.request.ValidateReply(string(raw)); err != nil {
		p.message = err.Error()
		return
	}
	p.completed, p.ready = true, &result
	p.message = "Answer sent"
}
func (p *Panel) notify() {
	p.mu.Lock()
	ready := p.ready
	p.ready = nil
	p.mu.Unlock()
	p.invalidate()
	if ready != nil {
		p.done(*ready)
	}
}
func (p *Panel) Render(width int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := p.frame.Render(width)
	for i, line := range lines {
		// Add the accent rail without changing Frame's mouse coordinates.
		lines[i] = p.theme.FG("accent", "│") + tui.SliceByColumn(line, 1, width-1, true)
	}
	return lines
}
func (p *Panel) SetFocused(f bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input.SetFocused(f && p.writing)
}
func (p *Panel) HandleInput(key tui.KeyEvent) {
	p.mu.Lock()
	p.hover = -1
	if !p.completed {
		switch {
		case tui.MatchesKey(key.Raw, "escape"):
			if p.writing && len(p.request.Questions[p.index].Options) > 0 {
				p.writing = false
				p.input.SetFocused(false)
			} else {
				p.completed, p.ready, p.message = true, &Result{Cancelled: true}, "Question dismissed"
			}
		case !p.single() && (tui.MatchesKey(key.Raw, "alt+left") || (!p.writing && (tui.MatchesKey(key.Raw, "left") || tui.MatchesKey(key.Raw, "shift+tab")))):
			p.move(-1)
		case !p.single() && !p.writing && (tui.MatchesKey(key.Raw, "right") || tui.MatchesKey(key.Raw, "tab")):
			p.move(1)
		case p.review():
			if tui.MatchesKey(key.Raw, "enter") {
				p.submit()
			}
		case p.writing:
			if tui.MatchesKey(key.Raw, "enter") {
				if p.request.Questions[p.index].MultiSelect && len(p.input.GetValue()) <= 16384 {
					p.save()
					p.writing = false
					p.input.SetFocused(false)
				} else {
					p.submit()
				}
			} else {
				p.input.HandleInput(key)
			}
		case tui.MatchesKey(key.Raw, "up"), key.Raw == "k":
			p.cursor = (p.cursor + len(p.request.Questions[p.index].Options)) % (len(p.request.Questions[p.index].Options) + 1)
		case tui.MatchesKey(key.Raw, "down"), key.Raw == "j":
			p.cursor = (p.cursor + 1) % (len(p.request.Questions[p.index].Options) + 1)
		case tui.MatchesKey(key.Raw, "enter"), key.Raw == " ":
			p.pick()
		default:
			if len(key.Raw) == 1 && key.Raw[0] >= '1' && int(key.Raw[0]-'1') <= len(p.request.Questions[p.index].Options) {
				p.cursor = int(key.Raw[0] - '1')
				p.pick()
			}
		}
	}
	p.mu.Unlock()
	p.notify()
}
func (p *Panel) WantsMouseMotion() bool { return true }
func (p *Panel) HandleMouse(event tui.MouseEvent) bool {
	p.mu.Lock()
	handled := false
	if !p.completed {
		handled = p.frame.HandleMouse(event)
	}
	p.mu.Unlock()
	if handled {
		p.notify()
	}
	return handled
}

type questionBody struct{ p *Panel }

func (body questionBody) Render(width int) []string {
	p := body.p
	p.hits = map[int]int{}
	p.tabs = p.tabs[:0]
	text := func(s string) []string { return tui.NewText(s, 0, 0, nil).Render(width) }
	if p.completed {
		return text(p.message)
	}
	var lines []string
	if !p.single() {
		tabLine, column := "", 0
		for i := 0; i <= len(p.request.Questions); i++ {
			label := "Confirm"
			if i < len(p.request.Questions) {
				label = p.request.Questions[i].Header
				if label == "" {
					label = strconv.Itoa(i + 1)
				}
				if len(p.answers[i].Selected) > 0 || p.answers[i].Custom != "" {
					label += " ✓"
				}
			}
			label = tui.TruncateToWidth(" "+label+" ", width, "…", false)
			size := tui.VisibleWidth(label)
			if column > 0 && column+size > width {
				lines = append(lines, tabLine)
				tabLine, column = "", 0
			}
			p.tabs = append(p.tabs, struct{ row, column, width, index int }{len(lines), column, size, i})
			if i == p.index || p.hover == 100+i {
				label = p.theme.BG("selectedBg", p.theme.FG("accent", label))
			} else {
				label = p.theme.FG("muted", label)
			}
			tabLine += label + " "
			column += size + 1
		}
		lines = append(lines, strings.TrimSuffix(tabLine, " "), "")
	}
	footer := "↑↓ select · enter submit · esc dismiss"
	if p.review() {
		lines = append(lines, p.theme.Bold("Review your answers"), "")
		for i, q := range p.request.Questions {
			a := p.answers[i]
			value := strings.Join(a.Selected, ", ")
			if a.Custom != "" {
				if value != "" {
					value += ", "
				}
				value += a.Custom
			}
			if value == "" {
				value = p.theme.FG("warning", "Not answered")
			}
			label := q.Header
			if label == "" {
				label = q.ID
			}
			preview := text(p.theme.FG("muted", label+": ") + value)
			if len(preview) > 3 {
				preview = append(preview[:2], p.theme.FG("muted", "…"))
			}
			lines = append(lines, preview...)
		}
		footer = "⇆ tab questions · enter submit · esc dismiss"
	} else {
		q := p.request.Questions[p.index]
		title := q.Question
		if q.MultiSelect {
			title += " (select all that apply)"
			footer = "↑↓ select · enter toggle · tab continue · esc dismiss"
		} else if !p.single() {
			footer = "↑↓ select · enter choose · tab next · esc dismiss"
		}
		heading := text(p.theme.Bold(title))
		lines = append(lines, heading[:min(len(heading), 4)]...)
		lines = append(lines, "")
		var rows []string
		var hits []int
		active := 0
		for i := 0; i <= len(q.Options); i++ {
			label, detail := "Type your own answer", p.input.GetValue()
			picked := detail != ""
			if i < len(q.Options) {
				o := q.Options[i]
				label, detail, picked = o.Label, strings.TrimSpace(o.Description+"\n"+o.Preview), p.selected[o.Label]
			}
			if q.MultiSelect {
				if picked {
					label = "[✓] " + label
				} else {
					label = "[ ] " + label
				}
			} else if picked {
				label += " ✓"
			}
			label = fmt.Sprintf("%d. %s", i+1, label)
			if i == p.cursor {
				active = len(rows)
			}
			if i == p.hover || (p.hover < 0 && i == p.cursor) {
				label = p.theme.BG("selectedBg", p.theme.FG("accent", label))
			}
			start := len(rows)
			rows = append(rows, text(label)...)
			if i == len(q.Options) && p.writing {
				rows = append(rows, p.input.Render(width)...)
				footer = "enter confirm · esc back"
			} else if detail != "" {
				details := tui.NewText(p.theme.FG("muted", detail), 0, 0, nil).Render(max(1, width-3))
				for _, line := range details[:min(3, len(details))] {
					rows = append(rows, "   "+line)
				}
			}
			rows = append(rows, "")
			for range len(rows) - start {
				hits = append(hits, i)
			}
		}
		budget := max(3, max(12, p.height()*2/3)-len(lines)-len(text(footer))-7)
		start := min(max(0, active-budget/2), max(0, len(rows)-budget))
		for i := start; i < min(len(rows), start+budget); i++ {
			p.hits[len(lines)] = hits[i]
			lines = append(lines, rows[i])
		}
	}
	if p.message != "" {
		lines = append(lines, text(p.theme.FG("warning", p.message))...)
	}
	lines = append(lines, "")
	if p.review() || (!p.single() && !p.writing) {
		label := "Continue →"
		if p.review() {
			label = "Submit answers"
		}
		p.hits[len(lines)] = 200
		label = p.theme.FG("accent", "[ "+label+" ]")
		if p.hover == 200 {
			label = p.theme.BG("selectedBg", label)
		}
		lines = append(lines, label)
	}
	return append(lines, text(p.theme.FG("muted", footer))...)
}
func (body questionBody) HandleMouse(event tui.MouseEvent) bool {
	p := body.p
	if event.Type == tui.MouseDrag {
		p.pressed = -1
		return true
	}
	if !p.review() && (event.Type == tui.MouseWheelUp || event.Type == tui.MouseWheelDown) {
		delta := 1
		if event.Type == tui.MouseWheelUp {
			delta = -1
		}
		p.cursor = max(0, min(len(p.request.Questions[p.index].Options), p.cursor+delta))
		return true
	}
	target, ok := p.hits[event.Row]
	for _, tab := range p.tabs {
		if tab.row == event.Row && event.Column >= tab.column && event.Column < tab.column+tab.width {
			target, ok = 100+tab.index, true
			break
		}
	}
	if event.Type == tui.MouseMove {
		if !ok {
			target = -1
		}
		changed := p.hover != target
		p.hover = target
		return changed
	}
	if !ok {
		p.pressed = -1
		return false
	}
	switch event.Type {
	case tui.MousePress:
		if event.Button != 0 {
			return false
		}
		p.pressed = target
		return true
	case tui.MouseRelease:
		pressed := p.pressed
		p.pressed = -1
		if pressed != target {
			return true
		}
		switch {
		case target == 200:
			p.submit()
		case target >= 100:
			p.move(target - 100 - p.index)
		default:
			p.cursor = target
			p.writing = false
			p.input.SetFocused(false)
			p.pick()
		}
		return true
	}
	return false
}
