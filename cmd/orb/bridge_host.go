package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/semver"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
)

// maxLaunched bounds the Orbs one Bridge starts for its peers; each is a full agent process.
// One that shows no activity for launchedIdle ends; its thread reopens with the next message.
const (
	maxLaunched  = 8
	launchedIdle = 30 * time.Minute
)

// launched is an Orb this Bridge started for a peer. It lives while its stdin stays open: until
// its peer's thread ends with the Bridge, or the process exits by itself.
type launched struct {
	input   io.Closer
	session string
	alias   string
	id      string // its instance, once on Bridge
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
		}{strings.Split(host, ".")[0], plainVersion(buildVersion(version, info, ok)), []item{}, page.Next}
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
			found := false
			for _, r := range rows {
				if r.ID == q.SessionID {
					q.CWD, found = r.CWD, true
					break
				}
			}
			if !found {
				return nil, bridge.Fail("not_found")
			}
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
	}
	return nil, bridge.Fail("method_not_found")
}

// update brings this machine's orb to the latest release, as `orb update` does, then restarts
// the Bridge on the new binary; peers see it back within seconds. The answer says what happened.
func (s *bridgeService) update(ctx context.Context) json.RawMessage {
	info, ok := debug.ReadBuildInfo()
	u := newSelfUpdater(buildVersion(version, info, ok), false)
	u.client = guardRedirects(u.client)
	result := map[string]string{"from": plainVersion(u.currentVersion)}
	say := func(status string) json.RawMessage { result["status"] = status; return bridge.JSON(result) }
	current, parsed := semver.Parse(u.currentVersion)
	if !parsed || isDevelopmentVersion(u.currentVersion) {
		return say("a development build; update it by hand")
	}
	tag, err := fetchLatestReleaseVersion(ctx, u.currentVersion, u.client, u.releaseURL, selfUpdateMetadataWait)
	if err != nil {
		return say(err.Error())
	}
	if latest, ok := semver.Parse(tag); !ok || semver.Compare(latest, current) <= 0 {
		return say("already current")
	}
	target, before, err := u.resolveTarget()
	if err == nil {
		var payload []byte
		if payload, err = u.download(ctx, tag); err == nil {
			err = swapBinary(target, payload, before)
		}
	}
	if err != nil {
		return say(err.Error())
	}
	result["to"] = plainVersion(tag)
	s.restart(target)
	return say("updated · Bridge restarting")
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
	go idle(output, input, launchedIdle)
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

// idle reads a launched Orb's RPC events and closes its input — which ends it — once it has been
// quiet for limit outside a turn. Reading also keeps its output from filling up and blocking it.
func idle(events io.Reader, input io.Closer, limit time.Duration) {
	var last atomic.Int64
	var turn atomic.Bool
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
				turn.Store(true)
			} else if bytes.Contains(line, []byte(`"type":"agent_end"`)) {
				turn.Store(false)
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
			if !turn.Load() && time.Since(time.Unix(0, last.Load())) > limit {
				_ = input.Close()
				return
			}
		}
	}
}

// launchedInstance waits until the Orb with alias is on Bridge and names it.
func (s *bridgeService) launchedInstance(ctx context.Context, p bridge.Principal, alias string) (json.RawMessage, error) {
	for {
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
func (s *bridgeService) stopLaunched() {
	s.mu.Lock()
	ids := []string{}
	for _, l := range s.launched {
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
