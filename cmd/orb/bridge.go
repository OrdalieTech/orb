package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
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
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/platforms/native"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
	transport "github.com/OrdalieTech/orb/platforms/native/tailcat"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

func bridgeAdmin(ctx context.Context, profile string) (*protocol.Conn, error) {
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return nil, err
	}
	token, err := stateFromContext(ctx).native().Read(ctx, filepath.Join(dir, "admin.token"))
	if err != nil {
		return nil, err
	}
	return nativebridge.Dial(ctx, filepath.Join(dir, "admin.sock"), nativebridge.Auth{Credential: string(token)}, nil, nil)
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
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return err
	}
	stopped := filepath.Join(dir, "stopped")
	if explicit {
		if err = stateFromContext(ctx).native().Write(ctx, stopped, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// A running Bridge is used whatever the stop marker says: the marker keeps Orb from starting
	// one by itself, never from reaching one that runs (a systemd service, say).
	if c, err := bridgeAdmin(ctx, profile); err == nil {
		ready, err := bridgeServiceReady(ctx, c)
		_ = c.Close()
		if err != nil || ready {
			return err
		}
		// The older daemon writes its deliberate-stop marker during replacement.
		if err = stateFromContext(ctx).native().Write(ctx, stopped, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if _, err = stateFromContext(ctx).native().Read(ctx, stopped); err == nil && !explicit {
		return errors.New("bridge stopped; start it explicitly")
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
	cmd.SysProcAttr = native.DetachedProcAttr()
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		return err
	}
	// Reaped when it exits: a service that loses the race to an already-running one exits at
	// once, and a long-lived parent (a TUI, an app's core) would otherwise keep it as a zombie.
	go func() { _ = cmd.Wait() }()
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
	webURL  string
	b       *bridge.Bridge
	node    *transport.Node
	mu      sync.Mutex
	peers   map[string]*protocol.Conn
	// joining: inviters this Bridge claimed an invitation from, kept as known peers only once
	// trusted, so one that never approves is not dialled at every start.
	joining map[string]string
	// launched: Orbs this Bridge started in a folder for a peer (host.launch).
	launched map[string]*launched
	// logins: sign-ins this machine runs for a peer (host.login.*).
	logins map[string]*hostLogin
	// terminals: shells this machine runs for a peer (host.terminal.*).
	terminals map[string]*hostTerminal
	// restart ends the service so its process can exec the binary at path (host.update).
	restart func(path string)
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
		s.mu.Lock()
		locator = s.joining[id]
		s.mu.Unlock()
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
	// Dial, unless the peer gets here first: one back from a network cut dials in by itself,
	// often long before a dial toward it would get through.
	dialed := make(chan error, 1)
	go func() { dialed <- s.dial(timeout, id, locator) }()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-dialed:
			if err != nil {
				return nil, err
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.peers[id], nil
		case <-tick.C:
			if c := s.b.Connection(id); c != nil {
				return c, nil
			}
		case <-timeout.Done():
			return nil, bridge.Fail("unavailable")
		}
	}
}

func (s *bridgeService) dial(ctx context.Context, id, locator string) error {
	var stream net.Conn
	var err error
	if strings.HasPrefix(locator, "ws://") || strings.HasPrefix(locator, "wss://") {
		stream, err = webtransport.Dial(ctx, locator)
	} else {
		stream, err = s.node.Dial(ctx, id, locator)
	}
	if err != nil {
		return err
	}
	c, _, err := s.b.Connect(ctx, stream, id, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.peers[id]
	s.peers[id] = c
	s.mu.Unlock()
	go keepalive(s.ctx, c, 4*time.Second)
	if old != nil {
		_ = old.Close()
	}
	return nil
}
func (s *bridgeService) remote(ctx context.Context, id, method string, p any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// The owner reaches its own machine with the same calls, served here: one client for every Orb.
	if id == s.b.PeerID() {
		params, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		return s.b.Handle(ctx, id, method, params)
	}
	// A dead connection fails, or is superseded by the channel a restarted peer opens to greet
	// this one; one retry reaches the peer's fresh channel or redials. Only calls safe to repeat
	// are retried: reads, and instances.call, which the peer deduplicates by operation id.
	retries := 1
	if method == "host.launch" || method == "host.terminal.open" || method == "host.terminal.write" {
		retries = 0
	}
	for attempt := 0; ; attempt++ {
		c, err := s.peer(ctx, id, "")
		if err != nil {
			return nil, err
		}
		result, err := s.call(ctx, id, c, method, p)
		var rpc *protocol.RPCError
		if err == nil || errors.As(err, &rpc) {
			return result, err
		}
		_ = c.Close()
		if ctx.Err() != nil || attempt == retries {
			return nil, err
		}
	}
}

var errSuperseded = errors.New("superseded by a newer channel")

// call waits for one call on c, giving up early once a newer channel from the same peer arrives.
func (s *bridgeService) call(ctx context.Context, id string, c *protocol.Conn, method string, p any) (json.RawMessage, error) {
	done := make(chan error, 1)
	var result json.RawMessage
	go func() { done <- c.Call(ctx, method, p, &result) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return result, err
		case <-tick.C:
			if n := s.b.Connection(id); n != nil && n != c {
				return nil, errSuperseded
			}
		}
	}
}

// keepalive pings a peer connection until the connection or the service ends, and closes it when
// the peer stops answering. A peer that restarted or lost its network leaves a half-open connection
// behind; without this, the next call on it would wait out its timeout before anything redialled.
func keepalive(ctx context.Context, c *protocol.Conn, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
			return
		case <-t.C:
		}
		ping, cancel := context.WithTimeout(ctx, every)
		err := c.Call(ping, "bridge.ping", struct{}{}, nil)
		cancel()
		var rpc *protocol.RPCError
		if err != nil && !errors.As(err, &rpc) && ctx.Err() == nil {
			_ = c.Close()
			return
		}
	}
}
func (s *bridgeService) outbound(ctx context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		PeerID string      `json:"peer_id"`
		Call   bridge.Call `json:"call"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	subject, err := s.b.Outbound(nativebridge.InstanceFromContext(ctx), p.PeerID, p.Call)
	if err != nil {
		return nil, err
	}
	return s.remote(ctx, p.PeerID, "instances.call", struct {
		bridge.Call
		Subject bridge.Subject `json:"subject"`
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
		db, err := openBridgeCache(s.ctx, s.profile)
		if err != nil {
			return nil, err
		}
		defer func() { _ = db.Close() }()
		return raw, db.Foreign(s.profile).Forget(s.ctx, p.PeerID)
	case "invite":
		raw, err := s.b.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invite bridge.Invitation
		_ = json.Unmarshal(raw, &invite)
		if s.webURL != "" {
			invite.Locator = s.webURL
		} else {
			invite.Locator, err = s.node.Locator()
		}
		return bridge.JSON(invite), err
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
		s.mu.Lock()
		s.joining[inv.PeerID] = inv.Locator
		s.mu.Unlock()
		return bridge.JSON(result), nil
	case "grant":
		raw, err := s.b.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var g bridge.Grant
		_ = json.Unmarshal(params, &g)
		s.mu.Lock()
		locator, joined := s.joining[g.Principal.PeerID]
		delete(s.joining, g.Principal.PeerID)
		s.mu.Unlock()
		if joined {
			return raw, s.b.SavePeer(g.Principal.PeerID, locator)
		}
		return raw, nil
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
		return bridge.JSON(r), err
	default:
		return s.b.Admin(ctx, method, params)
	}
}

type bridgeWebOptions struct {
	Listen  string
	URL     string
	Origins []string
}

func runBridgeService(ctx context.Context, profile string, web bridgeWebOptions) error {
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return err
	}
	store, err := stateFromContext(ctx).native().BridgeStore(filepath.Join(dir, "state.json"), protocol.MaxFrame)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	tokenPath := filepath.Join(dir, "admin.token")
	token, tokenErr := stateFromContext(ctx).native().Read(ctx, tokenPath)
	b, err := bridge.Open(store, errors.Is(tokenErr, os.ErrNotExist))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	if errors.Is(tokenErr, os.ErrNotExist) {
		var v [32]byte
		_, _ = rand.Read(v[:])
		token = []byte(base64.RawURLEncoding.EncodeToString(v[:]))
		tokenErr = stateFromContext(ctx).native().Write(ctx, tokenPath, token)
	}
	if tokenErr != nil {
		return tokenErr
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
	if web.Listen != "" {
		handler, err := webtransport.Handler(serviceCtx, b, web.Origins)
		if err != nil {
			return err
		}
		listener, err := net.Listen("tcp", web.Listen)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		defer func() { _ = server.Close() }()
		go func() { _ = server.Serve(listener); stop() }()
	}
	service := &bridgeService{profile: profile, webURL: web.URL, b: b, node: node, peers: map[string]*protocol.Conn{}, joining: map[string]string{}, launched: map[string]*launched{}, logins: map[string]*hostLogin{}, terminals: map[string]*hostTerminal{}, ctx: serviceCtx}
	b.SetHost(service.host)
	defer service.stopLaunched()
	reexec := ""
	service.restart = func(path string) { reexec = path; time.AfterFunc(300*time.Millisecond, stop) }
	admin := func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "stop" {
			var p struct{}
			if err := protocol.Decode(params, &p); err != nil {
				return nil, err
			}
			if err := stateFromContext(serviceCtx).native().Write(ctx, filepath.Join(dir, "stopped"), []byte("stopped\n")); err != nil {
				return nil, err
			}
			time.AfterFunc(100*time.Millisecond, stop)
			return bridge.JSON(struct{}{}), nil
		}
		return service.admin(ctx, method, params)
	}
	closeAdmin, err := nativebridge.Listen(serviceCtx, filepath.Join(dir, "admin.sock"), b, string(token), admin, nil)
	if err != nil {
		return err
	}
	defer closeAdmin()
	closeAttach, err := nativebridge.Listen(serviceCtx, filepath.Join(dir, "attach.sock"), b, "", nil, service.outbound)
	if err != nil {
		return err
	}
	defer closeAttach()
	// Anyone holding an old invitation can reach this listener, so peers it does not know yet
	// (a device pairing) get a small pool of their own, and only for an invitation's lifetime:
	// throwaway identities can fill that pool, never the slots of paired peers.
	slots, strangers := make(chan struct{}, 64), make(chan struct{}, 8)
	go func() {
		for {
			stream, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func(c net.Conn) {
					slot := slots
					defer func() { <-slot }()
					peer, id, err := b.Connect(serviceCtx, c, "", true)
					if err != nil {
						return
					}
					if !b.Known(id) {
						select {
						case strangers <- struct{}{}:
							<-slots
							slot = strangers
						default:
							_ = peer.Close()
							return
						}
						// Still unknown when its invitation would have expired, it is dropped;
						// paired by then, it stays like any known peer.
						drop := time.AfterFunc(10*time.Minute, func() {
							if !b.Known(id) {
								_ = peer.Close()
							}
						})
						defer drop.Stop()
					}
					// A peer that dialled here can vanish without a word (its network changed):
					// pinging finds out in seconds, where a call would wait out its timeout.
					keepalive(serviceCtx, peer, 4*time.Second)
					_ = peer.Close()
				}(stream)
			default:
				_ = stream.Close()
			}
		}
	}()
	go service.reconcile()
	go service.greet()
	<-serviceCtx.Done()
	if reexec != "" {
		return restartInto(reexec)
	}
	return nil
}

// restartInto asks the process that ran the service to become the binary at this path, once
// every listener and store is closed: an update keeps the process, so a supervisor keeps it too.
type restartInto string

func (r restartInto) Error() string { return "restarting into " + string(r) }

// greet runs at startup: it clears what the previous run left, then dials every known peer, and
// keeps dialling those without a channel.
// Their connections to that run are half-open; a fresh channel from this side becomes their
// newest, so their next call lands at once instead of waiting out a timeout on the dead one.
// retireAfter is how long a restarted Bridge waits before retiring throwaway registrations no Orb
// came back to: attached Orbs retry every two seconds at most.
const retireAfter = 2 * time.Minute

func (s *bridgeService) greet() {
	raw, err := s.b.Admin(s.ctx, "status", bridge.JSON(struct{}{}))
	if err != nil {
		return
	}
	var status struct {
		Peers     []string          `json:"peers"`
		States    map[string]string `json:"peer_states"`
		Instances []bridge.Instance `json:"instances"`
	}
	_ = json.Unmarshal(raw, &status)
	// Orbs a previous run started for peers ended with it (a crash left no one to retire them).
	stale := []string{}
	for _, i := range status.Instances {
		if strings.HasPrefix(i.Alias, "launch-") && !i.Available {
			stale = append(stale, i.ID)
		}
	}
	if len(stale) > 0 {
		_, _ = s.b.Admin(s.ctx, "retire", bridge.JSON(map[string][]string{"instance_ids": stale}))
	}
	// Throwaway registrations retire when their Orb exits cleanly; a crashed or killed one never
	// does. Once the Orbs still running have had time to attach again, the rest are gone.
	time.AfterFunc(retireAfter, func() {
		if s.ctx.Err() == nil {
			_, _ = s.b.Admin(s.ctx, "retire", bridge.JSON(struct{}{}))
		}
	})
	// A peer that restarted behind a NAT cannot always dial back (a phone after an app update):
	// every known peer without a channel is dialled again until one opens.
	for {
		for _, peer := range status.Peers {
			if status.States[peer] == "blocked" || peer == s.b.PeerID() || s.b.Connection(peer) != nil {
				continue
			}
			go func() {
				ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
				defer cancel()
				_, _ = s.peer(ctx, peer, "")
			}()
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		raw, err := s.b.Admin(s.ctx, "status", bridge.JSON(struct{}{}))
		if err != nil {
			return
		}
		_ = json.Unmarshal(raw, &status)
	}
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
		raw, err := s.b.Admin(s.ctx, "status", bridge.JSON(struct{}{}))
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
		_, _ = fmt.Fprintln(streams.Stdout, "orb bridge run|start|stop|status|instances|peers|grants|groups|scopes|prune [--profile personal]\norb bridge run --web-listen 127.0.0.1:8789 --web-origin http://127.0.0.1:8787 [--web-url wss://host/bridge]\norb bridge pair   (show a QR code, then approve the device that scans it)\norb bridge join <code>   (pair with an Orb that ran orb bridge pair)\norb bridge service install|remove   (Linux: keep Bridge running across logouts and reboots)\norb bridge pair invite | pair join < invitation.json | pair approve <invitation-id> <peer-id>\norb bridge grant|revoke|scope|group|assign|takeover|publish < request.json\norb bridge remote <peer-id> <method> < params.json\norb bridge view <peer-id> <instance-id>\norb bridge shell <peer-id> [folder]   (a terminal on a machine that lets this one start Orb there)\norb bridge pipe   (owner API as JSON lines on stdin/stdout)\norb bridge trust <peer-id>\norb bridge connect-ssh <user@host> [--remote-profile personal] [--remote-orb orb]")
		return 0
	}
	if args[0] == "run" {
		var web bridgeWebOptions
		flags := flag.NewFlagSet("orb bridge run", flag.ContinueOnError)
		flags.SetOutput(streams.Stderr)
		flags.StringVar(&web.Listen, "web-listen", "", "optional WebSocket bind address")
		flags.StringVar(&web.URL, "web-url", "", "public wss:// URL included in invitations")
		flags.Func("web-origin", "allowed browser origin (repeatable)", func(value string) error { web.Origins = append(web.Origins, value); return nil })
		if err := flags.Parse(args[1:]); err != nil {
			return 1
		}
		if flags.NArg() != 0 {
			return reportCLIError(streams.Stderr, errors.New("unexpected bridge run arguments"))
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
		err := runBridgeService(ctx, profile, web)
		var next restartInto
		if errors.As(err, &next) {
			err = native.Exec(string(next), os.Args, os.Environ())
		}
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		return 0
	}
	if args[0] == "service" {
		return runBridgeServiceCommand(ctx, profile, args[1:], streams)
	}
	if args[0] == "start" {
		if bridgeServiceInstalled(profile) {
			if err := systemctlUser(ctx, "start", bridgeUnitName(profile)); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			return 0
		}
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
	if args[0] == "shell" && (len(args) == 2 || len(args) == 3) {
		return runBridgeShell(ctx, profile, args[1], strings.Join(args[2:], ""), streams)
	}
	if args[0] == "pair" && len(args) == 1 {
		return runBridgePair(ctx, profile, streams)
	}
	if args[0] == "join" {
		return runBridgeJoin(ctx, profile, args[1:], streams)
	}
	if args[0] == "pipe" {
		return runBridgePipe(ctx, profile, streams)
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
	params := bridge.JSON(struct{}{})
	read := func() error {
		var e error
		params, e = io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
		if e != nil {
			return e
		}
		if len(params) > protocol.MaxFrame {
			return bridge.Fail("resource_exhausted")
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
			params = bridge.JSON(map[string]any{"grants": []bridge.Grant{fullBridgeGrant("")}})
		case "join":
			err = read()
		case "approve":
			if len(args) != 4 {
				err = errors.New("approve requires invitation ID and claimant PeerID")
			} else {
				params = bridge.JSON(map[string]string{"invitation_id": args[2], "claimant": args[3]})
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
			err = trustBridgePeer(ctx, client, args[1], true)
			if err == nil {
				return 0
			}
		}
	case "block":
		if len(args) != 2 {
			err = errors.New("block requires PeerID")
		} else {
			params = bridge.JSON(map[string]string{"peer_id": args[1]})
		}
	case "remote":
		if len(args) != 3 {
			err = errors.New("remote requires PeerID and method")
		} else {
			err = read()
			params = bridge.JSON(map[string]any{"peer_id": args[1], "method": args[2], "params": params})
		}
	case "prune":
		method = "retire"
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

// runBridgePipe serves the owner admin API as JSON lines for GUI shells, over
// one long-lived connection instead of a process per call:
// {"id":1,"method":"status"} -> {"id":1,"result":…} or {"id":1,"error":{"code":…,"message":…}}.
func runBridgePipe(ctx context.Context, profile string, streams cliStreams) int {
	if err := startBridge(ctx, profile, true); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	var mu sync.Mutex // guards client and output
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
		c, err := bridgeAdmin(ctx, profile)
		if err != nil {
			// A restarted service keeps its state; bring it back once before failing the call.
			if err = startBridge(ctx, profile, false); err == nil {
				c, err = bridgeAdmin(ctx, profile)
			}
		}
		client = c
		return c, err
	}
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
			c, err := connect()
			if err == nil {
				err = c.Call(ctx, req.Method, req.Params, &result)
			}
			if err != nil {
				reply["error"] = map[string]string{"code": bridge.Code(err), "message": err.Error()}
			} else {
				reply["result"] = result
			}
			mu.Lock()
			_ = out.Encode(reply)
			mu.Unlock()
		}()
	}
	calls.Wait()
	if client != nil {
		_ = client.Close()
	}
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
	if !nativebridge.ValidName(profile) || remoteOrb == "" || strings.ContainsAny(remoteOrb, "\r\n\x00") {
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
		wait = selfupdate.DownloadWait
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
	remoteOrb, err = ensureBridgeSSH(ctx, target, remoteOrb, selfupdate.New(version, false))
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
	return inv.PeerID, trustBridgePeer(ctx, client, inv.PeerID, false)
}

// fullBridgeGrant is what an invitation offers: control of every conversation, current and
// future, and of the machine itself (host.launch: its threads, starting Orb, updating it).
func fullBridgeGrant(peer string) bridge.Grant {
	g := conversationBridgeGrant(peer)
	g.Permissions = append(g.Permissions, "host.launch")
	return g
}

// conversationBridgeGrant is control of every conversation without the machine: what a joining
// Orb gives back to the Orb it joined, which never asked for more.
func conversationBridgeGrant(peer string) bridge.Grant {
	return bridge.Grant{Principal: bridge.Principal{PeerID: peer, Subject: bridge.Subject{Kind: "controller"}}, GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.input.reply", "instance.cancel", "instance.session.manage"}}
}

// trustBridgePeer grants peer control of this Orb's conversations, and of the machine too when
// machine is set (`orb bridge trust`, the owner's explicit choice).
func trustBridgePeer(ctx context.Context, client *protocol.Conn, peer string, machine bool) error {
	shareOrbsOnBridge(ctx)
	g := conversationBridgeGrant(peer)
	if machine {
		g = fullBridgeGrant(peer)
	}
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

func ensureBridgeSSH(ctx context.Context, target, remoteOrb string, updater selfupdate.Updater) (string, error) {
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
	tag, err := updater.Latest(ctx)
	if err != nil {
		return "", err
	}
	payload, err := updater.Download(ctx, tag, goos, goarch)
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
