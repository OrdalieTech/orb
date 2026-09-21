package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/hosts/native"
	transport "github.com/OrdalieTech/orb/bridge/transports/tailcat"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

func validBridgeName(s string) bool {
	if len(s) == 0 || len(s) > 48 {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}
func bridgeDir(profile string) (string, error) {
	if !validBridgeName(profile) {
		return "", errors.New("invalid bridge profile")
	}
	if root := os.Getenv("ORB_BRIDGE_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("ORB_BRIDGE_HOME must be absolute")
		}
		return filepath.Join(root, profile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orb", "bridge", profile), nil
}
func bridgeAdmin(ctx context.Context, profile string) (*protocol.Conn, error) {
	dir, err := bridgeDir(profile)
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(filepath.Join(dir, "admin.token"))
	if err != nil {
		return nil, err
	}
	return native.Dial(ctx, filepath.Join(dir, "admin.sock"), native.Auth{Credential: string(token)}, nil, nil)
}

func waitBridgeStopped(ctx context.Context, client *protocol.Conn) error {
	select {
	case <-client.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return fmt.Errorf("bridge is still stopping; refresh its status before restarting")
	}
}

func bridgeServiceReady(ctx context.Context, client *protocol.Conn) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status bridgeSettingsStatus
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return false, err
	}
	if status.SupportsFullAccess {
		return true, nil
	}
	if err := client.Call(ctx, "stop", struct{}{}, nil); err != nil {
		return false, err
	}
	return false, waitBridgeStopped(ctx, client)
}

