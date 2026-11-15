package main

import (
	"cmp"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// providerPage is what the providers of one machine ("" is none yet) last listed, kept across
// frames and visits, so the screens open full and refresh in place.
type providerPage struct {
	loaded    bool
	providers []Provider
	accounts  []Account
	query     string
	switching string // the account being made the one in use, provider/id
	note      string
}

// vendorPage is one provider's sign-in screen: what it said, the page it opened, the answer typed.
type vendorPage struct {
	note           string
	opened         string // the URL (and code) already opened in the browser
	reloaded       bool   // the listing was read again once the sign-in was done
	paste, visible bool   // a remote code is to be pasted; a secret shows
	question       string // what answer holds the reply to
	answer         string
}

var (
	providerPages = map[string]*providerPage{}
	vendorPages   = map[string]*vendorPage{}
	pluginPages   = map[string]*pluginPage{}
)

// reread has the screens of settings read their machine again the next time they show.
func reread() {
	for _, pg := range providerPages {
		pg.loaded = false
	}
	for _, pg := range pluginPages {
		pg.loaded = false
	}
}

func (a *app) providerPage(peer string) *providerPage {
	pg := providerPages[peer]
	if pg == nil {
		pg = &providerPage{}
		providerPages[peer] = pg
	}
	return pg
}

func (a *app) reloadProviders(peer string) {
	pg := a.providerPage(peer)
	then(a.o, func(r []Provider, err error) {
		if err != nil {
			pg.note = err.Error()
			return
		}
		pg.providers = r
	}, "providers", "machine", peer)
}

func (a *app) reloadAccounts(peer string) {
	pg := a.providerPage(peer)
	then(a.o, func(r []Account, err error) {
		if err == nil {
			pg.accounts = r
		}
	}, "accounts", "machine", peer)
}

func (a *app) machineName(peer string) string {
	if m := a.o.machine(peer); m != nil {
		return m.Name
	}
	return "that device"
}

func (a *app) providersScreen(c *ui.Context, peer string) {
	pal, pg := p(c), a.providerPage(peer)
	if !pg.loaded {
		pg.loaded = true
		a.reloadProviders(peer)
		a.reloadAccounts(peer)
		a.readDefault(peer, nil)
	}
	q := strings.ToLower(strings.TrimSpace(pg.query))
	has := func(s ...string) bool {
		for _, v := range s {
			if strings.Contains(strings.ToLower(v), q) {
				return true
			}
		}
		return q == ""
	}
	ready := 0
	for _, pr := range pg.providers {
		if pr.Ready {
			ready++
		}
	}
	sub := a.machineName(peer) + " · reading Orb's providers…"
	if pg.note != "" && len(pg.providers) == 0 {
		sub = a.machineName(peer)
	} else if len(pg.providers) > 0 {
		sub = fmt.Sprintf("%s · %d ready · %d to choose from", a.machineName(peer), ready, len(pg.providers))
	}
	page(c, func() {
		header(c, "Providers", sub, a.up(), nil)
		field(c, &pg.query, "Anthropic, OpenAI, Groq…", false).Margin(0, margin, 4)
	}, func() {
		var accounts []Account
		for _, ac := range pg.accounts {
			if has(ac.Name, ac.ProviderName) {
				accounts = append(accounts, ac)
			}
		}
		if pg.note != "" {
			ui.Text(c, pg.note).FontSize(small).TextColor(rupture).PaddingY(6)
		}
		if d := a.defaults[peer]; d != nil && len(d.Models) > 0 {
			_, model, _ := strings.Cut(d.Model, "/")
			if press(settingsLine(c.Key("default"), "default model", strings.Trim(model+" · "+d.Thinking, " ·"), "change ›", true)) {
				a.chooseModel(d, false)
			}
		}
		if len(accounts) > 0 {
			label(c, "accounts · quota left").Padding(22, 0, 4).TextColor(pal.meta)
			for _, ac := range accounts {
				a.accountRow(c.Key("a:"+ac.Provider+"/"+ac.ID), peer, ac)
			}
		}
		// The ready ones a line each; the rest, many and alike, in a grid of names to sign in to.
		group := func(name string, keep func(Provider) bool) {
			var in []Provider
			for _, pr := range pg.providers {
				if keep(pr) && has(pr.Name, pr.ID) {
					in = append(in, pr)
				}
			}
			if len(in) == 0 {
				return
			}
			label(c, name).Padding(22, 0, 4).TextColor(pal.meta)
			if in[0].Ready {
				for _, pr := range in {
					a.providerRow(c.Key(pr.ID), peer, pr)
				}
				return
			}
			ui.Grid(c).FillWidth().Columns(map[bool]int{true: 2, false: 3}[a.narrow]).Gap(6).Children(func() {
				for _, pr := range in {
					cell := ui.Row(c.Key(pr.ID)).Padding(8, 10).Gap(8).Radius(soft).Border(1, pal.rule).AlignItems(ui.Center).Children(func() {
						ui.Text(c, pr.Name).Grow(1).Basis(0).MinWidth(0).FontSize(small).FontWeight(medium).SingleLine()
						ui.Text(c, "›").TextColor(pal.meta)
					})
					hover(cell, pal.hover)
					if press(cell) {
						a.push(screen{kind: "vendor", peer: peer, arg: pr.ID})
					}
				}
			})
		}
		account := func(pr Provider) bool {
			for _, m := range pr.Methods {
				if m.Account {
					return true
				}
			}
			return false
		}
		group("ready", func(pr Provider) bool { return pr.Ready })
		group("sign in with your subscription", func(pr Provider) bool { return !pr.Ready && account(pr) })
		group("add an api key", func(pr Provider) bool { return !pr.Ready && !account(pr) })
		if q != "" && !slices.ContainsFunc(pg.providers, func(pr Provider) bool { return has(pr.Name, pr.ID) }) && len(accounts) == 0 {
			empty(c, "no provider matches “"+pg.query+"”")
		}
	})
}

// settingsLine is a row of the lists: a dot lit when it is on, its name and what it is, its state at right.
func settingsLine(c *ui.Context, name, sub, state string, on bool) ui.Element {
	pal := p(c)
	e := ui.Column(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().PaddingY(10).Gap(12).AlignItems(ui.Center).Children(func() {
			ink := pal.rule
			if on {
				ink = pal.fg
			}
			dot(c, ink, 8, false, false)
			ui.Column(c).Grow(1).Basis(0).Children(func() {
				w := 400
				if on {
					w = strong
				}
				ui.Text(c, name).FontSize(15).FontWeight(w).SingleLine()
				ui.Text(c, sub).FontSize(12).TextColor(pal.meta).SingleLine()
			})
			ink = pal.mute
			if on {
				ink = pal.fg
			}
			ui.Text(c, state).FontWeight(medium).TextColor(ink)
		})
		rule(c, pal.rule)
	})
	return e
}

