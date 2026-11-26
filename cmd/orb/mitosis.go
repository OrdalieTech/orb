package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/native"
	"github.com/OrdalieTech/orb/platforms/native/mitosis/daemon"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

func mitosisAdmin(ctx context.Context, profile string) (*protocol.Conn, error) {
	return daemon.Admin(ctx, stateFromContext(ctx), profile)
}

// notifyMitosis asks a running Mitosis daemon to schedule a debounced sync round after
// a local change to a synced class; no-op when Mitosis is not running.
func notifyMitosis(ctx context.Context, state *native.State, profile string) {
	if profile == "" {
		profile = "personal"
	}
	callCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	client, err := daemon.Admin(callCtx, state, profile)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	_ = client.Call(callCtx, "notify", struct{}{}, nil)
}

// debouncedMitosisNotify returns a non-blocking callback that coalesces bursts of local writes
// (e.g. multiple remember/replace/forget calls in one turn) before waking the Mitosis daemon.
func debouncedMitosisNotify(state *native.State, profile string) func() {
	var mu sync.Mutex
	var timer *time.Timer
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Reset(250 * time.Millisecond)
			return
		}
		timer = time.AfterFunc(250*time.Millisecond, func() {
			notifyMitosis(context.Background(), state, profile)
		})
	}
}

func startMitosis(ctx context.Context, profile string, explicit bool) error {
	return daemon.Start(ctx, stateFromContext(ctx), profile, explicit)
}

