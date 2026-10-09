package view

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/bridge"
)

// Claim is a device that claimed this machine's invitation and waits for its owner's yes.
type Claim struct {
	Invitation string `json:"invitation"`
	Claimant   string `json:"claimant"`
}

// Invitation is the code another device joins this machine with; it works once, until Expires.
type Invitation struct {
	id      string
	Code    string `json:"code"`
	Expires int64  `json:"expires"` // seconds
}

// Provider is one way to models on a machine, as `orb login --json` lists it: the TUI's /login.
type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Methods []Method `json:"methods"`
	Models  int      `json:"models"`
	Ready   bool     `json:"ready,omitempty"`
	Holds   string   `json:"holds,omitempty"`  // where its credential lives, in words
	Status  string   `json:"status,omitempty"` // the credential's type
	Source  string   `json:"source,omitempty"`
}

type Method struct {
	Auth    string `json:"auth"` // oauth · api_key
	Label   string `json:"label"`
	About   string `json:"about,omitempty"`
	Account bool   `json:"account,omitempty"`
}

// Account is a connected account and its plan limits, as `orb accounts --json` lists it.
type Account struct {
	Provider     string   `json:"provider"`
	ProviderName string   `json:"provider_name"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Active       bool     `json:"active,omitempty"`
	Plan         string   `json:"plan,omitempty"`
	Windows      []Window `json:"windows,omitempty"`
}

// Login is a sign-in a machine runs for this app, as the TUI's /login runs it.
type Login struct {
	Machine      string  `json:"machine"`
	Provider     string  `json:"provider"`
	State        string  `json:"state"` // starting · browser · code · asking · done · failed
	URL          string  `json:"url,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	Code         string  `json:"code,omitempty"`
	Detail       string  `json:"detail,omitempty"`
	Prompt       *Prompt `json:"prompt,omitempty"`
}