func (a *app) providerRow(c *ui.Context, peer string, pr Provider) {
	var ways []string
	account := false
	for _, m := range pr.Methods {
		if m.Account {
			account = true
			ways = append(ways, "account")
		} else {
			ways = append(ways, "api key")
		}
	}
	sub, state := strings.Join(ways, " · "), "add key ›"
	switch {
	case pr.Ready:
		sub, state = pr.Holds, fmt.Sprintf("%d model%s", pr.Models, map[bool]string{true: "", false: "s"}[pr.Models == 1])
	case account:
		state = "sign in ›"
	}
	if press(settingsLine(c, pr.Name, sub, state, pr.Ready)) {
		a.push(screen{kind: "vendor", peer: peer, arg: pr.ID})
	}
}

// accountRow is an account: a click makes it the one its provider uses; each plan-limit window below as a bar.
func (a *app) accountRow(c *ui.Context, peer string, ac Account) {
	pal, pg := p(c), a.providerPage(peer)
	key := ac.Provider + "/" + ac.ID
	busy := pg.switching == key
	e := ui.Column(c).FillWidth().Children(func() {
		ui.Column(c).FillWidth().PaddingY(10).Gap(8).Children(func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ink, w := pal.rule, 400
				if ac.Active {
					ink, w = pal.fg, strong
				}
				dot(c, ink, 8, false, false)
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					ui.Text(c, ac.Name).FontSize(15).FontWeight(w).SingleLine()
					provider, _, _ := strings.Cut(ac.ProviderName, " (")
					plan := ac.Plan
					if plan != "" {
						plan = strings.ToUpper(plan[:1]) + plan[1:]
					}
					ui.Text(c, strings.Trim(provider+" · "+plan, " ·")).FontSize(small).TextColor(pal.meta).SingleLine()
				})
				state, ink := "use ›", pal.mute
				switch {
				case busy:
					state = "switching…"
				case ac.Active:
					state, ink = "in use", pal.fg
				}
				ui.Text(c, state).FontWeight(medium).TextColor(ink)
			})
			for _, w := range ac.Windows {
				quota(c, w)
			}
		})
		rule(c, pal.rule)
	})
	if ac.Active || busy {
		return
	}
	if press(e) {
		pg.switching, pg.note = key, ""
		then(a.o, func(_ struct{}, err error) {
			if err != nil {
				pg.note = err.Error()
			}
			pg.switching = ""
			a.reloadAccounts(peer)
		}, "account", "machine", peer, "provider", ac.Provider, "account", ac.ID)
	}
}