func runMitosisCommand(ctx context.Context, args []string, streams cliStreams) int {
	profile := "personal"
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--profile" {
			i++
			if i >= len(args) {
				return reportCLIError(streams.Stderr, errors.New("--profile requires a name"))
			}
			profile = args[i]
		} else {
			filtered = append(filtered, args[i])
		}
	}
	args = filtered
	pair := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--yes" || a == "--browser" })
	if len(pair) == 0 && len(args) <= 2 {
		return runMitosisPair(ctx, profile, slices.Contains(args, "--yes"), slices.Contains(args, "--browser"), streams)
	}
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, _ = fmt.Fprintln(streams.Stdout, `orb mitosis [--yes]   (show a code; the machine that joins with it, once you confirm its fingerprint, gets this Orb's conversations, logins and memory; --yes takes the first that claims it, for agents)
orb mitosis --browser [--yes]   (the same for a browser tab, at the link it opens; this Mitosis listens for the page from now on)
orb mitosis join <code>   (join the Orb that showed the code)
orb mitosis spread <ssh-target...>   (install Orb and join a server over SSH; accepts alias, user@host, or ssh flags)
orb mitosis ssh <user@host> [--remote-profile personal] [--remote-orb orb]   (the same for a server, over SSH; installs Orb there)
orb mitosis status|stop|start|block <peer>|service install|remove   [--profile personal]

Advanced (<peer> is a device name, peer-id, or unambiguous prefix):
orb mitosis sync [add|remove|promote|demote <peer> | join <peer>]   (the sync group, which any of its owners changes; remove yourself to leave)
orb mitosis run [--web-listen 127.0.0.1:8789 --web-origin http://127.0.0.1:8787 --web-url wss://host/mitosis]
orb mitosis pair invite | pair join < invitation.json | pair approve <invitation-id> <peer-id>
orb mitosis trust <peer> | grant|revoke|scope|takeover|publish < request.json
orb mitosis remote <peer> <method> [< params.json] | view <peer> <session-id> | shell <peer> [folder]
orb mitosis instances|peers|grants|scopes|prune|pipe`)
		return 0
	}
	if args[0] == "run" {
		var web daemon.Web
		flags := flag.NewFlagSet("orb mitosis run", flag.ContinueOnError)
		flags.SetOutput(streams.Stderr)
		flags.StringVar(&web.Listen, "web-listen", "", "optional WebSocket bind address")
		flags.StringVar(&web.URL, "web-url", "", "public wss:// URL included in invitations")
		flags.Func("web-origin", "allowed browser origin (repeatable)", func(value string) error { web.Origins = append(web.Origins, value); return nil })
		if err := flags.Parse(args[1:]); err != nil {
			return 1
		}
		if flags.NArg() != 0 {
			return reportCLIError(streams.Stderr, errors.New("unexpected mitosis run arguments"))
		}
		if web.Listen != "" {
			if web.URL == "" {
				web.URL = "ws://" + web.Listen
			}
			if err := webtransport.ValidateURL(web.URL); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			if err := webtransport.ValidateOrigins(web.Origins); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
		} else if web.URL != "" || len(web.Origins) != 0 {
			return reportCLIError(streams.Stderr, errors.New("web options require --web-listen"))
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err := daemon.Run(ctx, stateFromContext(ctx), profile, version, web)
		var next daemon.RestartInto
		if errors.As(err, &next) {
			err = native.Exec(string(next), os.Args, os.Environ())
		}
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		return 0
	}
	if args[0] == "service" {
		return runMitosisServiceCommand(ctx, profile, args[1:], streams)
	}
	if args[0] == "start" {
		if daemon.Installed(profile) {
			if err := daemon.Systemctl(ctx, "start", daemon.UnitName(profile)); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
		} else if err := startMitosis(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		enableMitosisPlugin(ctx, profile)
		_, _ = fmt.Fprintf(streams.Stdout, "Mitosis runs (profile %s).\n", profile)
		return 0
	}
	if args[0] == "spread" {
		return runMitosisSpread(ctx, profile, args[1:], streams)
	}
	if args[0] == "ssh" {
		if len(args) < 2 {
			return reportCLIError(streams.Stderr, fmt.Errorf("ssh requires user@host or an SSH alias"))
		}
		target, remoteProfile, remoteOrb := args[1], "personal", "orb"
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--remote-profile", "--remote-orb":
				flag := args[i]
				i++
				if i >= len(args) {
					return reportCLIError(streams.Stderr, fmt.Errorf("%s requires a value", flag))
				}
				if flag == "--remote-profile" {
					remoteProfile = args[i]
				} else {
					remoteOrb = args[i]
				}
			default:
				return reportCLIError(streams.Stderr, fmt.Errorf("unknown ssh option"))
			}
		}
		if _, err := daemon.SSHCommand(target, remoteOrb, remoteProfile); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if err := startMitosis(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		client, err := mitosisAdmin(ctx, profile)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		defer func() { _ = client.Close() }()
		var status mitosisSettingsStatus
		if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		peer, err := connectMitosisSSH(ctx, client, profile, status.PeerID, target, remoteProfile, remoteOrb)
		return reportInSync(ctx, client, streams, peer, err)
	}
	if args[0] == "view" && len(args) == 3 {
		peer := args[1]
		if admin, err := mitosisAdmin(ctx, profile); err == nil {
			if resolved, rerr := peerArg(ctx, admin, peer); rerr == nil {
				peer = resolved
			}
			_ = admin.Close()
		}
		return runMitosisView(ctx, remoteLink{profile: profile, peer: peer, session: args[2]}, streams)
	}
	if args[0] == "shell" && (len(args) == 2 || len(args) == 3) {
		return runMitosisShell(ctx, profile, args[1], strings.Join(args[2:], ""), streams)
	}
	if args[0] == "join" {
		return runMitosisJoin(ctx, profile, args[1:], streams)
	}
	if args[0] == "pipe" {
		return runMitosisPipe(ctx, profile, streams)
	}
	if args[0] == "trust" || len(args) > 1 && args[0] == "pair" && args[1] == "invite" {
		if err := startMitosis(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	client, err := mitosisAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, errors.New("mitosis unavailable; run orb mitosis start"))
	}
	defer func() { _ = client.Close() }()
	method := args[0]
	params := mitosis.JSON(struct{}{})
	read := func() error {
		var e error
		params, e = io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
		if e != nil {
			return e
		}
		if len(params) > protocol.MaxFrame {
			return mitosis.Fail("resource_exhausted")
		}
		e = protocol.Validate(params)
		return e
	}
	switch method {
	case "pair":
		if len(args) < 2 {
			return reportCLIError(streams.Stderr, errors.New("pair requires invite, join, or approve"))
		}
		method = args[1]
		switch method {
		case "invite":
			params = mitosis.JSON(map[string]any{"grants": []mitosis.Grant{mitosis.FullGrant("")}})
		case "join":
			err = read()
		case "approve":
			if len(args) != 4 {
				err = errors.New("approve requires invitation ID and claimant PeerID")
			} else {
				params = mitosis.JSON(map[string]string{"invitation_id": args[2], "claimant": args[3]})
			}
		default:
			err = errors.New("unknown pairing command")
		}
	case "grant", "revoke", "scope", "takeover", "publish":
		err = read()
	case "trust":
		if len(args) != 2 {
			err = errors.New("trust requires PeerID or device name")
		} else if args[1], err = peerArg(ctx, client, args[1]); err == nil {
			err = trustMitosisPeer(ctx, client, profile, args[1], true)
			if err == nil {
				return 0
			}
		}
	case "block":
		if len(args) != 2 {
			err = errors.New("block requires PeerID or device name")
		} else if args[1], err = peerArg(ctx, client, args[1]); err == nil {
			params = mitosis.JSON(map[string]string{"peer_id": args[1]})
		}
	case "sync":
		switch {
		case len(args) == 3 && slices.Contains([]string{"join", "add", "remove", "promote", "demote"}, args[1]):
			if args[2], err = peerArg(ctx, client, args[2]); err == nil {
				params = mitosis.JSON(map[string]string{args[1]: args[2]})
			}
		case len(args) != 1:
			err = errors.New("sync takes join, add, remove, promote or demote <peer>")
		}
	case "remote":
		if len(args) != 3 {
			err = errors.New("remote requires peer and method")
		} else if args[1], err = peerArg(ctx, client, args[1]); err == nil {
			if !streams.StdinTTY {
				var raw []byte
				raw, err = io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
				if err == nil {
					if len(raw) > protocol.MaxFrame {
						err = mitosis.Fail("resource_exhausted")
					} else if strings.TrimSpace(string(raw)) != "" {
						params = raw
						err = protocol.Validate(params)
					}
				}
			}
			if err == nil {
				params = mitosis.JSON(map[string]any{"peer_id": args[1], "method": args[2], "params": params})
			}
		}
	case "prune":
		method = "retire"
	case "status", "instances", "peers", "grants", "scopes", "stop":
	default:
		err = errors.New("unknown mitosis command")
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	var result json.RawMessage
	if err = client.Call(ctx, method, params, &result); err != nil {
		switch {
		case method == "sync" && mitosis.Code(err) == "unauthorized":
			err = errors.New("only the sync group's owners change it")
		case method == "sync" && mitosis.Code(err) == "operation_conflict":
			err = errors.New("a sync group keeps an owner: promote another first")
		case method == "sync" && mitosis.Code(err) == "resource_exhausted":
			err = errors.New("what this profile shares, sealed to every member, exceeds the 64 MiB a sync vault holds")
		case method == "sync" && mitosis.Code(err) == "not_found" && len(args) > 1 && args[1] == "add":
			err = errors.New("pair with that device first: orb mitosis here, orb mitosis join on it")
		case method == "sync" && mitosis.Code(err) == "not_found":
			err = errors.New("no sync group here: add a device to this one, or join another's with orb mitosis sync join <peer-id>")
		}
		return reportCLIError(streams.Stderr, err)
	}
	switch {
	case method == "stop":
		if err = daemon.WaitStopped(ctx, client); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintf(streams.Stdout, "Mitosis stopped (profile %s).\n", profile)
	case method == "block":
		_, _ = fmt.Fprintf(streams.Stdout, "Blocked %s: no channel, grant or vault from now on.\n", args[1])
	case method == "sync" && streams.StdoutTTY:
		var group struct {
			GroupID  string              `json:"group_id"`
			Self     string              `json:"self"`
			Owners   []string            `json:"owners"`
			Members  []string            `json:"members"`
			Names    map[string]string   `json:"names"`
			InSync   bool                `json:"in_sync"`
			Received map[string][]string `json:"received"`
		}
		if json.Unmarshal(result, &group) == nil && group.GroupID != "" {
			status := "syncing"
			if group.InSync {
				status = "in sync"
			}
			_, _ = fmt.Fprintf(streams.Stdout, "Sync group %s (%s, %d devices)\n", group.GroupID, status, len(group.Members))
			for _, m := range group.Members {
				var tags []string
				if m == group.Self {
					tags = append(tags, "this device")
				}
				if slices.Contains(group.Owners, m) {
					tags = append(tags, "owner")
				}
				name := group.Names[m]
				if name == "" {
					name = "(unnamed)"
				}
				if len(tags) > 0 {
					_, _ = fmt.Fprintf(streams.Stdout, "  %s  %s  (%s)\n", m, name, strings.Join(tags, ", "))
				} else {
					_, _ = fmt.Fprintf(streams.Stdout, "  %s  %s\n", m, name)
				}
			}
			for class, items := range group.Received {
				if len(items) > 0 {
					_, _ = fmt.Fprintf(streams.Stdout, "Updated %s: %s\n", class, strings.Join(items, ", "))
				}
			}
			return 0
		}
		_, _ = fmt.Fprintln(streams.Stdout, string(result))
	case method == "status" && streams.StdoutTTY:
		var status struct {
			mitosisSettingsStatus
			Scopes map[string][]string `json:"scopes"`
		}
		_ = json.Unmarshal(result, &status)
		_, _ = fmt.Fprintf(streams.Stdout, "This Orb  %s (profile %s)\n", status.PeerID, profile)
		for _, peer := range status.Peers {
			member := ""
			for _, members := range status.Scopes {
				if slices.Contains(members, peer) && slices.Contains(members, status.PeerID) {
					member = ", sync group"
				}
			}
			name := ""
			if n := strings.TrimSpace(status.PeerNames[peer]); n != "" {
				name = "  " + n
			}
			_, _ = fmt.Fprintf(streams.Stdout, "  %s%s  %s%s\n", peer, name, status.PeerStates[peer], member)
		}
	default:
		_, _ = fmt.Fprintln(streams.Stdout, string(result))
	}
	return 0
}

// peerArg is a device name or a PeerID typed whole or as an unambiguous prefix, with or without
// "orb:ed25519:", of a device this Mitosis knows (paired peers or sync group members); anything
// else goes through as typed.
func peerArg(ctx context.Context, client *protocol.Conn, arg string) (string, error) {
	var status struct {
		mitosisSettingsStatus
		Scopes map[string][]string `json:"scopes"`
	}
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return "", err
	}
	known := slices.Clone(status.Peers)
	for _, members := range status.Scopes {
		for _, p := range members {
			if !slices.Contains(known, p) {
				known = append(known, p)
			}
		}
	}
	var match []string
	for _, p := range known {
		if p == arg {
			return p, nil
		}
		if strings.HasPrefix(p, arg) || strings.HasPrefix(strings.TrimPrefix(p, "orb:ed25519:"), arg) {
			match = append(match, p)
		}
	}
	if len(match) == 0 {
		query := strings.ToLower(strings.TrimSpace(arg))
		for _, p := range known {
			if name := strings.ToLower(strings.TrimSpace(status.PeerNames[p])); name != "" && name == query {
				match = append(match, p)
			}
		}
	}
	if len(match) > 1 {
		return "", fmt.Errorf("%s matches %d devices; type more of it", arg, len(match))
	}
	if _, err := mitosis.ParsePeerID(arg); len(match) == 0 && err != nil {
		return "", fmt.Errorf("no device here matches %s; orb mitosis status lists them", arg)
	}
	return append(match, arg)[0], nil
}

