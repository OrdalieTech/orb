package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	"github.com/creack/pty"
)

// maxLaunched bounds the Orbs one Bridge starts for its peers; each is a full agent process.
// One that shows no activity for launchedIdle ends; its thread reopens with the next message.
const (
	maxLaunched  = 8
	launchedIdle = 30 * time.Minute
	launchWait   = time.Minute // for a started Orb to come up on Bridge
)

// launched is an Orb this Bridge started for a peer. It lives while its stdin stays open: until
// its peer's thread ends with the Bridge, or the process exits by itself.
type launched struct {
	input   io.Closer
	session string
	alias   string
	id      string      // its instance, once on Bridge
	turn    atomic.Bool // a turn is running
}

// host serves a peer's machine-level calls. host.sessions pages the threads stored on this
// machine across folders, newest created first; host.launch starts Orb in a folder, on a new thread or resuming one,
// and answers once that Orb is on Bridge.
func (s *bridgeService) host(ctx context.Context, p bridge.Principal, method string, params json.RawMessage) (json.RawMessage, error) {
	state := stateFromContext(s.ctx)
	if state == nil {
		return nil, bridge.Fail("unavailable")
	}
	switch method {
	case "host.sessions":
		var q struct {
			Cursor string `json:"cursor,omitempty"`
		}
		if err := protocol.Decode(params, &q); err != nil {
			return nil, err
		}
		page, err := state.sessions().Catalog(ctx, sqlite.CatalogQuery{Cursor: q.Cursor, Limit: min(max(protocol.PageLimit(ctx), 1), 128)})
		if err != nil {
			return nil, bridge.Fail("invalid_params")
		}
		type item struct {
			ID       string `json:"session_id"`
			Name     string `json:"name,omitempty"`
			CWD      string `json:"cwd"`
			Modified int64  `json:"modified"`
			Messages int    `json:"messages"`
			First    string `json:"first"`
		}
		host, _ := os.Hostname()
		info, ok := debug.ReadBuildInfo()
		out := struct {
			Host    string `json:"host,omitempty"` // what the machine calls itself: peers have no names on the wire
			Version string `json:"version"`        // its Orb, so a peer can offer host.update
			Items   []item `json:"items"`
			Cursor  string `json:"cursor,omitempty"`
		}{strings.Split(host, ".")[0], selfupdate.Plain(selfupdate.BuildVersion(version, info, ok)), []item{}, page.Next}
		for _, e := range page.Sessions {
			if e.MessageCount == 0 {
				continue // a thread nobody wrote in is not worth reopening
			}
			modified, _ := time.Parse(time.RFC3339Nano, e.Modified)
			first := []rune(e.Preview)
			out.Items = append(out.Items, item{e.ID, e.Name, e.CWD, modified.UnixMilli(), e.MessageCount, string(first[:min(len(first), 160)])})
		}
		return bridge.JSON(out), nil
	case "host.launch":
		var q struct {
			CWD       string `json:"cwd,omitempty"`
			SessionID string `json:"session_id,omitempty"`
		}
		if err := protocol.Decode(params, &q); err != nil {
			return nil, err
		}
		if q.SessionID != "" {
			rows, err := state.sessions().ListInfo(ctx, "", nil)
			if err != nil {
				return nil, err
			}
			i := slices.IndexFunc(rows, func(r session.SessionInfo) bool { return r.ID == q.SessionID })
			if i < 0 {
				return nil, bridge.Fail("not_found")
			}
			q.CWD = rows[i].CWD
		}
		cwd, err := launchFolder(q.CWD)
		if err != nil {
			return nil, err
		}
		return s.launch(ctx, p, cwd, q.SessionID)
	case "host.update":
		var q struct{}
		if err := protocol.Decode(params, &q); err != nil {
			return nil, err
		}
		return s.update(ctx), nil
	case "host.providers", "host.login.start", "host.login.poll", "host.login.answer", "host.login.cancel":
		return s.login(ctx, method, params)
	case "host.terminal.open", "host.terminal.read", "host.terminal.write", "host.terminal.resize", "host.terminal.close":
		return s.terminal(ctx, method, params)
	}
	return nil, bridge.Fail("method_not_found")
}

