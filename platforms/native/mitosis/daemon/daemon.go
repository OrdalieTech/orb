// Package daemon is the native Mitosis service: the process `orb mitosis run`
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
	"github.com/OrdalieTech/orb/agent/config"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/internal/ctxsleep"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/native"
	nativemitosis "github.com/OrdalieTech/orb/platforms/native/mitosis"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	transport "github.com/OrdalieTech/orb/platforms/native/tailcat"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

// Cache is the profile's previews of peers' conversations: in the native
// database, or in file mode in one beside the Mitosis home, which done closes.
func Cache(ctx context.Context, state *native.State, profile string) (_ *sqlite.Foreign, done func(), _ error) {
	if state != nil {
		return state.DB.Foreign(profile), func() {}, nil
	}
	dir, err := nativemitosis.Dir(profile)
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Dir(filepath.Dir(dir))
	if override := os.Getenv("ORB_MITOSIS_HOME"); override != "" {
		root = override
	}
	db, err := sqlite.Open(ctx, filepath.Join(root, "state", "orb.db"))
	if err != nil {
		return nil, nil, err
	}
	return db.Foreign(profile), func() { _ = db.Close() }, nil
}

func Admin(ctx context.Context, state *native.State, profile string) (*protocol.Conn, error) {
	dir, err := nativemitosis.Dir(profile)
	if err != nil {
		return nil, err
	}
	token, err := state.Read(ctx, filepath.Join(dir, "admin.token"))
	if err != nil {
		return nil, err
	}
	return nativemitosis.Dial(ctx, filepath.Join(dir, "admin.sock"), nativemitosis.Auth{Credential: string(token)}, nil, nil)
}

func WaitStopped(ctx context.Context, client *protocol.Conn) error {
	select {
	case <-client.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return fmt.Errorf("mitosis is still stopping; refresh its status before restarting")
	}
}

// runArgs runs the service on this process's storage: Pi files when it has no native state.
func runArgs(state *native.State, profile string) []string {
	args := []string{"mitosis", "run", "--profile", profile}
	if state == nil {
		return append([]string{"--pi-files"}, args...)
	}
	return args
}