// runMitosisPipe serves the owner admin API as JSON lines for GUI shells, over
// one long-lived connection instead of a process per call:
// {"id":1,"method":"status"} -> {"id":1,"result":…} or {"id":1,"error":{"code":…,"message":…}}.
func runMitosisPipe(ctx context.Context, profile string, streams cliStreams) int {
	if err := startMitosis(ctx, profile, true); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	call, done := mitosisOwner(ctx, profile)
	defer done()
	var mu sync.Mutex // guards output
	out := json.NewEncoder(streams.Stdout)
	lines := bufio.NewScanner(streams.Stdin)
	lines.Buffer(make([]byte, 64<<10), protocol.MaxFrame+1024)
	var calls sync.WaitGroup
	for lines.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(lines.Bytes(), &req) != nil || req.Method == "" {
			continue
		}
		if len(req.Params) == 0 {
			req.Params = json.RawMessage("{}")
		}
		calls.Add(1)
		go func() {
			defer calls.Done()
			reply := map[string]any{"id": req.ID}
			var result json.RawMessage
			if err := call(ctx, req.Method, req.Params, &result); err != nil {
				reply["error"] = map[string]string{"code": mitosis.Code(err), "message": err.Error()}
			} else {
				reply["result"] = result
			}
			mu.Lock()
			_ = out.Encode(reply)
			mu.Unlock()
		}()
	}
	calls.Wait()
	return 0
}