// maxLogins bounds the sign-ins one Bridge runs at once; loginPoll is how long a poll waits for
// news; a sign-in its peer abandoned ends after loginLimit, as device codes do.
const (
	maxLogins  = 2
	loginPoll  = 20 * time.Second
	loginLimit = 15 * time.Minute
)

// hostLogin is a sign-in this machine runs for a peer, so a headless Orb is signed in from the
// phone: `orb login --json`, whose lines (the link, a device code, prompts, progress, the
// outcome) the peer polls and whose prompts it answers.
type hostLogin struct {
	stdin  io.WriteCloser
	cancel context.CancelFunc
	mu     sync.Mutex
	lines  []json.RawMessage
	done   bool
	wake   chan struct{}
}

func (l *hostLogin) add(line json.RawMessage, done bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if line != nil {
		l.lines = append(l.lines, line)
	}
	l.done = l.done || done
	close(l.wake)
	l.wake = make(chan struct{})
}

// loginExecutable is the orb a host sign-in runs; tests stand in a script.
var (
	loginExecutable = os.Executable
	providerName    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func (s *bridgeService) login(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	exe, err := loginExecutable()
	if err != nil {
		return nil, err
	}
	if method == "host.providers" {
		var q struct{}
		if err := protocol.Decode(params, &q); err != nil {
			return nil, err
		}
		output, err := exec.CommandContext(ctx, exe, "login", "--json").Output()
		if err != nil {
			return nil, bridge.Fail("unavailable")
		}
		rows := []json.RawMessage{}
		for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte("\n")) {
			if json.Valid(line) {
				rows = append(rows, append(json.RawMessage(nil), line...))
			}
		}
		return bridge.JSON(map[string]any{"providers": rows}), nil
	}
	if method == "host.login.start" {
		var q struct {
			Provider string `json:"provider"`
			Method   string `json:"method"`
		}
		if err := protocol.Decode(params, &q); err != nil {
			return nil, err
		}
		if !providerName.MatchString(q.Provider) || q.Method != "oauth" && q.Method != "api_key" {
			return nil, bridge.Fail("invalid_params")
		}
		return s.startLogin(exe, q.Provider, q.Method)
	}
	var q struct {
		ID     string `json:"login_id"`
		Cursor int    `json:"cursor,omitempty"`
		Value  string `json:"value,omitempty"`
	}
	if err := protocol.Decode(params, &q); err != nil {
		return nil, err
	}
	s.mu.Lock()
	l := s.logins[q.ID]
	s.mu.Unlock()
	if l == nil {
		return nil, bridge.Fail("not_found")
	}
	switch method {
	case "host.login.answer":
		l.mu.Lock()
		_, err := io.WriteString(l.stdin, strings.ReplaceAll(q.Value, "\n", " ")+"\n")
		l.mu.Unlock()
		if err != nil {
			return nil, bridge.Fail("unavailable")
		}
		return bridge.JSON(struct{}{}), nil
	case "host.login.cancel":
		l.cancel()
		s.endLogin(q.ID)
		return bridge.JSON(struct{}{}), nil
	}
	timer := time.NewTimer(loginPoll)
	defer timer.Stop()
	for {
		l.mu.Lock()
		lines, done, wake := l.lines[min(max(q.Cursor, 0), len(l.lines)):], l.done, l.wake
		cursor := len(l.lines)
		l.mu.Unlock()
		if len(lines) > 0 || done {
			if done {
				s.endLogin(q.ID)
			}
			return bridge.JSON(map[string]any{"events": lines, "cursor": cursor, "done": done}), nil
		}
		select {
		case <-wake:
		case <-timer.C:
			return bridge.JSON(map[string]any{"events": []json.RawMessage{}, "cursor": cursor, "done": false}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *bridgeService) startLogin(exe, provider, method string) (json.RawMessage, error) {
	s.mu.Lock()
	if len(s.logins) >= maxLogins {
		s.mu.Unlock()
		return nil, bridge.Fail("resource_exhausted")
	}
	id := protocol.NewID()
	ctx, cancel := context.WithTimeout(s.ctx, loginLimit)
	l := &hostLogin{cancel: cancel, wake: make(chan struct{})}
	s.logins[id] = l
	s.mu.Unlock()
	command := exec.CommandContext(ctx, exe, "login", "--json", provider, method)
	stdin, err := command.StdinPipe()
	var stdout io.ReadCloser
	if err == nil {
		stdout, err = command.StdoutPipe()
	}
	if err == nil {
		err = command.Start()
	}
	if err != nil {
		cancel()
		s.endLogin(id)
		return nil, err
	}
	l.stdin = stdin
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			if line := scanner.Bytes(); json.Valid(line) {
				l.add(append(json.RawMessage(nil), line...), false)
			}
		}
		// An Orb reads its accounts when it starts: the idle ones end, and reopen with the new
		// credential at their next message.
		if command.Wait() == nil {
			s.endLaunched(false)
		}
		cancel()
		l.add(nil, true)
		// The outcome waits a minute for its peer's poll.
		time.AfterFunc(time.Minute, func() { s.endLogin(id) })
	}()
	return bridge.JSON(map[string]string{"login_id": id}), nil
}

func (s *bridgeService) endLogin(id string) {
	s.mu.Lock()
	delete(s.logins, id)
	s.mu.Unlock()
}

// A peer reaching the machine (host.launch) may also open a terminal on it: its owner's login
// shell in a pseudo-terminal, in a folder, as Orb's own bash runs there. The output is kept as a
// window the peer reads by offset, waiting up to terminalPoll for more; one left unread ends.
const (
	maxTerminals   = 4
	terminalPoll   = 15 * time.Second // under the 20 s a peer gives any call
	terminalIdle   = 30 * time.Minute
	terminalWindow = 256 << 10
)

type hostTerminal struct {
	pty  *os.File
	idle *time.Timer
	mu   sync.Mutex
	out  []byte // the last terminalWindow bytes of output
	end  int64  // the offset just past out
	done bool
	wake chan struct{}
}

func (s *bridgeService) terminal(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	var q struct {
		ID     string `json:"terminal_id,omitempty"`
		CWD    string `json:"cwd,omitempty"`
		Cols   uint16 `json:"cols,omitempty"`
		Rows   uint16 `json:"rows,omitempty"`
		Offset int64  `json:"offset,omitempty"`
		Data   []byte `json:"data,omitempty"`
	}
	if err := protocol.Decode(params, &q); err != nil {
		return nil, err
	}
	size := &pty.Winsize{Cols: max(q.Cols, 20), Rows: max(q.Rows, 5)}
	if method == "host.terminal.open" {
		cwd, err := launchFolder(cmp.Or(q.CWD, "~"))
		if err != nil {
			return nil, err
		}
		return s.openTerminal(cwd, size)
	}
	s.mu.Lock()
	t := s.terminals[q.ID]
	s.mu.Unlock()
	if t == nil {
		return nil, bridge.Fail("not_found")
	}
	t.idle.Reset(terminalIdle)
	switch method {
	case "host.terminal.write":
		if _, err := t.pty.Write(q.Data); err != nil {
			return nil, bridge.Fail("unavailable")
		}
		return bridge.JSON(struct{}{}), nil
	case "host.terminal.resize":
		_ = pty.Setsize(t.pty, size)
		return bridge.JSON(struct{}{}), nil
	case "host.terminal.close":
		s.endTerminal(q.ID)
		return bridge.JSON(struct{}{}), nil
	}
	timer := time.NewTimer(terminalPoll)
	defer timer.Stop()
	for {
		t.mu.Lock()
		start := t.end - int64(len(t.out))
		data := slices.Clone(t.out[min(max(q.Offset, start), t.end)-start:])
		end, done, wake := t.end, t.done, t.wake
		t.mu.Unlock()
		if len(data) > 0 || done {
			return bridge.JSON(map[string]any{"data": data, "offset": end, "done": done}), nil
		}
		select {
		case <-wake:
		case <-timer.C:
			return bridge.JSON(map[string]any{"offset": end, "done": false}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *bridgeService) openTerminal(cwd string, size *pty.Winsize) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.terminals) >= maxTerminals {
		return nil, bridge.Fail("resource_exhausted")
	}
	command := exec.Command(cmp.Or(os.Getenv("SHELL"), "/bin/sh"), "-l")
	command.Dir, command.Env = cwd, append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(command, size)
	if err != nil {
		return nil, bridge.Fail("unavailable")
	}
	id := protocol.NewID()
	t := &hostTerminal{pty: f, wake: make(chan struct{})}
	t.idle = time.AfterFunc(terminalIdle, func() { s.endTerminal(id) })
	s.terminals[id] = t
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			t.mu.Lock()
			t.out = append(t.out, buf[:n]...)
			t.out = t.out[max(len(t.out)-terminalWindow, 0):]
			t.end += int64(n)
			t.done = err != nil
			close(t.wake)
			t.wake = make(chan struct{})
			t.mu.Unlock()
			if err != nil {
				break
			}
		}
		_ = command.Wait()
		// The last output and the end wait a minute for their peer's read.
		time.AfterFunc(time.Minute, func() { s.endTerminal(id) })
	}()
	return bridge.JSON(map[string]string{"terminal_id": id}), nil
}