// Prompt is what a sign-in asks: a menu, a line of text, a secret, or a pasted code or URL.
type Prompt struct {
	Kind        string   `json:"kind"`
	Message     string   `json:"message"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Completion is what an `@` token completes to: the text that replaces it, and how to show it.
type Completion struct {
	Text   string `json:"text"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

var upper = regexp.MustCompile(`^[A-Z0-9_, ]+$`)

// slow returns the work of an intent that waits on a machine, nil for one that does not.
func (a *App) slow(in intent) func() (any, error) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	call := func(f func() (any, error)) func() (any, error) {
		return func() (any, error) { defer cancel(); return f() }
	}
	a.mu.Lock()
	t, self := a.tab(in.Tab), a.self
	a.mu.Unlock()
	machine := cmp.Or(in.Machine, self)
	// A machine's own calls; a refusal says why: it does not let this app, or its Orb is too old.
	host := func(method, what string, params, result any) error {
		return a.refused(machine, what, a.remote(ctx, machine, method, params, result))
	}
	switch in.Do {
	case "open":
		return call(func() (any, error) { return a.open(in.Key) })
	case "start":
		cwd := in.CWD
		if cwd == "" && machine == self {
			cwd = a.o.CWD
		}
		return call(func() (any, error) { return a.start(machine, cwd, "", in.Text) })
	case "send", "rename":
		if in.Tab != "" || in.Machine != "" && in.Do == "rename" {
			break
		}
		if in.Do == "send" {
			return call(func() (any, error) { return a.sendHome(in.Text) })
		}
		return call(func() (any, error) { return a.renameThread(in.Key, in.Name) })
	case "delete":
		return call(func() (any, error) { return a.delete(in.Key) })
	case "complete", "image":
		return call(func() (any, error) {
			if t == nil {
				return nil, errNoTab
			}
			if in.Do == "image" {
				var r struct {
					Data string `json:"data"`
				}
				return r, t.read("image", map[string]any{"ref": in.Ref, "size": in.Px}, &r)
			}
			var r struct {
				Items []Completion `json:"items"`
			}
			return r.Items, t.read("complete", map[string]string{"query": in.Text}, &r)
		})
	case "providers":
		return call(func() (any, error) {
			var r struct{ Providers []json.RawMessage }
			if err := host("host.providers", "list its providers", struct{}{}, &r); err != nil {
				return nil, err
			}
			return providers(r.Providers), nil
		})
	case "accounts":
		return call(func() (any, error) {
			var r struct{ Accounts []json.RawMessage }
			if err := host("host.accounts", "list its accounts", struct{}{}, &r); err != nil {
				return nil, err
			}
			return accounts(r.Accounts), nil
		})
	case "account":
		return call(func() (any, error) {
			var r struct {
				Accounts []struct{ Type, Message string }
			}
			err := host("host.accounts.use", "switch its accounts", map[string]string{"provider": in.Provider, "id": in.Account}, &r)
			for _, row := range r.Accounts {
				if row.Type == "error" {
					return nil, errors.New(row.Message)
				}
			}
			return nil, err
		})
	case "login":
		return func() (any, error) { cancel(); a.signIn(machine, in.Provider, in.Auth); return nil, nil }
	case "update":
		return call(func() (any, error) {
			var r struct{ Status, To string }
			err := host("host.update", "update itself", struct{}{}, &r)
			return strings.Trim(r.Status+" · "+r.To, " ·"), err
		})
	case "invite":
		return call(func() (any, error) {
			var inv bridge.Invitation
			if err := a.o.Call(ctx, "invite", map[string]any{"grants": []bridge.Grant{bridge.FullGrant("")}}, &inv); err != nil {
				return nil, err
			}
			a.mu.Lock()
			a.invitation = &Invitation{id: inv.ID, Code: bridge.InvitationCode(inv), Expires: inv.Expires}
			a.flush()
			a.mu.Unlock()
			return nil, nil
		})
	case "join":
		return func() (any, error) { cancel(); return nil, a.join(in.Text) }
	case "invitation":
		// What a pasted or scanned code is: an invitation from which device, until when.
		return func() (any, error) {
			cancel()
			inv, err := bridge.ParseInvitation(in.Text)
			return map[string]any{"peer": inv.PeerID, "expires": inv.Expires}, err
		}
	case "approve", "deny":
		return call(func() (any, error) {
			a.mu.Lock()
			claim := a.claim
			a.mu.Unlock()
			if claim == nil {
				return nil, nil
			}
			var err error
			if in.Do == "deny" {
				err = a.o.Call(ctx, "block", map[string]string{"peer_id": claim.Claimant}, nil)
			} else if err = a.o.Call(ctx, "approve", map[string]string{"invitation_id": claim.Invitation, "claimant": claim.Claimant}, nil); bridge.Code(err) == "identity_conflict" {
				err = errors.New("two devices used this code, so someone else saw it: not paired · invite again where only you see the code")
			}
			a.wakeMachines()
			return nil, err
		})
	case "forget":
		return call(func() (any, error) {
			err := a.o.Call(ctx, "block", map[string]string{"peer_id": in.Machine}, nil)
			a.wakeMachines()
			return nil, err
		})
	case "restart":
		// Bridge starts again on the next call, with what changed in its environment (the phone's
		// Linux); the Orbs it started end, to reopen at their next message.
		return call(func() (any, error) { err := a.o.Call(ctx, "stop", struct{}{}, nil); a.wakeMachines(); return nil, err })
	case "plugins":
		// The machine's plugins as `orb plugins list --json` lists them: name, on, about, and
		// choices with the value each has.
		return call(func() (any, error) {
			var r struct{ Plugins []json.RawMessage }
			err := host("host.plugins", "list its plugins", struct{}{}, &r)
			return r.Plugins, err
		})
	case "plugin", "logout":
		// A plugin turned on or off, one of its choices, a provider signed out: the machine's Orbs
		// read it when they start, and those its Bridge started for this app reopen with it.
		method, what, params := "host.plugins.set", "change its plugins", map[string]any{"name": in.Name}
		switch {
		case in.Do == "logout":
			method, what, params = "host.logout", "sign it out", map[string]any{"provider": in.Provider}
		case in.Key != "" && in.Value != nil:
			params["key"], params["value"] = in.Key, *in.Value
		default:
			params["on"] = in.On // none is no change: the machine refuses it
		}
		return call(func() (any, error) {
			var r struct{ Error string }
			if err := host(method, what, params, &r); err != nil || r.Error == "" {
				return nil, err
			}
			return nil, errors.New(r.Error)
		})
	}
	cancel()
	return nil
}