// mitosisOwner calls this machine's Mitosis owner API over one long-lived connection, which it
// opens again, and the Mitosis with it, when either went away. done closes it.
func mitosisOwner(ctx context.Context, profile string) (call func(ctx context.Context, method string, params, result any) error, done func()) {
	var mu sync.Mutex
	var client *protocol.Conn
	connect := func() (*protocol.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if client != nil {
			select {
			case <-client.Done():
				client = nil
			default:
				return client, nil
			}
		}
		c, err := mitosisAdmin(ctx, profile)
		if err != nil {
			// A restarted service keeps its state; bring it back once before failing the call.
			if err = startMitosis(ctx, profile, false); err == nil {
				c, err = mitosisAdmin(ctx, profile)
			}
		}
		client = c
		return c, err
	}
	call = func(ctx context.Context, method string, params, result any) error {
		c, err := connect()
		if err != nil {
			return err
		}
		return c.Call(ctx, method, params, result)
	}
	return call, func() {
		mu.Lock()
		defer mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
	}
}

func connectMitosisSSH(ctx context.Context, client *protocol.Conn, profile, localPeer, target, remoteProfile, remoteOrb string) (string, error) {
	var err error
	remoteOrb, err = daemon.EnsureSSH(ctx, target, remoteOrb, selfupdate.New(version, false))
	if err != nil {
		return "", err
	}
	if _, err := daemon.RunSSH(ctx, target, remoteOrb, remoteProfile, "start"); err != nil {
		return "", err
	}
	raw, err := daemon.RunSSH(ctx, target, remoteOrb, remoteProfile, "pair", "invite")
	if err != nil {
		return "", err
	}
	inv, err := mitosis.ParseInvitation(string(raw))
	if err != nil {
		return "", err
	}
	var claimed mitosis.Invitation
	if err = client.Call(ctx, "join", inv, &claimed); err != nil {
		return "", fmt.Errorf("SSH connected, but Mitosis could not reach the server: %w", err)
	}
	if claimed.ID != inv.ID || claimed.PeerID != inv.PeerID || claimed.Claimant != localPeer {
		return "", fmt.Errorf("pairing identity changed")
	}
	if _, err = daemon.RunSSH(ctx, target, remoteOrb, remoteProfile, "pair", "approve", inv.ID, localPeer); err != nil {
		return "", err
	}
	if err = trustMitosisPeer(ctx, client, profile, inv.PeerID, false); err != nil {
		return "", err
	}
	// The server joins this Orb's sync group (this Orb owns it if it is in none) unless it follows one.
	if _, err = daemon.RunSSH(ctx, target, remoteOrb, remoteProfile, "sync"); err == nil {
		return inv.PeerID, nil
	}
	if err = client.Call(ctx, "sync", map[string]string{"add": inv.PeerID}, nil); err != nil {
		return inv.PeerID, err // unauthorized: this Orb is a member of a group, whose owners add devices
	}
	_, err = daemon.RunSSH(ctx, target, remoteOrb, remoteProfile, "sync", "join", localPeer)
	return inv.PeerID, err
}