func Start(ctx context.Context, state *native.State, profile string, explicit bool) error {
	dir, err := nativemitosis.Dir(profile)
	if err != nil {
		return err
	}
	stopped := filepath.Join(dir, "stopped")
	if explicit {
		if err = state.Write(ctx, stopped, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// A running Mitosis is used whatever the stop marker says: the marker keeps Orb from starting
	// one by itself, never from reaching one that runs (a systemd service, say).
	if c, err := Admin(ctx, state, profile); err == nil {
		return c.Close()
	} else if _, err = state.Read(ctx, stopped); err == nil && !explicit {
		return errors.New("mitosis stopped; start it explicitly")
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
	cmd := exec.Command(exe, runArgs(state, profile)...)
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
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("mitosis startup failed; see private service.log")
		case <-ticker.C:
			if c, err := Admin(ctx, state, profile); err == nil {
				_ = c.Close()
				return nil
			}
		}
	}
}

// Service is a running Mitosis on this machine: its peers, and the
// machine-level calls it serves them (host.*). Its state is the native
// database, or nil for files.
type Service struct {
	*mitosis.Peers
	state   *native.State
	profile string
	version string
	// loginExecutable is the orb a host sign-in runs; tests stand in a script.
	loginExecutable func() (string, error)
	mu              sync.Mutex
	// launched: Orbs this Mitosis started in a folder for a peer (host.launch).
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
func New(ctx context.Context, state *native.State, profile, version string, b *mitosis.Mitosis, node *transport.Node, webURL string) *Service {
	peers := mitosis.NewPeers(ctx, b)
	peers.WebURL = webURL
	// The relay comes up behind the local sockets: an invitation or a join asked
	// right as Mitosis starts waits a while for the address it gives.
	peers.Locator = func() (string, error) {
		for deadline := time.Now().Add(25 * time.Second); ; {
			l, err := node.Locator()
			if err == nil || time.Now().After(deadline) {
				return l, err
			}
			if ctxErr := ctxsleep.Sleep(ctx, 50*time.Millisecond); ctxErr != nil {
				return "", ctxErr
			}
		}
	}
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

type Web struct {
	Listen  string
	URL     string
	Origins []string
}

func Run(ctx context.Context, state *native.State, profile, version string, web Web) error {
	dir, err := nativemitosis.Dir(profile)
	if err != nil {
		return err
	}
	store, err := state.MitosisStore(filepath.Join(dir, "state.json"), protocol.MaxFrame)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	tokenPath := filepath.Join(dir, "admin.token")
	token, tokenErr := state.Read(ctx, tokenPath)
	b, err := mitosis.Open(store, errors.Is(tokenErr, os.ErrNotExist))
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
		return errors.New("corrupt mitosis admin credential")
	}
	node, err := transport.Open(b.TransportState(), b.SaveTransportState, transport.MapURL)
	if err != nil {
		return err
	}
	defer func() { _ = node.Close() }()
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
	// service.log (or the journal) says what runs; nothing about peers or what they share.
	_, _ = fmt.Fprintf(os.Stderr, "%s mitosis %s, profile %s, running as %s\n", time.Now().Format(time.RFC3339), version, profile, b.PeerID())
	defer func() { _, _ = fmt.Fprintf(os.Stderr, "%s mitosis stopped\n", time.Now().Format(time.RFC3339)) }()
	b.SetHost(service.host)
	b.SetName(machineName())
	agentDir, _ := config.GetAgentDir()
	if state != nil {
		agentDir = state.AgentDir
	}
	if credentials, err := state.Auth(agentDir); err == nil && agentDir != "" {
		b.SetClass("credentials", credentials)
	}
	// The memory the agent's memory plugin keeps: native storage's, else the agent directory's journal.
	if memories := state.Memory(); memories != nil {
		b.SetClass("memory", memory.Shared{Store: memories})
	} else if agentDir != "" {
		if journal, err := filestore.NewFileStore(filepath.Join(agentDir, "memory")); err == nil {
			b.SetClass("memory", memory.Shared{Store: journal})
		}
	}
	defer service.stopLaunched()
	reexec := ""
	service.restart = func(path string) { reexec = path; time.AfterFunc(300*time.Millisecond, stop) }
	// Windows cannot exec: there an update restarts Mitosis through its service manager.
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
			return mitosis.JSON(struct{}{}), nil
		}
		return service.Admin(ctx, method, params)
	}
	closeAdmin, err := nativemitosis.Listen(serviceCtx, filepath.Join(dir, "admin.sock"), b, string(token), admin)
	if err != nil {
		return err
	}
	defer closeAdmin()
	closeAttach, err := nativemitosis.Listen(serviceCtx, filepath.Join(dir, "attach.sock"), b, "", nil)
	if err != nil {
		return err
	}
	defer closeAttach()
	// Anyone holding an old invitation can reach this listener, so peers it does not know yet
	// (a device pairing) also hold a place in a small pool of their own while they are unknown,
	// and only for an invitation's lifetime: throwaway identities can fill that pool, never the
	// slots of paired peers, and a device once paired gives its place back to the next one.
	slots, strangers := make(chan struct{}, 128), make(chan struct{}, 8)
	paired := func(peer *protocol.Conn, id string) bool {
		select {
		case strangers <- struct{}{}:
		default:
			return false
		}
		defer func() { <-strangers }()
		expired := time.After(10 * time.Minute)
		for !b.Known(id) {
			select {
			case <-peer.Done():
				return false
			case <-expired:
				return false
			case <-time.After(time.Second):
			}
		}
		return true
	}
	// The relay comes up behind the local sockets: this machine's apps and Orbs wait on no network
	// to start (its map and region take a round trip or more), and keep working while it is
	// unreachable, offline say, as it is tried again every 30 s.
	go func() {
		var listener net.Listener
		for {
			startup, cancel := context.WithTimeout(serviceCtx, 25*time.Second)
			var err error
			listener, err = node.Listen(startup)
			cancel()
			if err == nil {
				break
			}
			select {
			case <-serviceCtx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
		context.AfterFunc(serviceCtx, func() { _ = listener.Close() })
		for {
			stream, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func(c net.Conn) {
					defer func() { <-slots }()
					peer, id, err := b.Connect(serviceCtx, c, "", true)
					if err != nil {
						return
					}
					if !b.Known(id) {
						go func() {
							if !paired(peer, id) {
								_ = peer.Close()
							}
						}()
					}
					// A peer that dialled here can vanish without a word (its network changed):
					// pinging finds out in seconds, where a call would wait out its timeout.
					mitosis.Keepalive(serviceCtx, peer, 4*time.Second)
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
