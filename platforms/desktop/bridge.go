package main

import (
	"fmt"
	"github.com/OrdalieTech/orb/internal/qr"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// pairing is what the Bridge screens keep from frame to frame.
var pairing struct {
	code, from string // the code on the join screen, and the one it opened with
	read       string // the code the view last said what it is
	inviter    string // the device that made it
	expires    int64  // seconds
	editing    bool
	err        string
	paired     time.Time         // when the other device said yes
	inviting   bool              // an invitation is being made
	invited    bool              // one was asked for since the app started
	updating   map[string]string // what updating each device came to
}

// fingerprint is the start of a fingerprint, large enough to compare at a glance.
func fingerprint(id string, n int) string {
	id = id[strings.LastIndex(id, ":")+1:]
	return id[:min(n, len(id))]
}

// countdown is what is left of an invitation, as m:ss; it draws again each second.
func countdown(c *ui.Context, expires int64) (string, bool) {
	left := max(expires-time.Now().Unix(), 0)
	c.After(time.Second)
	return fmt.Sprintf("%d:%02d", left/60, left%60), left > 0
}

// newer reports whether version a is past b, by their major.minor.patch.
func newer(a, b string) bool {
	parts := func(v string) [3]int {
		var p [3]int
		v, _, _ = strings.Cut(v, "-")
		for i, s := range strings.SplitN(v, ".", 3) {
			p[i], _ = strconv.Atoi(s)
		}
		return p
	}
	x, y := parts(a), parts(b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func (a *app) bridgeScreen(c *ui.Context) {
	o, pal := a.o, p(c)
	sub := "starting"
	if o.state.Up {
		sub = "on · peer to peer"
	}
	page(c, func() { header(c, "Bridge", sub, a.back, nil) }, func() {
		// One code pairs two devices: the other shows it, this one takes it, the other says yes.
		ui.Column(c).FillWidth().Padding(16).Radius(pane).Border(1, pal.fg).Gap(8).Children(func() {
			ui.Text(c, "Pair a device").FontSize(title).FontWeight(strong)
			ui.Text(c, "On the other computer, run").TextColor(pal.mute)
			ui.Row(c).Padding(7, 12).Radius(soft).Border(1, pal.rule).Children(func() {
				ui.Text(c, "orb bridge pair").FontWeight(strong).Selectable()
			})
			ui.Text(c, "then paste the code it shows. It asks you to approve this computer there; or invite it from here.").FontSize(small).TextColor(pal.meta)
			ui.Row(c).Gap(8).PaddingY(6).Children(func() {
				if btn(c, "paste code", true, pal.fg, pal.bg) {
					a.push(screen{kind: "join", arg: strings.TrimSpace(mygo.Clipboard.ReadText())})
				}
				if btn(c, "invite a device", false, pal.fg, pal.bg) {
					a.push(screen{kind: "invite"})
				}
			})
		})
		var peers []Machine
		connected := 0
		for _, m := range o.home.Machines {
			if !m.Self {
				peers = append(peers, m)
				if m.Connected {
					connected++
				}
			}
		}
		state := "none yet"
		if len(peers) > 0 {
			state = fmt.Sprintf("%d / %d connected", connected, len(peers))
		}
		section(c, "devices", state, func() {
			for _, m := range peers {
				a.peerRow(c.Key(m.ID), m)
			}
		})
		ui.Box(c).Height(6)
		section(c, device, "", func() {
			ui.Text(c, o.state.Self).FontSize(small).TextColor(pal.mute).Selectable()
			ui.Text(c, "Each device is approved by fingerprint on its own screen. Pairs stay paired and reconnect by themselves; forget revokes one.").
				FontSize(labelSize + 1).TextColor(pal.meta).PaddingY(12)
		})
	})
}

func (a *app) peerRow(c *ui.Context, m Machine) {
	o, pal := a.o, p(c)
	busy := false
	for _, r := range m.Running {
		busy = busy || r.Busy
	}
	row := ui.Row(c).FillWidth().PaddingY(12).Gap(12)
	row.Children(func() {
		// Its own hue, as its threads have it; dim while it is away.
		ink := hue(c, o.hue(m.ID), pal.fg)
		if !m.Connected {
			ink = pal.rule
		}
		dot(c, ink, 8, busy, false)
		ui.Column(c).Grow(1).Basis(0).Children(func() {
			name := ui.Text(c, m.Name).FontSize(16).SingleLine()
			if m.Connected {
				name.FontWeight(strong)
			} else {
				name.TextColor(pal.meta)
			}
			parts := []string{"disconnected"}
			if m.Connected {
				parts[0] = strconv.Itoa(len(m.Running)) + " session"
				if len(m.Running) != 1 {
					parts[0] += "s"
				}
			}
			parts = append(parts, fingerprint(m.ID, 6))
			if m.Version != "" {
				parts = append(parts, "orb "+m.Version)
			}
			ui.Text(c, strings.Join(parts, " · ")).FontSize(labelSize + 1).TextColor(pal.meta).SingleLine()
			if u := pairing.updating[m.ID]; u != "" {
				ui.Text(c, u).FontSize(labelSize + 1).TextColor(pal.mute)
			}
		})
		// A device behind the latest release updates from here (its Orb swaps itself and restarts Bridge).
		next := o.state.Latest
		if next != "" && m.Version != "" && m.Connected && newer(next, m.Version) && pairing.updating[m.ID] == "" {
			if btn(c, "update", false, pal.fg, pal.bg) {
				if pairing.updating == nil {
					pairing.updating = map[string]string{}
				}
				pairing.updating[m.ID] = "updating to " + next + "…"
				then(o, func(r string, err error) {
					if err != nil {
						r = err.Error()
					}
					pairing.updating[m.ID] = r
				}, "update", "machine", m.ID)
			}
		}
		// Named here (it may not say its own name), or forgotten.
		if press(ui.Row(c).Padding(4, 8).Children(func() { ui.Text(c, "⋯").FontSize(20).TextColor(pal.meta) }).Tooltip("Rename or forget")) {
			a.picker = &picker{title: m.Name, options: []string{"rename", "forget · revoke its access"}, pick: func(s string) {
				if s == "rename" {
					a.rename = &renaming{name: m.Name, apply: func(name string) { o.send("rename", "machine", m.ID, "name", name) }}
					return
				}
				o.send("forget", "machine", m.ID)
			}}
		}
	})
	if row.Hovered() {
		row.Background(pal.raised)
	}
	if press(row) {
		switch {
		case len(m.Running) > 0:
			a.open("i:" + m.Running[0].Instance)
		case m.Launch:
			a.explore(m.ID)
		}
	}
	rule(c, pal.rule)
}

func (a *app) inviteScreen(c *ui.Context) {
	o, pal := a.o, p(c)
	inv := o.state.Invitation
	if inv != nil {
		pairing.inviting = false
	}
	invite := func() {
		pairing.inviting, pairing.invited = true, true
		then(o, func(_ any, err error) {
			if err != nil {
				pairing.inviting = false
				a.say(err.Error())
			}
		}, "invite")
	}
	// Asked for once on its own; again only by "new code", so a refusal is not asked again each frame.
	if inv == nil && !pairing.invited {
		invite()
	}
	page(c, func() { header(c, "Invite a device", "", a.back, nil) }, func() {
		left, valid := "creating", false
		code := ""
		if inv != nil {
			left, valid = countdown(c, inv.Expires)
			left += " · single use"
			if !valid {
				left = "expired"
			}
			code = inv.Code
		}
		command := "orb bridge join " + code
		if code == "" {
			command += "…"
		}
		ui.Row(c).FillWidth().Children(func() {
			label(c, "photograph it with a phone, or run on the other computer").Grow(1)
			ui.Text(c, left).FontSize(labelSize).TextColor(pal.meta)
		})
		ink := pal.meta
		if valid {
			ink = pal.fg
		}
		// A phone pairs by photographing the code, as it pairs with a terminal's.
		if valid {
			qrCode(c, code, 200)
		}
		ui.Column(c).FillWidth().Padding(14).Radius(pane).Border(1, pal.fg).Children(func() {
			ui.Text(c, command).FontSize(12).TextColor(ink).Selectable()
		})
		ui.Row(c).Gap(8).Children(func() {
			if code != "" && valid && btn(c, "copy", true, pal.fg, pal.bg) {
				mygo.Clipboard.WriteText(command)
				a.say("copied")
			}
			if code != "" && valid && btn(c, "copy code", false, pal.fg, pal.bg) {
				mygo.Clipboard.WriteText(code)
				a.say("copied")
			}
			if (inv == nil && !pairing.inviting || inv != nil && !valid) && btn(c, "new code", true, pal.fg, pal.bg) {
				invite()
			}
		})
		ui.Box(c).Height(18)
		rule(c, pal.rule)
		ui.Row(c).Gap(10).Children(func() {
			dot(c, pal.mute, 8, true, false)
			ui.Text(c, "waiting for the other device").TextColor(pal.mute)
		})
		ui.Text(c, "A phone photographs the code, or pastes it in its Bridge screen. You approve it here by fingerprint once it claims the code. The code works once, for ten minutes.").
			FontSize(labelSize + 1).TextColor(pal.meta).PaddingY(4)
	})
}

func (a *app) joinScreen(c *ui.Context, code string) {
	o, pal := a.o, p(c)
	if pairing.from != code {
		pairing.from, pairing.code, pairing.err, pairing.editing, pairing.paired = code, code, "", false, time.Time{}
	}
	// What the code is, as the view reads it: an invitation from which device, until when.
	if text := strings.TrimSpace(pairing.code); text != pairing.read {
		pairing.read = text
		pairing.inviter, pairing.expires = "", 0
		then(o, func(r struct {
			Peer    string `json:"peer"`
			Expires int64  `json:"expires"`
		}, err error) {
			if pairing.read == text && err == nil {
				pairing.inviter, pairing.expires = r.Peer, r.Expires
			}
		}, "invitation", "text", text)
	}
	pair := func() {
		pairing.err = ""
		then(o, func(_ any, err error) {
			if err != nil && err.Error() != "cancelled" {
				pairing.err = err.Error()
			}
		}, "join", "text", strings.TrimSpace(pairing.code))
	}
	// Paired: a moment to read it, then back where the app was.
	state := o.state.Joining
	if state == "paired" {
		if pairing.paired.IsZero() {
			pairing.paired = time.Now()
		}
		if time.Since(pairing.paired) > 1100*time.Millisecond {
			o.send("join.cancel")
			a.show(a.tab)
			return
		}
		c.After(1100 * time.Millisecond)
	}
	page(c, func() { header(c, "Pair", "", func() { o.send("join.cancel"); a.back() }, nil) }, func() {
		if state == "" {
			if !pairing.editing && pairing.inviter != "" {
				// A valid code reads as what it is: an invitation from one device, for a while.
				ui.Column(c).FillWidth().Padding(16).Radius(pane).Border(1, pal.fg).Children(func() {
					label(c, "invitation from")
					ui.Text(c, fingerprint(pairing.inviter, 8)).FontSize(28).FontWeight(strong).PaddingY(8)
					ui.Text(c, pairing.inviter).FontSize(labelSize + 1).TextColor(pal.meta).Selectable()
					ui.Row(c).FillWidth().PaddingY(12).Children(func() {
						left, valid := countdown(c, pairing.expires)
						t := ui.Text(c, "valid "+left+" · single use").Grow(1).FontSize(small).TextColor(pal.mute)
						if !valid {
							t.TextColor(rupture)
						}
						if press(ui.Text(c, "edit").FontSize(small).TextColor(pal.meta)) {
							pairing.editing = true
						}
					})
				})
				ui.Text(c, "Pairing lets that device read and drive this computer's conversations. Once it approves this computer, you get its conversations and can start Orb in its folders. Pair only with a device you know.").
					FontSize(labelSize + 1).TextColor(pal.meta)
			} else {
				ui.Text(c, "Paste the code from  orb bridge pair.").TextColor(pal.mute)
				ui.TextAreaBase(c, &pairing.code).Placeholder("orb-bridge:v1:…").PlaceholderColor(pal.meta).FontSize(12).
					FillWidth().MinHeight(120).Padding(16).Radius(pane).Border(1, pal.fg).AutoFocus()
				if pairing.inviter != "" {
					ui.Text(c, "from "+fingerprint(pairing.inviter, 8)).FontSize(labelSize + 1).TextColor(pal.meta)
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				if btn(c, "pair", true, pal.fg, pal.bg) && strings.TrimSpace(pairing.code) != "" {
					pair()
				}
				if btn(c, "paste", false, pal.fg, pal.bg) {
					pairing.code = strings.TrimSpace(mygo.Clipboard.ReadText())
				}
			})
			if pairing.err != "" {
				ui.Text(c, pairing.err).TextColor(rupture)
			}
			return
		}
		heading, sub := "Approve on the other device", "On it, answer y. It shows this computer as"
		if state == "paired" {
			heading, sub = "Paired", "The other device said yes. Its sessions appear on Home."
		}
		ui.Text(c, heading).FontSize(24).FontWeight(strong).Padding(12, 0, 0, 0)
		ui.Text(c, sub).TextColor(pal.mute)
		if state != "paired" {
			// The fingerprint exactly as the terminal prints it, its start large enough to compare at a glance.
			ui.Text(c, fingerprint(o.state.Self, 8)).FontSize(28).FontWeight(strong)
			ui.Text(c, o.state.Self).FontSize(small).TextColor(pal.meta).Selectable()
			ui.Row(c).FillWidth().Gap(10).Padding(8, 0, 0, 0).Children(func() {
				dot(c, pal.mute, 8, true, false)
				waiting := "waiting for the yes"
				if state == "claiming" {
					waiting = "reaching the other device"
				}
				ui.Text(c, waiting).Grow(1).TextColor(pal.mute)
				if btn(c, "cancel", false, pal.fg, pal.bg) {
					o.send("join.cancel")
				}
			})
		}
	})
}

// pairRequest is someone claiming this computer's invitation: its fingerprint, and the owner's yes or no.
func (a *app) pairRequest(c *ui.Context, claim Claim) {
	a.interrupt(c, Ask{
		ID:      "pair:" + claim.Claimant,
		Owner:   "bridge",
		Title:   "Pair with " + fingerprint(claim.Claimant, 8) + "?",
		Message: "Check the other device shows this fingerprint:\n" + claim.Claimant + "\nAllowing lets it read and drive this computer's conversations, run what they run, and start Orb here.",
		Choices: []string{"allow", "deny"},
	}, func(v *string) {
		// Answered once: the claim stays in the view's state until its next reading of the machines.
		a.answered = claim.Claimant
		if v != nil && *v == "allow" {
			a.o.send("approve")
		} else {
			a.o.send("deny")
		}
	})
}

// qrCode draws text as a QR code, module by module, sharp at any size, ink on paper whatever the theme.
func qrCode(c *ui.Context, text string, size float32) {
	code, err := qr.Encode([]byte(text))
	if err != nil || len(code) == 0 {
		return
	}
	ui.Box(c).Size(size, size).Padding(10).Radius(soft).Background(light.bg).Draw(func(p *ui.Painter, r ui.Rect) {
		m := (r.W - 20) / float32(len(code))
		for y, row := range code {
			for x, dark := range row {
				if dark {
					p.Fill(ui.Rect{X: r.X + 10 + float32(x)*m, Y: r.Y + 10 + float32(y)*m, W: m + 0.5, H: m + 0.5}, charcoal, 0)
				}
			}
		}
	})
}