func startBridge(ctx context.Context, profile string, explicit bool) error {
	dir, err := bridgeDir(profile)
	if err != nil {
		return err
	}
	if explicit {
		if err = os.Remove(filepath.Join(dir, "stopped")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if _, err = os.Stat(filepath.Join(dir, "stopped")); err == nil {
			return errors.New("bridge stopped; start it explicitly")
		}
	}
	if c, err := bridgeAdmin(ctx, profile); err == nil {
		ready, err := bridgeServiceReady(ctx, c)
		_ = c.Close()
		if err != nil || ready {
			return err
		}
		// The older daemon writes its deliberate-stop marker during replacement.
		if err = os.Remove(filepath.Join(dir, "stopped")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "service.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command(exe, "bridge", "run", "--profile", profile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("bridge startup failed; see private service.log")
		case <-ticker.C:
			if c, err := bridgeAdmin(ctx, profile); err == nil {
				_ = c.Close()
				return nil
			}
		}
	}
}

type bridgeService struct {
	profile string
	b       *bridge.Bridge
	node    *transport.Node
	mu      sync.Mutex
	peers   map[string]*protocol.Conn
	ctx     context.Context
}

func (s *bridgeService) peer(ctx context.Context, id, locator string) (*protocol.Conn, error) {
	if c := s.b.Connection(id); c != nil {
		return c, nil
	}
	s.mu.Lock()
	c := s.peers[id]
	s.mu.Unlock()
	if c != nil {
		select {
		case <-c.Done():
		default:
			return c, nil
		}
	}
	if locator == "" {
		var err error
		locator, err = s.b.PeerLocator(id)
		if err != nil {
			return nil, err
		}
	}
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stream, err := s.node.Dial(timeout, id, locator)
	if err != nil {
		return nil, err
	}
	c, _, err = s.b.Connect(timeout, stream, id, false)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	old := s.peers[id]
	s.peers[id] = c
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return c, nil
}
func (s *bridgeService) remote(ctx context.Context, id, method string, p any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := s.peer(ctx, id, "")
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = c.Call(ctx, method, p, &result)
	if err != nil {
		var rpc *protocol.RPCError
		if !errors.As(err, &rpc) {
			_ = c.Close()
		}
	}
	return result, err
}
func (s *bridgeService) outbound(ctx context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		PeerID string       `json:"peer_id"`
		Call   connect.Call `json:"call"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	subject, err := s.b.Outbound(native.InstanceFromContext(ctx), p.PeerID, p.Call)
	if err != nil {
		return nil, err
	}
	return s.remote(ctx, p.PeerID, "instances.call", struct {
		connect.Call
		Subject connect.Subject `json:"subject"`
	}{p.Call, subject})
}
func (s *bridgeService) admin(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "block":
		raw, err := s.b.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var p struct {
			PeerID string `json:"peer_id"`
		}
		if err = json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		db, err := openBridgeCache(ctx, s.profile)
		if err != nil {
			return nil, err
		}
		defer func() { _ = db.Close() }()
		return raw, db.Foreign(s.profile).Forget(ctx, p.PeerID)
	case "invite":
		raw, err := s.b.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invite bridge.Invitation
		_ = json.Unmarshal(raw, &invite)
		invite.Locator, err = s.node.Locator()
		return connect.JSON(invite), err
	case "join":
		var inv bridge.Invitation
		if err := protocol.Decode(params, &inv); err != nil {
			return nil, err
		}
		c, err := s.peer(ctx, inv.PeerID, inv.Locator)
		if err != nil {
			return nil, err
		}
		var result bridge.Invitation
		locator, err := s.node.Locator()
		if err != nil {
			return nil, err
		}
		err = c.Call(ctx, "pair.claim", map[string]string{"invitation_id": inv.ID, "token": inv.Token, "locator": locator}, &result)
		if err != nil {
			return nil, err
		}
		if err = s.b.SavePeer(inv.PeerID, inv.Locator); err != nil {
			return nil, err
		}
		return connect.JSON(result), nil
	case "remote":
		var p struct {
			PeerID string          `json:"peer_id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return s.remote(ctx, p.PeerID, p.Method, p.Params)
	case "publish":
		var p struct {
			Scope    string `json:"scope_id"`
			Name     string `json:"display_name"`
			Withdraw bool   `json:"withdrawn"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		locator, err := s.node.Locator()
		if err != nil {
			return nil, err
		}
		r, err := s.b.PublishContact(p.Scope, p.Name, []bridge.Locator{{Transport: "tailcat/1", Locator: locator}}, p.Withdraw)
		return connect.JSON(r), err
	default:
		return s.b.Admin(ctx, method, params)
	}
}
func runBridgeService(ctx context.Context, profile string) error {
	dir, err := bridgeDir(profile)
	if err != nil {
		return err
	}
	store, err := native.OpenStore(filepath.Join(dir, "state.json"), protocol.MaxFrame)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	_, tokenErr := os.Stat(filepath.Join(dir, "admin.token"))
	b, err := bridge.Open(store, errors.Is(tokenErr, os.ErrNotExist))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	tokenPath := filepath.Join(dir, "admin.token")
	token, err := os.ReadFile(tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		var v [32]byte
		_, _ = rand.Read(v[:])
		token = []byte(base64.RawURLEncoding.EncodeToString(v[:]))
		err = os.WriteFile(tokenPath, token, 0600)
	}
	if err != nil {
		return err
	}
	if len(token) != 43 {
		return errors.New("corrupt bridge admin credential")
	}
	node, err := transport.Open(b.TransportState(), b.SaveTransportState, "https://tailcat.dev/derpmap.json")
	if err != nil {
		return err
	}
	defer func() { _ = node.Close() }()
	startup, cancel := context.WithTimeout(ctx, 25*time.Second)
	listener, err := node.Listen(startup)
	cancel()
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	serviceCtx, stop := context.WithCancel(ctx)
	defer stop()
	service := &bridgeService{profile: profile, b: b, node: node, peers: map[string]*protocol.Conn{}, ctx: serviceCtx}
	admin := func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "stop" {
			var p struct{}
			if err := protocol.Decode(params, &p); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dir, "stopped"), []byte("stopped\n"), 0600); err != nil {
				return nil, err
			}
			time.AfterFunc(100*time.Millisecond, stop)
			return connect.JSON(struct{}{}), nil
		}
		return service.admin(ctx, method, params)
	}
	closeAdmin, err := native.Listen(serviceCtx, filepath.Join(dir, "admin.sock"), b, string(token), admin, nil)
	if err != nil {
		return err
	}
	defer closeAdmin()
	closeAttach, err := native.Listen(serviceCtx, filepath.Join(dir, "attach.sock"), b, "", nil, service.outbound)
	if err != nil {
		return err
	}
	defer closeAttach()
	slots := make(chan struct{}, 64)
	go func() {
		for {
			stream, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func(c net.Conn) {
					defer func() { <-slots }()
					peer, _, err := b.Connect(serviceCtx, c, "", true)
					if err == nil {
						select {
						case <-serviceCtx.Done():
							_ = peer.Close()
						case <-peer.Done():
						}
					}
				}(stream)
			default:
				_ = stream.Close()
			}
		}
	}()
	go service.reconcile()
	<-serviceCtx.Done()
	return nil
}
func (s *bridgeService) reconcile() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.b.Changes():
		}
		{
			raw, err := s.b.Admin(s.ctx, "status", connect.JSON(struct{}{}))
			if err != nil {
				continue
			}
			var status struct {
				Scopes map[string][]string `json:"scopes"`
			}
			_ = json.Unmarshal(raw, &status)
			for scope, peers := range status.Scopes {
				for _, peer := range peers {
					if peer == s.b.PeerID() {
						continue
					}
					ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
					c, err := s.peer(ctx, peer, "")
					if err == nil {
						cursor := ""
						for pages := 0; pages < 16; pages++ {
							var page struct {
								Items  []bridge.Record `json:"items"`
								Cursor string          `json:"cursor"`
							}
							err = c.Call(ctx, "peers.list", map[string]string{"scope_id": scope, "cursor": cursor}, &page)
							if err != nil {
								break
							}
							for _, r := range page.Items {
								_, _ = s.b.MergeContact(peer, r)
							}
							cursor = page.Cursor
							if cursor == "" {
								break
							}
						}
						records, e := s.b.Contacts(peer, scope)
						if e == nil {
							for len(records) > 0 {
								n := min(len(records), 16)
								if c.Call(ctx, "peers.publish", map[string]any{"records": records[:n]}, nil) != nil {
									break
								}
								records = records[n:]
							}
						}
					}
					cancel()
				}
			}
		}
	}
}

func runBridgeCommand(ctx context.Context, args []string, streams cliStreams) int {
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
		_, _ = fmt.Fprintln(streams.Stdout, "orb bridge run|start|stop|status|instances|peers|grants|groups|scopes [--profile personal]\norb bridge pair invite | pair join < invitation.json | pair approve <invitation-id> <peer-id>\norb bridge grant|revoke|scope|group|assign|takeover|publish < request.json\norb bridge remote <peer-id> <method> < params.json\norb bridge view <peer-id> <instance-id>\norb bridge trust <peer-id>\norb bridge connect-ssh <user@host> [--remote-profile personal] [--remote-orb orb]")
		return 0
	}
	if args[0] == "run" {
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := runBridgeService(ctx, profile); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		return 0
	}
	if args[0] == "start" {
		if err := startBridge(ctx, profile, true); err != nil {
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
		if _, err := bridgeSSHCommand(target, remoteOrb, remoteProfile); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if err := startBridge(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		client, err := bridgeAdmin(ctx, profile)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		defer func() { _ = client.Close() }()
		var status bridgeSettingsStatus
		if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		peer, err := connectBridgeSSH(ctx, client, status.PeerID, target, remoteProfile, remoteOrb)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, peer)
		return 0
	}
	if args[0] == "view" && len(args) == 3 {
		return runBridgeView(ctx, profile, args[1], args[2], streams)
	}
	if args[0] == "trust" || len(args) > 1 && args[0] == "pair" && args[1] == "invite" {
		if err := startBridge(ctx, profile, true); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	client, err := bridgeAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, errors.New("bridge unavailable; run orb bridge start"))
	}
	defer func() { _ = client.Close() }()
	method := args[0]
	params := connect.JSON(struct{}{})
	read := func() error {
		var e error
		params, e = io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
		if e != nil {
			return e
		}
		if len(params) > protocol.MaxFrame {
			return connect.Fail("resource_exhausted")
		}
		_, e = protocol.Canonical(params)
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
			params = connect.JSON(map[string]any{"grants": []bridge.Grant{fullBridgeGrant("")}})
		case "join":
			err = read()
		case "approve":
			if len(args) != 4 {
				err = errors.New("approve requires invitation ID and claimant PeerID")
			} else {
				params = connect.JSON(map[string]string{"invitation_id": args[2], "claimant": args[3]})
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
			err = trustBridgePeer(ctx, client, args[1])
			if err == nil {
				return 0
			}
		}
	case "block":
		if len(args) != 2 {
			err = errors.New("block requires PeerID")
		} else {
			params = connect.JSON(map[string]string{"peer_id": args[1]})
		}
	case "remote":
		if len(args) != 3 {
			err = errors.New("remote requires PeerID and method")
		} else {
			err = read()
			params = connect.JSON(map[string]any{"peer_id": args[1], "method": args[2], "params": params})
		}
	case "status", "instances", "peers", "grants", "groups", "scopes", "stop":
	default:
		err = errors.New("unknown bridge command")
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	var result json.RawMessage
	if err = client.Call(ctx, method, params, &result); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if method == "stop" {
		if err = waitBridgeStopped(ctx, client); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	_, _ = fmt.Fprintln(streams.Stdout, string(result))
	return 0
}

// OpenSSH is an explicit native host integration, like the system clipboard;
// SSH keys and host verification stay with the user's existing SSH configuration.
func bridgeSSHArgs(target, command string) ([]string, error) {
	if target == "" || len(target) > 255 || strings.HasPrefix(target, "-") || strings.Trim(target, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-@[]:") != "" {
		return nil, fmt.Errorf("enter an SSH alias or user@host, without command-line options")
	}
	return []string{"-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=10", "-oClearAllForwardings=yes", "-oForwardAgent=no", "--", target, command}, nil
}

func bridgeSSHCommand(target, remoteOrb, profile string, args ...string) ([]string, error) {
	if !validBridgeName(profile) || remoteOrb == "" || strings.ContainsAny(remoteOrb, "\r\n\x00") {
		return nil, fmt.Errorf("invalid remote Orb path or profile")
	}
	command := []string{remoteOrb, "bridge", "--profile", profile}
	command = append(command, args...)
	for i, arg := range command {
		command[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return bridgeSSHArgs(target, "exec "+strings.Join(command, " "))
}

func runBridgeSSH(ctx context.Context, target, remoteOrb, profile string, args ...string) ([]byte, error) {
	arguments, err := bridgeSSHCommand(target, remoteOrb, profile, args...)
	if err != nil {
		return nil, err
	}
	return runBridgeSSHExec(ctx, arguments, nil)
}

func runBridgeSSHExec(ctx context.Context, arguments []string, input io.Reader) ([]byte, error) {
	wait := 40 * time.Second
	if input != nil {
		wait = selfUpdateDownloadWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	command := exec.CommandContext(ctx, "ssh", arguments...)
	command.WaitDelay = time.Second
	command.Stdin = input
	command.Stderr = io.Discard
	pipe, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, fmt.Errorf("SSH unavailable: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(pipe, protocol.MaxFrame+1))
	if len(output) > protocol.MaxFrame {
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf("SSH response exceeded the Bridge limit")
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("SSH setup interrupted.\nCheck the connection and try again")
	}
	if readErr != nil || waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			switch exit.ExitCode() {
			case 255:
				return nil, fmt.Errorf("SSH login failed.\nCheck your SSH keys and saved host key")
			case 42:
				return nil, fmt.Errorf("this release does not support Bridge pairing.\nInstall a Bridge-enabled build on the server")
			case 43:
				return nil, fmt.Errorf("uploaded Orb failed its checksum.\nReconnect to retry the installation")
			}
		}
		return nil, fmt.Errorf("server setup failed.\nCheck its install directory and Bridge service")
	}
	return output, nil
}

func connectBridgeSSH(ctx context.Context, client *protocol.Conn, localPeer, target, remoteProfile, remoteOrb string) (string, error) {
	var err error
	remoteOrb, err = ensureBridgeSSH(ctx, target, remoteOrb, newSelfUpdater(version, false))
	if err != nil {
		return "", err
	}
	if _, err := runBridgeSSH(ctx, target, remoteOrb, remoteProfile, "start"); err != nil {
		return "", err
	}
	raw, err := runBridgeSSH(ctx, target, remoteOrb, remoteProfile, "pair", "invite")
	if err != nil {
		return "", err
	}
	inv, err := parseBridgeInvitation(string(raw))
	if err != nil {
		return "", err
	}
	var claimed bridge.Invitation
	if err = client.Call(ctx, "join", inv, &claimed); err != nil {
		return "", fmt.Errorf("SSH connected, but Bridge could not reach the server: %w", err)
	}
	if claimed.ID != inv.ID || claimed.PeerID != inv.PeerID || claimed.Claimant != localPeer {
		return "", fmt.Errorf("pairing identity changed")
	}
	if _, err = runBridgeSSH(ctx, target, remoteOrb, remoteProfile, "pair", "approve", inv.ID, localPeer); err != nil {
		return "", err
	}
	return inv.PeerID, trustBridgePeer(ctx, client, inv.PeerID)
}

func fullBridgeGrant(peer string) bridge.Grant {
	return bridge.Grant{Principal: connect.Principal{PeerID: peer, Subject: connect.Subject{Kind: "controller"}}, GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.cancel", "instance.session.manage"}}
}

func trustBridgePeer(ctx context.Context, client *protocol.Conn, peer string) error {
	g := fullBridgeGrant(peer)
	var status bridgeSettingsStatus
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return err
	}
	if !status.SupportsFullAccess {
		return fmt.Errorf("bridge needs to restart after an update.\nTurn it off and on, then reconnect")
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

func ensureBridgeSSH(ctx context.Context, target, remoteOrb string, updater selfUpdater) (string, error) {
	if remoteOrb != "orb" {
		help, err := runBridgeSSH(ctx, target, remoteOrb, "personal", "--help")
		if err != nil {
			return "", err
		}
		if !strings.Contains(string(help), "orb bridge trust") {
			return "", fmt.Errorf("the selected Orb does not support Bridge pairing.\nUpdate that executable or use automatic setup")
		}
		return remoteOrb, nil
	}
	args, err := bridgeSSHArgs(target, `for orb_path in "$(command -v orb || true)" "${ORB_INSTALL_DIR:-$HOME/.local/bin}/orb"; do
  if [ -x "$orb_path" ] && PI_OFFLINE=1 "$orb_path" bridge --help 2>/dev/null | grep -q 'orb bridge trust'; then
    printf 'ready\n%s\n' "$orb_path"; exit 0
  fi
done
printf 'install\n'; uname -sm`)
	if err != nil {
		return "", err
	}
	raw, err := runBridgeSSHExec(ctx, args, nil)
	if err != nil {
		return "", err
	}
	state, value, ok := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	if !ok {
		return "", fmt.Errorf("could not detect Orb on the server.\nCheck its SSH shell configuration")
	}
	if state == "ready" {
		return value, nil
	}
	platform := strings.Fields(value)
	if state != "install" || len(platform) != 2 {
		return "", fmt.Errorf("could not detect the server platform")
	}
	goos := strings.ToLower(platform[0])
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform[1]]
	updater.client = guardRedirects(updater.client)
	tag, err := fetchLatestReleaseVersion(ctx, updater.currentVersion, updater.client, updater.releaseURL, selfUpdateMetadataWait)
	if err != nil {
		return "", err
	}
	payload, err := updater.downloadTarget(ctx, tag, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("could not download Orb for the server.\n%w", err)
	}
	return installBridgeSSH(ctx, target, payload)
}

func installBridgeSSH(ctx context.Context, target string, payload []byte) (string, error) {
	args, err := bridgeSSHArgs(target, fmt.Sprintf("expected=%x\n", sha256.Sum256(payload))+`set -eu
umask 077
dir="${ORB_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir"
stage=$(mktemp "$dir/.orb-install.XXXXXXXX")
trap 'rm -f "$stage"' EXIT
cat > "$stage"
if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum "$stage"); else digest=$(shasum -a 256 "$stage"); fi
[ "${digest%% *}" = "$expected" ] || exit 43
chmod 755 "$stage"
PI_OFFLINE=1 "$stage" bridge --help 2>/dev/null | grep -q 'orb bridge trust' || exit 42
mv -f "$stage" "$dir/orb"
printf '%s\n' "$dir/orb"`)
	if err != nil {
		return "", err
	}
	raw, err := runBridgeSSHExec(ctx, args, bytes.NewReader(payload))
	return strings.TrimSpace(string(raw)), err
}