// quota is one plan-limit window: the share left as a bar, in the rupture red once it runs low, and its reset.
func quota(c *ui.Context, w Window) {
	pal := p(c)
	ink := pal.fg
	if w.Left < 15 {
		ink = rupture
	}
	ui.Row(c).FillWidth().PaddingStart(20).Gap(10).Children(func() {
		ui.Text(c, w.Name).Width(84).FontSize(small).TextColor(pal.meta).SingleLine()
		ui.Box(c).Grow(1).Basis(0).Height(4).Radius(2).Background(pal.rule).Clip().Children(func() {
			ui.Box(c).FillHeight().WidthPercent(float32(math.Max(0, math.Min(100, w.Left)))).Background(ink)
		})
		ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(w.Left)))).Width(44).FontSize(small).FontWeight(medium).TextColor(ink)
		ui.Text(c, resetsAt(w.Resets)).Width(84).FontSize(12).TextColor(pal.meta).SingleLine()
	})
}

// resetsAt is when a window resets, as briefly as it stays unambiguous.
func resetsAt(ms int64) string {
	if ms <= 0 {
		return ""
	}
	at := time.UnixMilli(ms)
	switch h := time.Until(at).Hours(); {
	case h < 20:
		return at.Format("15:04")
	case h < 6*24:
		return at.Format("Mon 15:04")
	}
	return at.Format("2 Jan")
}

// vendorScreen is one provider: its sign-in methods exactly as /login offers them, the flow
// running, and what it unlocked.
func (a *app) vendorScreen(c *ui.Context, id, peer string) {
	pal, pg := p(c), a.providerPage(peer)
	vp := vendorPages[peer+"/"+id]
	if vp == nil {
		vp = &vendorPage{}
		vendorPages[peer+"/"+id] = vp
		pg.loaded = true
		a.reloadProviders(peer)
	}
	pr := Provider{ID: id, Name: id}
	for _, x := range pg.providers {
		if x.ID == id {
			pr = x
		}
	}
	// The sign-in the view runs for this provider on this machine, if any.
	var flow *Login
	if l := a.o.state.Login; l != nil && l.Machine == peer && l.Provider == id {
		flow = l
	}
	running := flow != nil && flow.State != "done" && flow.State != "failed"
	// Signed in: the listing says what it unlocked.
	if flow != nil && flow.State == "done" {
		if !vp.reloaded {
			vp.reloaded, vp.note = true, ""
			a.reloadProviders(peer)
			a.reloadAccounts(peer)
		}
	} else {
		vp.reloaded = false
	}
	back := func() {
		if running {
			a.o.send("login.cancel")
		}
		a.back()
	}
	var abouts []string
	for _, m := range pr.Methods {
		abouts = append(abouts, m.About)
	}
	page(c, func() { header(c, pr.Name, strings.Join(abouts, " · "), back, nil) }, func() {
		ui.Column(c).FillWidth().Gap(18).Children(func() {
			if pr.Ready {
				ui.Text(c, fmt.Sprintf("Ready through %s · %d model%s", pr.Holds, pr.Models, map[bool]string{true: "", false: "s"}[pr.Models == 1])).FontSize(15).FontWeight(medium)
			} else {
				ui.Text(c, "Not signed in").FontSize(15).FontWeight(medium).TextColor(pal.mute)
			}
			if flow == nil || flow.State == "failed" {
				ui.Column(c).FillWidth().Gap(12).Children(func() {
					if flow != nil {
						ui.Text(c, cmp.Or(flow.Detail, "sign-in failed")).TextColor(rupture)
					}
					for _, m := range pr.Methods {
						if methodCard(c.Key(m.Auth+m.Label), m, pr.Ready) {
							vp.note, vp.opened = "", ""
							a.o.send("login", "machine", peer, "provider", id, "auth", m.Auth)
						}
					}
					if pr.Ready {
						a.signOut(c, peer, pr, vp)
					}
				})
			} else {
				a.loginFlow(c, flow, vp, peer != a.o.state.Self)
			}
			if vp.note != "" {
				ui.Text(c, vp.note).TextColor(pal.mute)
			}
		})
	})
}