// refused says why a machine did not do [what]: it does not let this app, or its Orb predates the
// call; other errors stand.
func (a *App) refused(machine, what string, err error) error {
	a.mu.Lock()
	name := "that device"
	if m := a.machine(machine); m != nil {
		name = a.name(m)
	}
	a.mu.Unlock()
	switch bridge.Code(err) {
	case "unauthorized":
		return fmt.Errorf("%s does not let %s %s", name, a.o.Name, what)
	case "method_not_found":
		return fmt.Errorf("%s runs an Orb too old to %s from here: update it", name, what)
	}
	return err
}

// providers groups `orb login --json` rows by provider, the way /login lists them.
func providers(rows []json.RawMessage) []Provider {
	var out []Provider
	for _, raw := range rows {
		var r struct {
			ID, Name, Auth, Method, Label string
			Models                        int
			Status                        *struct{ Type, Source string }
		}
		if json.Unmarshal(raw, &r) != nil || r.ID == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1].ID != r.ID {
			p := Provider{ID: r.ID, Name: cmp.Or(r.Name, r.ID), Models: r.Models}
			if s := r.Status; s != nil {
				p.Ready, p.Status, p.Source = true, s.Type, s.Source
				switch {
				case s.Type == "oauth":
					p.Holds = "account"
				case s.Source == "stored":
					p.Holds = "api key"
				case upper.MatchString(s.Source):
					p.Holds = "key in this app"
				default:
					p.Holds = strings.ReplaceAll(s.Source, "_", " ")
				}
			}
			out = append(out, p)
		}
		p := &out[len(out)-1]
		p.Methods = append(p.Methods, Method{Auth: r.Auth, Label: r.Label, About: r.Method, Account: r.Auth == "oauth"})
	}
	return out
}

func accounts(rows []json.RawMessage) []Account {
	var out []Account
	for _, raw := range rows {
		var r struct {
			Account
			Usage *struct {
				Plan    string
				Windows []struct {
					Name      string
					Remaining float64
					ResetsAt  time.Time `json:"resets_at"`
				}
			}
		}
		if json.Unmarshal(raw, &r) != nil || r.Provider == "" {
			continue
		}
		if u := r.Usage; u != nil {
			r.Plan = u.Plan
			for _, w := range u.Windows {
				r.Windows = append(r.Windows, Window{w.Name, w.Remaining, max(0, w.ResetsAt.UnixMilli())})
			}
		}
		out = append(out, r.Account)
	}
	return out
}

// join claims an invitation, waits until the inviter approves this machine's fingerprint, then
// gives the inviter its conversations back: pairing is mutual, and nothing is trusted before the yes.
func (a *App) join(code string) error {
	inv, err := bridge.ParseInvitation(code)
	if err != nil {
		return err
	}
	set := func(state string) {
		a.mu.Lock()
		a.joining = state
		a.flush()
		a.mu.Unlock()
	}
	ctx, cancel := context.WithDeadline(a.ctx, time.Unix(inv.Expires, 0))
	defer cancel()
	set("claiming")
	if err := a.o.Call(ctx, "join", inv, nil); err != nil {
		set("")
		if bridge.Code(err) == "identity_conflict" {
			return errors.New("another device already used this code: ask for a new one")
		}
		return err
	}
	set("waiting")
	for {
		a.mu.Lock()
		waiting := a.joining == "waiting"
		a.mu.Unlock()
		if !waiting {
			return errors.New("cancelled")
		}
		var i bridge.Invitation
		err := a.remote(ctx, inv.PeerID, "pair.status", map[string]string{"invitation_id": inv.ID}, &i)
		switch {
		case bridge.Code(err) == "not_found":
			set("")
			return errors.New("the computer declined, or the code expired")
		case i.Status == "approved":
			if err := a.o.Call(ctx, "grant", bridge.ConversationGrant(inv.PeerID), nil); err != nil && bridge.Code(err) != "identity_conflict" {
				set("")
				return err
			}
			set("paired")
			a.wakeMachines()
			return nil
		}
		select {
		case <-ctx.Done():
			set("")
			return errors.New("not approved in time · run orb bridge pair again")
		case <-time.After(time.Second):
		}
	}
}

