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
	if len(args) == 0 || args[0] == "--help" {
		_, _ = fmt.Fprintln(streams.Stdout, "orb mitosis run|start|stop|status|instances|peers|grants|groups|scopes|prune [--profile personal]\norb mitosis run --web-listen 127.0.0.1:8789 --web-origin http://127.0.0.1:8787 [--web-url wss://host/mitosis]\norb mitosis sync [add|remove <peer-id> | join <owner-peer-id>]   (share credentials across the profile's sync group, sealed per device)\norb mitosis pair   (show a QR code, then approve the device that scans it)\norb mitosis join <code>   (pair with an Orb that ran orb mitosis pair)\norb mitosis service install|remove   (Linux: keep Mitosis running across logouts and reboots)\norb mitosis pair invite | pair join < invitation.json | pair approve <invitation-id> <peer-id>\norb mitosis grant|revoke|scope|group|assign|takeover|publish < request.json\norb mitosis remote <peer-id> <method> < params.json\norb mitosis view <peer-id> <instance-id>\norb mitosis shell <peer-id> [folder]   (a terminal on a machine that lets this one start Orb there)\norb mitosis pipe   (owner API as JSON lines on stdin/stdout)\norb mitosis trust <peer-id>\norb mitosis connect-ssh <user@host> [--remote-profile personal] [--remote-orb orb]")
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
			return 0
		}
		if err := startMitosis(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		return 0
	}
	if args[0] == "connect-ssh" {
		if len(args) < 2 {
			return reportCLIError(streams.Stderr, fmt.Errorf("connect-ssh requires user@host or an SSH alias"))
		}
		target, remoteProfile, remoteOrb := args[1], "personal", "orb"
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--include-future":
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
				return reportCLIError(streams.Stderr, fmt.Errorf("unknown connect-ssh option"))
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
		peer, err := connectMitosisSSH(ctx, client, status.PeerID, target, remoteProfile, remoteOrb)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, peer)
		return 0
	}
	if args[0] == "view" && len(args) == 3 {
		return runMitosisView(ctx, remoteLink{profile: profile, peer: args[1], instance: args[2]}, streams)
	}
	if args[0] == "shell" && (len(args) == 2 || len(args) == 3) {
		return runMitosisShell(ctx, profile, args[1], strings.Join(args[2:], ""), streams)
	}
	if args[0] == "pair" && len(args) == 1 {
		return runMitosisPair(ctx, profile, streams)
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
	case "grant", "revoke", "scope", "group", "assign", "takeover", "publish":
		err = read()
	case "trust":
		if len(args) != 2 {
			err = errors.New("trust requires PeerID")
		} else {
			err = trustMitosisPeer(ctx, client, args[1], true)
			if err == nil {
				return 0
			}
		}
	case "block":
		if len(args) != 2 {
			err = errors.New("block requires PeerID")
		} else {
			params = mitosis.JSON(map[string]string{"peer_id": args[1]})
		}
	case "sync":
		switch {
		case len(args) == 3 && slices.Contains([]string{"join", "add", "remove"}, args[1]):
			params = mitosis.JSON(map[string]string{args[1]: args[2]})
		case len(args) != 1:
			err = errors.New("sync takes join <owner-peer-id>, add <peer-id> or remove <peer-id>")
		}
	case "remote":
		if len(args) != 3 {
			err = errors.New("remote requires PeerID and method")
		} else {
			err = read()
			params = mitosis.JSON(map[string]any{"peer_id": args[1], "method": args[2], "params": params})
		}
	case "prune":
		method = "retire"
	case "status", "instances", "peers", "grants", "groups", "scopes", "stop":
	default:
		err = errors.New("unknown mitosis command")
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	var result json.RawMessage
	if err = client.Call(ctx, method, params, &result); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if method == "stop" {
		if err = daemon.WaitStopped(ctx, client); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	_, _ = fmt.Fprintln(streams.Stdout, string(result))
	return 0
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

func connectMitosisSSH(ctx context.Context, client *protocol.Conn, localPeer, target, remoteProfile, remoteOrb string) (string, error) {
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
	return inv.PeerID, trustMitosisPeer(ctx, client, inv.PeerID, false)
}

// trustMitosisPeer grants peer control of this Orb's conversations, and of the machine too when
// machine is set (`orb mitosis trust`, the owner's explicit choice).
func trustMitosisPeer(ctx context.Context, client *protocol.Conn, peer string, machine bool) error {
	shareOrbsOnMitosis(ctx)
	g := mitosis.ConversationGrant(peer)
	if machine {
		g = mitosis.FullGrant(peer)
	}
	var status mitosisSettingsStatus
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return err
	}
	for _, old := range status.Grants {
		if old.Principal == g.Principal && old.GroupID == "*" && old.IncludeFuture && old.Destination == "" && slices.Equal(old.Permissions, g.Permissions) {
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