// methodCard is one way to sign in; it reports a click.
func methodCard(c *ui.Context, m Method, ready bool) bool {
	pal := p(c)
	kind, action := "api key", "add key"
	switch {
	case m.Account && ready:
		kind, action = "account", "sign in again"
	case m.Account:
		kind, action = "account", "sign in"
	case ready:
		action = "replace key"
	}
	clicked := false
	e := ui.Column(c).FillWidth().Padding(18).Radius(pane).Border(1, pal.rule).Children(func() {
		label(c, kind).TextColor(pal.meta)
		ui.Text(c, m.Label).FontSize(15).FontWeight(strong).Margin(6, 0, 0)
		ui.Text(c, m.About).TextColor(pal.meta).Margin(2, 0, 0)
		ui.Row(c).Margin(14, 0, 0).Children(func() { clicked = btn(c, action, !ready, pal.fg, pal.bg) })
	})
	hover(e, pal.hover)
	return press(e) || clicked
}

// signOut removes the credential from Orb's store, as `orb logout` does; one configured elsewhere is changed there.
func (a *app) signOut(c *ui.Context, peer string, pr Provider, vp *vendorPage) {
	pal := p(c)
	if pr.Status != "oauth" && pr.Source != "stored" {
		ui.Text(c, "Configured outside the app ("+pr.Source+"); change it there.").FontSize(small).TextColor(pal.meta)
		return
	}
	text, done := "remove key", "key removed"
	if pr.Status == "oauth" {
		text, done = "sign out", "signed out of "+pr.Name
	}
	ui.Row(c).Children(func() {
		if btn(c, text, false, pal.fg, pal.bg) {
			then(a.o, func(_ struct{}, err error) {
				vp.note = done
				if err != nil {
					vp.note = err.Error()
				}
				a.reloadProviders(peer)
				a.reloadAccounts(peer)
			}, "logout", "machine", peer, "provider", pr.ID)
		}
	})
}