// signIn is a sign-in a machine runs for this app (host.login.*), this machine's own included:
// its link, device code and questions come to the app's state, the answers go back.
type signIn struct {
	a     *App
	state Login
	id    string
	stop  context.CancelFunc
}

func (a *App) signIn(machine, provider, auth string) {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
	s := &signIn{a: a, state: Login{Machine: machine, Provider: provider, State: "starting"}, stop: cancel}
	a.mu.Lock()
	if a.login != nil {
		a.login.cancel()
	}
	a.login = s
	a.flush()
	a.mu.Unlock()
	update := func(f func(l *Login)) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.login == s {
			f(&s.state)
			a.flush()
		}
	}
	defer update(func(l *Login) {
		if l.State != "done" && l.State != "failed" {
			l.State, l.Detail = "failed", cmp.Or(l.Detail, "sign-in stopped")
		}
	})
	var started struct {
		ID string `json:"login_id"`
	}
	if err := a.remote(ctx, machine, "host.login.start", map[string]string{"provider": provider, "method": auth}, &started); err != nil {
		err = a.refused(machine, "sign it in", err)
		update(func(l *Login) { l.State, l.Detail = "failed", err.Error() })
		return
	}
	a.mu.Lock()
	s.id = started.ID
	a.mu.Unlock()
	for cursor := 0; ctx.Err() == nil; {
		var page struct {
			Events []json.RawMessage
			Cursor int
			Done   bool
		}
		if err := a.remote(ctx, machine, "host.login.poll", map[string]any{"login_id": s.id, "cursor": cursor}, &page); err != nil {
			update(func(l *Login) { l.State, l.Detail = "failed", cmp.Or(l.Detail, err.Error()) })
			return
		}
		for _, raw := range page.Events {
			var e struct {
				Type, URL, URI, Instructions, Code, Message, Kind, Placeholder string
				Options                                                        []Option
			}
			_ = json.Unmarshal(raw, &e)
			update(func(l *Login) {
				switch e.Type {
				case "auth_url":
					l.URL, l.Instructions, l.State = web(e.URL), e.Instructions, "browser"
				case "device_code":
					l.Code, l.URL, l.State = e.Code, web(e.URI), "code"
				case "progress", "info":
					l.Detail = e.Message
				case "prompt":
					l.Prompt = &Prompt{e.Kind, e.Message, e.Placeholder, e.Options}
					// A pasted code is the fallback while the browser is out; anything else is the question now.
					if e.Kind != "manual_code" {
						l.State = "asking"
					}
				case "done":
					l.State, l.Prompt = "done", nil
				case "error":
					l.State, l.Prompt, l.Detail = "failed", nil, e.Message
				}
			})
		}
		if cursor = page.Cursor; page.Done {
			return
		}
	}
}

// answer replies to the sign-in's open prompt; called with the lock held.
func (s *signIn) answer(value string) {
	if s == nil {
		return
	}
	s.state.Prompt = nil
	if s.state.State == "asking" {
		s.state.State = "starting"
	}
	machine, id := s.state.Machine, s.id
	go func() {
		_ = s.a.remote(s.a.ctx, machine, "host.login.answer", map[string]string{"login_id": id, "value": value}, nil)
	}()
}

// cancel ends the sign-in on its machine; called with the lock held.
func (s *signIn) cancel() {
	if s == nil {
		return
	}
	s.stop()
	machine, id := s.state.Machine, s.id
	go func() { _ = s.a.remote(s.a.ctx, machine, "host.login.cancel", map[string]string{"login_id": id}, nil) }()
}