// endTerminal hangs up a terminal: closing its side of the pty ends the shell and what it started.
func (s *bridgeService) endTerminal(id string) {
	s.mu.Lock()
	t := s.terminals[id]
	delete(s.terminals, id)
	s.mu.Unlock()
	if t != nil {
		t.idle.Stop()
		_ = t.pty.Close()
	}
}

// update brings this machine's orb to the latest release, as `orb update` does, then restarts
// the Bridge on the new binary; peers see it back within seconds. The answer says what happened.
func (s *bridgeService) update(ctx context.Context) json.RawMessage {
	u := selfupdate.New(version, false)
	result := map[string]string{"from": selfupdate.Plain(u.CurrentVersion)}
	tag, target, err := u.Update(ctx, func(string) func() { return func() {} })
	switch {
	case errors.Is(err, selfupdate.ErrDevelopment):
		result["status"] = "a development build; update it by hand"
	case errors.Is(err, selfupdate.ErrCurrent):
		result["status"] = "already current"
	case err != nil:
		result["status"] = err.Error()
	default:
		result["to"], result["status"] = selfupdate.Plain(tag), "updated · Bridge restarting"
		s.restart(target)
	}
	return bridge.JSON(result)
}

// launchFolder accepts an absolute path or one under ~, and only an existing directory.
func launchFolder(path string) (string, error) {
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || rest[0] == '/') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + rest
	}
	if len(path) > 4096 || !filepath.IsAbs(path) {
		return "", bridge.Fail("invalid_params")
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", bridge.Fail("not_found")
	}
	return path, nil
}