// loginFlow is a sign-in in progress: a browser page, a device code, or a question, as the TUI shows them.
func (a *app) loginFlow(c *ui.Context, f *Login, vp *vendorPage, remote bool) {
	pal := p(c)
	cancel := func() { a.o.send("login.cancel") }
	answer := func(text string) { a.o.send("login.answer", "text", text) }
	open := func() {
		if f.URL != "" {
			_ = mygo.Shell.OpenExternal(f.URL)
		}
	}
	ui.Column(c).FillWidth().Gap(12).Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 12}}).Children(func() {
		switch f.State {
		case "starting":
			ui.Row(c).Gap(10).Children(func() {
				dot(c, pal.mute, 8, true, false)
				ui.Text(c, cmp.Or(f.Detail, "starting…")).TextColor(pal.mute)
			})
		case "browser":
			if vp.opened != f.URL {
				vp.opened = f.URL
				vp.paste = remote
				open()
			}
			ui.Text(c, "Continue in the browser").FontSize(title).FontWeight(strong)
			// A device signing in over Bridge listens on its own localhost: the code or final URL comes back by paste.
			if remote {
				ui.Text(c, "Finish on the page that opened, then paste the code it shows or the final redirect URL here.").TextColor(pal.mute)
			} else {
				ui.Text(c, "Finish on the page that opened. It redirects to Orb on this computer, which brings you back here.").TextColor(pal.mute)
			}
			ui.Row(c).Gap(8).Children(func() {
				if btn(c, "open again", true, pal.fg, pal.bg) {
					open()
				}
				if btn(c, "cancel", false, pal.fg, pal.bg) {
					cancel()
				}
			})
			if pr := f.Prompt; pr != nil && pr.Kind == "manual_code" {
				if !vp.paste {
					if press(ui.Text(c, "signing in from another device? paste the code or redirect URL").FontSize(small).TextColor(pal.meta)) {
						vp.paste = true
					}
				} else {
					a.loginAnswer(c, vp, pr.Message, pr.Placeholder, false, remote, answer)
				}
			}
		case "code":
			// The code rides the clipboard to the page, which opens by itself.
			if vp.opened != f.Code+f.URL {
				vp.opened = f.Code + f.URL
				mygo.Clipboard.WriteText(f.Code)
				open()
			}
			label(c, "enter this code").TextColor(pal.meta)
			if press(ui.Text(c, f.Code).FontSize(34).FontWeight(strong).Selectable().Tooltip("Copy")) {
				mygo.Clipboard.WriteText(f.Code)
				a.say("code copied")
			}
			ui.Text(c, "It is on your clipboard. The page is "+strings.TrimPrefix(f.URL, "https://")).FontSize(small).TextColor(pal.mute)
			ui.Row(c).Gap(8).Children(func() {
				if btn(c, "open page", true, pal.fg, pal.bg) {
					open()
				}
				if btn(c, "cancel", false, pal.fg, pal.bg) {
					cancel()
				}
			})
			if f.Detail != "" {
				ui.Text(c, f.Detail).FontSize(small).TextColor(pal.meta)
			}
		case "asking":
			if pr := f.Prompt; pr != nil {
				if len(pr.Options) > 0 {
					ui.Text(c, pr.Message).TextColor(pal.mute)
					for i, o := range pr.Options {
						ui.Row(c.Key(o.ID)).Children(func() {
							if btn(c, o.Label, i == 0, pal.fg, pal.bg) {
								answer(o.ID)
							}
						})
					}
				} else {
					a.loginAnswer(c, vp, pr.Message, pr.Placeholder, pr.Kind == "secret", remote, answer)
				}
			}
			ui.Row(c).Children(func() {
				if btn(c, "cancel", false, pal.fg, pal.bg) {
					cancel()
				}
			})
		case "done":
			ui.Text(c, "Signed in").FontSize(title).FontWeight(strong)
			if remote {
				ui.Text(c, "That device holds the new credential; its sessions can use the models now.").TextColor(pal.mute)
			} else {
				ui.Text(c, "The core restarted with the new credential; its models are in the model deck.").TextColor(pal.mute)
			}
			ui.Row(c).Children(func() {
				if btn(c, "done", true, pal.fg, pal.bg) {
					cancel()
				}
			})
		}
	})
}

// loginAnswer is a question of the sign-in and the field that answers it; a secret hides until shown.
func (a *app) loginAnswer(c *ui.Context, vp *vendorPage, question, hint string, secret, remote bool, send func(string)) {
	pal := p(c)
	if vp.question != question {
		vp.question, vp.answer, vp.visible = question, "", false
	}
	if hint == "" && secret {
		hint = "paste it here"
	}
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, question).TextColor(pal.mute)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			in := field(c, &vp.answer, hint, secret && !vp.visible).Grow(1).AutoFocus()
			submit := in.Submitted()
			if secret {
				word := "show"
				if vp.visible {
					word = "hide"
				}
				if press(ui.Text(c, word).FontSize(labelSize).TextColor(pal.meta).PaddingX(4)) {
					vp.visible = !vp.visible
				}
			}
			if (btn(c, "save", true, pal.fg, pal.bg) || submit) && strings.TrimSpace(vp.answer) != "" {
				send(strings.TrimSpace(vp.answer))
			}
		})
		if secret {
			where := "this computer"
			if remote {
				where = "that device"
			}
			ui.Text(c, "Orb stores it in its own credential store on "+where+", as /login does.").FontSize(small).TextColor(pal.meta)
		}
	})
}

// pluginPage is a machine's plugins as it last listed them.
type pluginPage struct {
	loaded bool
	list   []Plugin
	note   string
}

func (a *app) reloadPlugins(peer string, pg *pluginPage) {
	then(a.o, func(r []Plugin, err error) {
		pg.note = ""
		if err != nil {
			pg.note = err.Error()
			return
		}
		pg.list = r
	}, "plugins", "machine", peer)
}

