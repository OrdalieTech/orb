package main

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type liveProcess struct {
	Verb   string
	Target string
	Detail string
}

type liveAgent struct {
	TabID     string
	Peer      string
	Where     string
	Title     string
	Status    string
	Hue       int
	Processes []liveProcess
}

type liveInput struct {
	TabID  string
	Peer   string
	Where  string
	Title  string
	Prompt string
	Hue    int
}

type liveRemote struct {
	Instance string
	Peer     string
	Label    string
	Hue      int
}

type liveActivitySnapshot struct {
	Inputs  []liveInput
	Agents  []liveAgent
	Remotes []liveRemote
}

func (s liveActivitySnapshot) empty() bool {
	return len(s.Inputs) == 0 && len(s.Agents) == 0 && len(s.Remotes) == 0
}

// collectLiveActivity gathers all pending inputs, busy tabs with their live tools/subagents,
// and unopened remote running agents into a single immutable snapshot.
func collectLiveActivity(tabs []Tab, rows map[string][]Row, home Home, hueFn func(string) int) liveActivitySnapshot {
	var snap liveActivitySnapshot

	for _, t := range tabs {
		h := 0
		if hueFn != nil {
			h = hueFn(t.Peer)
		}
		title := cmp.Or(strings.TrimSpace(t.Title), strings.TrimSpace(t.Where), "session")

		if t.Ask != nil {
			prompt := cmp.Or(strings.TrimSpace(t.Ask.Title), strings.TrimSpace(t.Ask.Message), "waiting for input")
			snap.Inputs = append(snap.Inputs, liveInput{
				TabID:  t.ID,
				Peer:   t.Peer,
				Where:  t.Where,
				Title:  title,
				Prompt: prompt,
				Hue:    h,
			})
		}

		if t.Busy || t.Streaming {
			var procs []liveProcess
			for _, r := range rows[t.ID] {
				switch r.Kind {
				case "run":
					if !r.Live {
						continue
					}
					foundLiveAction := false
					for _, act := range r.Actions {
						if !act.Live {
							continue
						}
						foundLiveAction = true
						procs = append(procs, liveProcess{
							Verb:   cmp.Or(strings.TrimSpace(act.Verb), "running"),
							Target: strings.TrimSpace(act.Target),
						})
					}
					if !foundLiveAction {
						if label := cmp.Or(strings.TrimSpace(r.Now), strings.TrimSpace(r.What)); label != "" {
							procs = append(procs, liveProcess{
								Verb:   "running",
								Target: label,
							})
						}
					}
				case "peer":
					if r.Exchange == nil || !r.Exchange.Live {
						continue
					}
					ex := r.Exchange
					who := cmp.Or(strings.TrimSpace(ex.Who), strings.TrimSpace(ex.Scope), "subagent")
					target := firstNonEmptyLine(ex.Text)
					detail := firstNonEmptyLine(ex.Steps)
					if detail == "" {
						detail = strings.TrimSpace(ex.Status)
					}
					procs = append(procs, liveProcess{
						Verb:   who,
						Target: target,
						Detail: detail,
					})
				}
			}

			snap.Agents = append(snap.Agents, liveAgent{
				TabID:     t.ID,
				Peer:      t.Peer,
				Where:     t.Where,
				Title:     title,
				Status:    cmp.Or(strings.TrimSpace(t.Status), "working…"),
				Hue:       h,
				Processes: procs,
			})
		}
	}

	for _, m := range home.Machines {
		for _, r := range m.Running {
			if !r.Busy || r.Open {
				continue
			}
			h := m.Hue
			if h == 0 && hueFn != nil {
				h = hueFn(m.ID)
			}
			snap.Remotes = append(snap.Remotes, liveRemote{
				Instance: r.Instance,
				Peer:     cmp.Or(strings.TrimSpace(m.Name), m.ID),
				Label:    cmp.Or(strings.TrimSpace(r.Label), r.Instance, "running"),
				Hue:      h,
			})
		}
	}

	return snap
}

// liveActivitySummary formats a compact one-line summary of the live activity snapshot.
func liveActivitySummary(s liveActivitySnapshot) string {
	if s.empty() {
		return ""
	}
	var parts []string
	if n := len(s.Agents) + len(s.Remotes); n > 0 {
		parts = append(parts, fmt.Sprintf("%d running", n))
	}
	if n := len(s.Inputs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d input", n))
	}

	highlight := ""
	for _, ag := range s.Agents {
		if len(ag.Processes) > 0 {
			pr := ag.Processes[0]
			switch {
			case pr.Target != "":
				highlight = pr.Verb + ": " + pr.Target
			case pr.Detail != "":
				highlight = pr.Verb + ": " + pr.Detail
			default:
				highlight = pr.Verb
			}
			break
		}
	}
	if highlight == "" && len(s.Inputs) > 0 && s.Inputs[0].Prompt != "" {
		highlight = s.Inputs[0].Prompt
	}
	if highlight == "" && len(s.Agents) > 0 && s.Agents[0].Status != "" && s.Agents[0].Status != "working…" {
		highlight = s.Agents[0].Status
	}
	if highlight == "" && len(s.Remotes) > 0 && s.Remotes[0].Label != "" {
		highlight = s.Remotes[0].Label
	}
	if highlight != "" {
		parts = append(parts, highlight)
	}

	return strings.Join(parts, " · ")
}