func (s *bridgeService) launch(ctx context.Context, p bridge.Principal, cwd, session string) (json.RawMessage, error) {
	s.mu.Lock()
	for _, l := range s.launched {
		// A thread already open in an Orb this Bridge started is opened again, not twice.
		if session != "" && l.session == session {
			alias := l.alias
			s.mu.Unlock()
			return s.launchedInstance(ctx, p, alias)
		}
	}
	if len(s.launched) >= maxLaunched {
		s.mu.Unlock()
		return nil, bridge.Fail("resource_exhausted")
	}
	// The slot is taken before the process starts, so concurrent launches cannot pass the cap.
	l := &launched{session: session, alias: "launch-" + strings.ToLower(protocol.NewID()[:8])}
	s.launched[l.alias] = l
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		delete(s.launched, l.alias)
		s.mu.Unlock()
	}
	// A thread open in another Orb on this machine, one in a terminal say, cannot open twice: the
	// peer hears so at once instead of an Orb that exits before it is on Bridge.
	if state := stateFromContext(s.ctx); session != "" && state != nil {
		if lock, err := state.OwnerLock(session); err == nil {
			free, _ := lock.TryLock()
			_ = lock.Close()
			if !free {
				release()
				// Open in an Orb on Bridge (a terminal one, say): the peer joins it there.
				if open := s.holding(ctx, p, session); open != nil {
					return open, nil
				}
				return nil, bridge.Fail("busy")
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		release()
		return nil, err
	}
	args := []string{"--mode", "rpc", "--bridge", s.profile, "--instance", l.alias}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	// Ephemeral: the instance retires its registration when it exits, like an unnamed one.
	cmd.Env = append(os.Environ(), "ORB_BRIDGE_EPHEMERAL=1")
	input, err := cmd.StdinPipe()
	var output io.ReadCloser
	if err == nil {
		output, err = cmd.StdoutPipe()
	}
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		release()
		return nil, err
	}
	s.mu.Lock()
	l.input = input
	s.mu.Unlock()
	go idle(output, l, launchedIdle)
	go func() {
		_ = cmd.Wait()
		release()
		_ = input.Close()
	}()
	result, err := s.launchedInstance(ctx, p, l.alias)
	if err != nil {
		_ = cmd.Process.Kill() // it never came up: nobody could reach it
	}
	return result, err
}