// pluginsScreen is Orb's bundled plugins on a machine, and their choices. Each one only ever
// renders into a slot or raises an interrupt.
func (a *app) pluginsScreen(c *ui.Context, peer string) {
	pal := p(c)
	pg := pluginPages[peer]
	if pg == nil {
		pg = &pluginPage{}
		pluginPages[peer] = pg
	}
	if !pg.loaded {
		pg.loaded = true
		a.reloadPlugins(peer, pg)
	}
	// A plugin on or off, or one of its choices; the machine's Orbs opened here reopen with it.
	change := func(args ...any) {
		then(a.o, func(_ struct{}, err error) {
			a.reloadPlugins(peer, pg)
			if err != nil {
				pg.note = err.Error()
			}
		}, "plugin", append([]any{"machine", peer}, args...)...)
	}
	on := 0
	for _, pl := range pg.list {
		if pl.On {
			on++
		}
	}
	sub := a.machineName(peer) + " · reading"
	if len(pg.list) > 0 {
		sub = fmt.Sprintf("%s · %d of %d on", a.machineName(peer), on, len(pg.list))
	}
	page(c, func() { header(c, "Plugins", sub, a.up(), nil) }, func() {
		for _, pl := range pg.list {
			ui.Column(c.Key(pl.Name)).FillWidth().Children(func() {
				row := ui.Row(c).FillWidth().Padding(11, 4).Gap(14).AlignItems(ui.Center).Children(func() {
					box := ui.Box(c).Size(18, 18).Radius(4).Border(1.5, pal.fg)
					if pl.On { // a check, which an x would contradict
						box.Background(pal.fg).Draw(func(p *ui.Painter, r ui.Rect) {
							p.StrokePolyline([][2]float32{{r.X + 4.5, r.Y + 9.5}, {r.X + 7.5, r.Y + 12.5}, {r.X + 13.5, r.Y + 5.5}}, 1.8, pal.bg)
						})
					}
					ui.Column(c).Grow(1).Basis(0).Children(func() {
						ink, w := pal.mute, 400
						if pl.On {
							ink, w = pal.fg, strong
						}
						ui.Text(c, pl.Name).FontSize(15).FontWeight(w).TextColor(ink)
						ui.Text(c, pl.About).FontSize(small).TextColor(pal.meta)
					})
				})
				hover(row, pal.hover)
				if press(row) {
					change("name", pl.Name, "on", !pl.On)
				}
				if pl.On {
					for _, ch := range pl.Choices {
						ui.Row(c.Key(ch.Key)).Padding(0, 4, 14, 40).Gap(8).Children(func() {
							ui.Text(c, ch.Key).FontSize(small).TextColor(pal.meta)
							for _, v := range ch.Values {
								kind := outline
								if v == ch.Value {
									kind = inverted
								}
								if press(chip(c.Key(v), v, kind)) && v != ch.Value {
									change("name", pl.Name, "key", ch.Key, "value", v)
								}
							}
						})
					}
				}
				rule(c, pal.rule)
			})
		}
		note, ink := "An Orb reads its plugins when it starts: those open here reopen with a change at their next message.", pal.meta
		if pg.note != "" {
			note, ink = pg.note, rupture
		}
		ui.Text(c, note).FontSize(labelSize+1).TextColor(ink).Padding(20, 4, 0)
	})
}

// settingsKinds are the tabs of Settings (⌘,), in order.
var settingsKinds = []struct{ kind, name string }{{"general", "General"}, {"providers", "Providers"}, {"plugins", "Plugins"}, {"bridge", "Bridge"}}

// settings opens Settings on its first tab.
func (a *app) settings() {
	reread()
	a.stack = []screen{{kind: "general", peer: a.o.state.Self}}
}