// liveActivityBar renders a quiet 1-line collapsible & expandable Live Activity bar.
// When nothing is active, it renders nothing (zero height, zero layout change).
func (a *app) liveActivityBar(c *ui.Context, key, currentTabID string) {
	s := collectLiveActivity(a.o.state.Tabs, a.o.rows, a.o.home, a.o.hue)
	if s.empty() {
		return
	}

	pal := p(c)
	expKey := "live:" + key
	open := a.expanded[expKey]
	hasInputs := len(s.Inputs) > 0

	dotColor := pal.fg
	ink := pal.fg
	if hasInputs {
		dotColor = rupture
		ink = rupture
	}
	chevron := "›"
	if open {
		chevron = "⌄"
	}

	ui.Column(c).FillWidth().Unselectable().Children(func() {
		hdr := ui.Row(c).FillWidth().Padding(4, 2).Radius(soft).Gap(8).AlignItems(ui.Center).Children(func() {
			dot(c, dotColor, 5, true, false)
			ui.Text(c, liveActivitySummary(s)).Grow(1).Basis(0).FontSize(small).TextColor(ink).SingleLine().Ellipsis("…")
			ui.Text(c, chevron).FontSize(small).TextColor(pal.meta).SingleLine()
		})
		if press(hover(hdr, pal.hover)) {
			a.expanded[expKey] = !open
		}

		if !open {
			return
		}

		ui.Scroll(c).FillWidth().MaxHeight(220).Transition(fold).Padding(4, 0, 6, 4).Children(func() {
			ui.Row(c).FillWidth().Gap(10).Children(func() {
				ui.Box(c).Width(1).FillHeight().Background(pal.rule)
				ui.Column(c).Grow(1).Basis(0).Gap(6).Children(func() {
					// 1. Pending inputs
					for _, in := range s.Inputs {
						row := ui.Row(c.Key("in:"+in.TabID)).FillWidth().Padding(3, 6).Radius(soft).Gap(8).AlignItems(ui.Center).Children(func() {
							dot(c, rupture, 6, true, true)
							ui.Text(c, "input").FontSize(12).FontWeight(medium).TextColor(rupture).SingleLine()
							ui.Text(c, in.Prompt).Grow(1).Basis(0).FontSize(12).TextColor(pal.fg).SingleLine().Ellipsis("…")
							if in.Where != "" {
								ui.Text(c, in.Where).FontSize(11).TextColor(pal.meta).SingleLine()
							}
						})
						if press(hover(row, pal.hover)) {
							a.show(in.TabID)
						}
					}

					// 2. Running agents & their active processes/tools/subagents
					for _, ag := range s.Agents {
						ui.Column(c.Key("ag:"+ag.TabID)).FillWidth().Gap(2).Children(func() {
							titleInk := pal.fg
							if ag.TabID == currentTabID {
								titleInk = pal.fg
							}
							agRow := ui.Row(c).FillWidth().Padding(3, 6).Radius(soft).Gap(8).AlignItems(ui.Center).Children(func() {
								where(c, ag.Hue, true, false, pal.fg)
								ui.Text(c, ag.Title).Grow(1).Basis(0).FontSize(12).FontWeight(medium).TextColor(titleInk).SingleLine().Ellipsis("…")
								if ag.Where != "" {
									ui.Text(c, ag.Where).FontSize(11).TextColor(pal.meta).SingleLine()
								}
							})
							if press(hover(agRow, pal.hover)) {
								a.show(ag.TabID)
							}

							ui.Row(c).FillWidth().Padding(0, 0, 0, 9).Gap(8).Children(func() {
								ui.Box(c).Width(1).FillHeight().Background(pal.rule.Alpha(0.6))
								ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
									if len(ag.Processes) == 0 {
										ui.Text(c, ag.Status).FontSize(11).TextColor(pal.meta).SingleLine().Ellipsis("…")
										return
									}
									for i, pr := range ag.Processes {
										pRow := ui.Row(c.Key(fmt.Sprintf("p:%s:%d", ag.TabID, i))).FillWidth().Padding(2, 4).Radius(soft).Gap(6).AlignItems(ui.Center).Children(func() {
											ui.Text(c, pr.Verb).FontSize(11).FontWeight(medium).TextColor(pal.fg).SingleLine()
											detail := pr.Target
											if pr.Detail != "" {
												if detail != "" {
													detail += " · " + pr.Detail
												} else {
													detail = pr.Detail
												}
											}
											if detail != "" {
												ui.Text(c, detail).Grow(1).Basis(0).FontSize(11).TextColor(pal.meta).SingleLine().Ellipsis("…")
											}
										})
										if press(hover(pRow, pal.hover)) {
											a.show(ag.TabID)
										}
									}
								})
							})
						})
					}

					// 3. Unopened remote running agents
					for _, rem := range s.Remotes {
						rRow := ui.Row(c.Key("rem:"+rem.Instance)).FillWidth().Padding(3, 6).Radius(soft).Gap(8).AlignItems(ui.Center).Children(func() {
							where(c, rem.Hue, true, false, pal.fg)
							ui.Text(c, rem.Label).Grow(1).Basis(0).FontSize(12).TextColor(pal.fg).SingleLine().Ellipsis("…")
							if rem.Peer != "" {
								ui.Text(c, rem.Peer).FontSize(11).TextColor(pal.meta).SingleLine()
							}
						})
						if press(hover(rRow, pal.hover)) {
							a.open("i:" + rem.Instance)
						}
					}
				})
			})
		})
	})
}