// trustMitosisPeer grants peer control of this Orb's conversations, and of the machine too when
// machine is set (`orb mitosis trust`, the owner's explicit choice).
func trustMitosisPeer(ctx context.Context, client *protocol.Conn, profile, peer string, machine bool) error {
	shareOrbsOnMitosis(ctx, profile)
	g := mitosis.ConversationGrant(peer)
	if machine {
		g = mitosis.FullGrant(peer)
	}
	var status mitosisSettingsStatus
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return err
	}
	for _, old := range status.Grants {
		if old.Principal == g.Principal && old.Destination == "" && slices.Equal(old.Permissions, g.Permissions) {
			return nil
		}
	}
	if err := client.Call(ctx, "grant", g, nil); err != nil {
		return fmt.Errorf("could not enable access on this Orb: %w", err)
	}
	return nil
}

func runMitosisServiceCommand(ctx context.Context, profile string, args []string, streams cliStreams) int {
	var err error
	switch strings.Join(args, " ") {
	case "install":
		var note string
		if note, err = daemon.Install(ctx, stateFromContext(ctx), profile); err == nil {
			enableMitosisPlugin(ctx, profile)
			_, _ = fmt.Fprintln(streams.Stdout, strings.TrimSpace("Mitosis runs as a service now: it starts with the machine and restarts if it crashes.\n"+note))
		}
	case "remove":
		if err = daemon.Remove(ctx, profile); err == nil {
			_, _ = fmt.Fprintln(streams.Stdout, "Mitosis service removed; Mitosis now runs only while Orb needs it.")
		}
	default:
		err = errors.New("usage: orb mitosis service install|remove")
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}