// settingsMenu is Settings' left side: its pages, the one shown lit, on the machine they show;
// at its foot the way back to the conversations (Escape).
func (a *app) settingsMenu(c *ui.Context) {
	pal, root := p(c), a.stack[0]
	ui.Column(c).Width(208).Padding(4, 12, 16).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(pal.rule).Children(func() {
		ui.Text(c, "Settings").FontSize(title).FontWeight(strong).Padding(0, 10, 2)
		ui.Text(c, a.machineName(cmp.Or(root.peer, a.o.state.Self))).FontSize(small).TextColor(pal.meta).Padding(0, 10, 14).SingleLine()
		for _, s := range settingsKinds {
			on := root.kind == s.kind
			e := ui.Row(c.Key(s.kind)).FillWidth().Padding(7, 10).Radius(7).Children(func() {
				ui.Text(c, s.name).FontWeight(map[bool]int{true: strong, false: medium}[on]).TextColor(map[bool]ui.Color{true: pal.fg, false: pal.mute}[on])
			})
			if on {
				e.Background(pal.raised)
			} else {
				hover(e, pal.hover)
			}
			if press(e) && (!on || len(a.stack) > 1) {
				reread()
				a.stack = []screen{{kind: s.kind, peer: cmp.Or(root.peer, a.o.state.Self)}}
			}
		}
		ui.Box(c).Grow(1)
		done := ui.Row(c).FillWidth().Padding(7, 10).Radius(7).Children(func() {
			ui.Text(c, "‹  conversations").Grow(1).TextColor(pal.mute)
			label(c, "esc").TextColor(pal.meta)
		})
		hover(done, pal.hover)
		if press(done) {
			for len(a.stack) > 0 {
				a.back()
			}
		}
	})
}

// up is a page's way back: the page it was opened from, none for a page of Settings' menu.
func (a *app) up() func() {
	if len(a.stack) > 1 {
		return a.back
	}
	return nil
}

// generalScreen is Settings' first tab: what new conversations start on, how the machines stand,
// and the keys.
func (a *app) generalScreen(c *ui.Context) {
	pal := p(c)
	page(c, func() { header(c, "General", "new conversations, machines, keys", nil, nil) }, func() {
		label(c, "new conversations").Padding(14, 0, 2).TextColor(pal.meta)
		d := a.starts()
		if d == nil {
			d = &Tab{} // read as it shows
		}
		{
			_, model, _ := strings.Cut(d.Model, "/")
			row := ui.Row(c.Key("default")).FillWidth().PaddingY(10).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					ui.Text(c, "default model").FontSize(15).FontWeight(strong).SingleLine()
					ui.Text(c, cmp.Or(strings.Trim(model+" · "+d.Thinking, " ·"), map[bool]string{true: "none yet", false: "reading…"}[d.Loaded])).FontSize(small).TextColor(pal.meta).SingleLine()
				})
				ui.Text(c, "change ›").FontWeight(medium)
			})
			if press(row) {
				a.chooseModel(nil, false)
			}
		}
		label(c, "machines").Padding(18, 0, 4).TextColor(pal.meta)
		a.magi(c)
		label(c, "keys").Padding(18, 0, 4).TextColor(pal.meta)
		for _, k := range [][2]string{
			{keys("K") + " · " + keys("P"), "go to a conversation, a thread"}, {keys("T") + " · " + keys("N"), "new tab"}, {keys("W"), "close the tab (it runs on)"},
			{keys("⇧T"), "reopen the tab closed last"}, {keys("⇧W"), "close the tile"}, {keys("D") + " · " + keys("⇧D"), "split right · down"},
			{keys("[") + " · " + keys("]"), "previous · next tile"}, {keys("⇧[") + " · " + keys("⇧]"), "previous · next tab"},
			{map[bool]string{true: "⌃Tab · ⌃⇧Tab", false: "Ctrl+Tab · Ctrl+Shift+Tab"}[runtime.GOOS == "darwin"], "next · previous tab"},
			{keys("1") + "…" + keys("8") + " · " + keys("9"), "a tile's tabs · its last"}, {keys("⇧↩"), "zoom the tile"},
			{keys("=") + " · " + keys("-") + " · " + keys("0"), "zoom in · out · actual size"}, {map[bool]string{true: "⌃M", false: "Ctrl+M"}[runtime.GOOS == "darwin"], "model and reasoning"},
			{keys("J"), "the conversation's terminal"}, {keys("."), "stop the turn"}, {keys(","), "settings"},
		} {
			ui.Row(c.Key(k[1])).FillWidth().PaddingY(2).Gap(12).Children(func() {
				ui.Text(c, k[0]).Width(map[bool]float32{true: 120, false: 170}[runtime.GOOS == "darwin"]).FontSize(small).FontWeight(medium).TextColor(pal.fg)
				ui.Text(c, k[1]).FontSize(small).TextColor(pal.mute)
			})
		}
	})
}