// holding is the Orb on Bridge that has the thread open, as host.launch answers it, or nil.
func (s *bridgeService) holding(ctx context.Context, p bridge.Principal, session string) json.RawMessage {
	for _, i := range s.b.Catalog(p) {
		if !i.Available {
			continue
		}
		raw, err := s.b.Handle(ctx, p.PeerID, "instances.describe", bridge.JSON(map[string]string{"instance_id": i.ID}))
		var d struct {
			Target struct {
				SessionID string `json:"session_id"`
			} `json:"target"`
		}
		if err == nil && json.Unmarshal(raw, &d) == nil && d.Target.SessionID == session {
			return bridge.JSON(map[string]string{"instance_id": i.ID, "alias": i.Alias})
		}
	}
	return nil
}

// idle reads a launched Orb's RPC events and closes its input — which ends it — once it has been
// quiet for limit outside a turn. Reading also keeps its output from filling up and blocking it.
func idle(events io.Reader, l *launched, limit time.Duration) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(events)
		for {
			line, _, err := r.ReadLine() // long lines come in pieces; the type leads each event
			if err != nil {
				return
			}
			last.Store(time.Now().UnixNano())
			if bytes.Contains(line, []byte(`"type":"agent_start"`)) {
				l.turn.Store(true)
			} else if bytes.Contains(line, []byte(`"type":"agent_end"`)) {
				l.turn.Store(false)
			}
		}
	}()
	tick := time.NewTicker(min(limit/4, time.Minute))
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			if !l.turn.Load() && time.Since(time.Unix(0, last.Load())) > limit {
				_ = l.input.Close()
				return
			}
		}
	}
}

// launchedInstance waits until the Orb with alias is on Bridge and names it: a minute at most,
// and not past the process, whose exit releases its slot.
func (s *bridgeService) launchedInstance(ctx context.Context, p bridge.Principal, alias string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, launchWait)
	defer cancel()
	for {
		s.mu.Lock()
		running := s.launched[alias] != nil
		s.mu.Unlock()
		if !running {
			return nil, bridge.Fail("unavailable")
		}
		for _, i := range s.b.Catalog(p) {
			if i.Alias == alias && i.Available {
				s.mu.Lock()
				if l := s.launched[alias]; l != nil {
					l.id = i.ID
				}
				s.mu.Unlock()
				return bridge.JSON(map[string]string{"instance_id": i.ID, "alias": alias}), nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, bridge.Fail("unavailable")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// stopLaunched ends every Orb this Bridge started by closing its input, and retires their
// registrations here: with the Bridge going away, they cannot retire themselves.
func (s *bridgeService) stopLaunched() { s.endLaunched(true) }

// endLaunched ends the Orbs this Bridge started, or only those between turns, and retires them.
func (s *bridgeService) endLaunched(busy bool) {
	s.mu.Lock()
	ids := []string{}
	for _, l := range s.launched {
		if !busy && l.turn.Load() {
			continue
		}
		if l.input != nil {
			_ = l.input.Close()
		}
		if l.id != "" {
			ids = append(ids, l.id)
		}
	}
	s.mu.Unlock()
	if len(ids) > 0 {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 2*time.Second)
		defer cancel()
		_, _ = s.b.Admin(ctx, "retire", bridge.JSON(map[string][]string{"instance_ids": ids}))
	}
}
