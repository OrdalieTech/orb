// Package daemon is the native Bridge service: the process `orb bridge run`
// serves, reached over private IPC sockets in the profile directory, its peers
// over Tailcat and WebSocket, the machine-level calls it answers peers
// (host.*), and its installation as a systemd user service or on a server
// over SSH. Its documents live in the native database when there is one.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/platforms/native"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	transport "github.com/OrdalieTech/orb/platforms/native/tailcat"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

// Cache is the profile's previews of peers' conversations: in the native
// database, or in file mode in one beside the Bridge home, which done closes.
func Cache(ctx context.Context, state *native.State, profile string) (_ *sqlite.Foreign, done func(), _ error) {
	if state != nil {
		return state.DB.Foreign(profile), func() {}, nil
	}
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Dir(filepath.Dir(dir))
	if override := os.Getenv("ORB_BRIDGE_HOME"); override != "" {
		root = override
	}
	db, err := sqlite.Open(ctx, filepath.Join(root, "state", "orb.db"))
	if err != nil {
		return nil, nil, err
	}
	return db.Foreign(profile), func() { _ = db.Close() }, nil
}

func Admin(ctx context.Context, state *native.State, profile string) (*protocol.Conn, error) {
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return nil, err
	}
	token, err := state.Read(ctx, filepath.Join(dir, "admin.token"))
	if err != nil {
		return nil, err
	}
	return nativebridge.Dial(ctx, filepath.Join(dir, "admin.sock"), nativebridge.Auth{Credential: string(token)}, nil, nil)
}

func WaitStopped(ctx context.Context, client *protocol.Conn) error {
	select {
	case <-client.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return fmt.Errorf("bridge is still stopping; refresh its status before restarting")
	}
}

func Ready(ctx context.Context, client *protocol.Conn) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status struct {
		SupportsFullAccess bool `json:"supports_full_access"`
	}
	if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return false, err
	}
	if status.SupportsFullAccess {
		return true, nil
	}
	if err := client.Call(ctx, "stop", struct{}{}, nil); err != nil {
		return false, err
	}
	return false, WaitStopped(ctx, client)
}

func Start(ctx context.Context, state *native.State, profile string, explicit bool) error {
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return err
	}
	stopped := filepath.Join(dir, "stopped")
	if explicit {
		if err = state.Write(ctx, stopped, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// A running Bridge is used whatever the stop marker says: the marker keeps Orb from starting
	// one by itself, never from reaching one that runs (a systemd service, say).
	if c, err := Admin(ctx, state, profile); err == nil {
		ready, err := Ready(ctx, c)
		_ = c.Close()
		if err != nil || ready {
			return err
		}
		// The older daemon writes its deliberate-stop marker during replacement.
		if err = state.Write(ctx, stopped, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if _, err = state.Read(ctx, stopped); err == nil && !explicit {
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
			if c, err := Admin(ctx, state, profile); err == nil {
				_ = c.Close()
				return nil
			}
		}
	}
}

// Service is a running Bridge on this machine: its peers, and the
// machine-level calls it serves them (host.*). Its state is the native
// database, or nil for files.
type Service struct {
	*bridge.Peers
	state   *native.State
	profile string
	version string
	// loginExecutable is the orb a host sign-in runs; tests stand in a script.
	loginExecutable func() (string, error)
	mu              sync.Mutex
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

// New serves b's peers over Tailcat (node) and, for ws:// and wss:// locators,
// WebSocket; webURL, when set, is the address invitations give.
func New(ctx context.Context, state *native.State, profile, version string, b *bridge.Bridge, node *transport.Node, webURL string) *Service {
	peers := bridge.NewPeers(ctx, b)
	peers.WebURL, peers.Locator = webURL, node.Locator
	peers.Dial = func(ctx context.Context, id, locator string) (net.Conn, error) {
		if strings.HasPrefix(locator, "ws://") || strings.HasPrefix(locator, "wss://") {
			return webtransport.Dial(ctx, locator)
		}
		return node.Dial(ctx, id, locator)
	}
	peers.Forget = func(ctx context.Context, peer string) error {
		cache, done, err := Cache(ctx, state, profile)
		if err != nil {
			return err
		}
		defer done()
		return cache.Forget(ctx, peer)
	}
	return &Service{Peers: peers, state: state, profile: profile, version: version, loginExecutable: os.Executable, launched: map[string]*launched{}, logins: map[string]*hostLogin{}, terminals: map[string]*hostTerminal{}, ctx: ctx}
}

func (s *Service) outbound(ctx context.Context, _ string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		PeerID string      `json:"peer_id"`
		Call   bridge.Call `json:"call"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	subject, err := s.Bridge.Outbound(nativebridge.InstanceFromContext(ctx), p.PeerID, p.Call)
	if err != nil {
		return nil, err
	}
	return s.Remote(ctx, p.PeerID, "instances.call", struct {
		bridge.Call
		Subject bridge.Subject `json:"subject"`
	}{p.Call, subject})
}

type Web struct {
	Listen  string
	URL     string
	Origins []string
}

func Run(ctx context.Context, state *native.State, profile, version string, web Web) error {
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return err
	}
	store, err := state.BridgeStore(filepath.Join(dir, "state.json"), protocol.MaxFrame)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	tokenPath := filepath.Join(dir, "admin.token")
	token, tokenErr := state.Read(ctx, tokenPath)
	b, err := bridge.Open(store, errors.Is(tokenErr, os.ErrNotExist))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	if errors.Is(tokenErr, os.ErrNotExist) {
		var v [32]byte
		_, _ = rand.Read(v[:])
		token = []byte(base64.RawURLEncoding.EncodeToString(v[:]))
		tokenErr = state.Write(ctx, tokenPath, token)
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
	service := New(serviceCtx, state, profile, version, b, node, web.URL)
	b.SetHost(service.host)
	b.SetName(machineName())
	defer service.stopLaunched()
	reexec := ""
	service.restart = func(path string) { reexec = path; time.AfterFunc(300*time.Millisecond, stop) }
	// Windows cannot exec: there an update restarts Bridge through its service manager.
	if exe, err := os.Executable(); err == nil && runtime.GOOS != "windows" {
		go service.follow(exe, time.Minute)
	}
	admin := func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "stop" {
			var p struct{}
			if err := protocol.Decode(params, &p); err != nil {
				return nil, err
			}
			if err := state.Write(ctx, filepath.Join(dir, "stopped"), []byte("stopped\n")); err != nil {
				return nil, err
			}
			time.AfterFunc(100*time.Millisecond, stop)
			return bridge.JSON(struct{}{}), nil
		}
		return service.Admin(ctx, method, params)
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
					bridge.Keepalive(serviceCtx, peer, 4*time.Second)
					_ = peer.Close()
				}(stream)
			default:
				_ = stream.Close()
			}
		}
	}()
	go service.Reconcile()
	go service.Greet()
	<-serviceCtx.Done()
	if reexec != "" {
		return RestartInto(reexec)
	}
	return nil
}

// RestartInto asks the process that ran the service to become the binary at this path, once
// every listener and store is closed: an update keeps the process, so a supervisor keeps it too.
type RestartInto string

func (r RestartInto) Error() string { return "restarting into " + string(r) }
