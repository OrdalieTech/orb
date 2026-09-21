package main

import (
	"context"
	"crypto/rand"
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
		_ = c.Close()
		return nil
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
	b     *bridge.Bridge
	node  *transport.Node
	mu    sync.Mutex
	peers map[string]*protocol.Conn
	ctx   context.Context
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
	service := &bridgeService{b: b, node: node, peers: map[string]*protocol.Conn{}, ctx: serviceCtx}
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
		_, _ = fmt.Fprintln(streams.Stdout, "orb bridge run|start|stop|status|instances|peers|grants|groups|scopes [--profile personal]\norb bridge pair invite [--include-future] | pair join < invitation.json | pair approve <invitation-id> <peer-id>\norb bridge grant|revoke|scope|group|assign|takeover|publish < request.json\norb bridge remote <peer-id> <method> < params.json\norb bridge view <peer-id> <instance-id>")
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
	if args[0] == "view" && len(args) == 3 {
		return runBridgeView(ctx, profile, args[1], args[2], streams)
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
			var status struct {
				Groups map[string]string `json:"groups"`
			}
			if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			group := ""
			for id, name := range status.Groups {
				if name == "personal" {
					group = id
				}
			}
			future := len(args) == 3 && args[2] == "--include-future"
			params = connect.JSON(map[string]any{"grants": []bridge.Grant{{GroupID: group, IncludeFuture: future, Permissions: []string{"instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.cancel", "instance.session.manage"}}}})
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
	_, _ = fmt.Fprintln(streams.Stdout, string(result))
	return 0
}
