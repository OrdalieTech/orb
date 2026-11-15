// Package buzz is the Buzz platform: an agent on a Buzz relay under its own
// Nostr identity, with no other program. Events addressed to the agent come in
// on one WebSocket subscription; its replies, and whatever it reads or
// publishes with its tools, go through the relay's HTTP API signed as itself.
package buzz

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/internal/runechunk"
	"github.com/OrdalieTech/orb/chat/internal/typing"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/ctxsleep"
	"github.com/OrdalieTech/orb/internal/truncate"
)

// Options configures [New].
type Options struct {
	// Key is the agent's secret key, 64 hex characters or an nsec. Required.
	Key string
	// Relay is the relay's URL, ws(s):// or http(s)://. Required.
	Relay string
	// AuthTag is the NIP-OA tag its owner signed for the agent, as JSON: it
	// names the owner, and the relay admits the agent on the owner's behalf.
	AuthTag string
	// RespondTo is whom the agent answers: "owner-only" (the default),
	// "allowlist" (the owner and Allow) or "anyone".
	RespondTo string
	// Allow are the pubkeys an allowlist agent answers besides its owner.
	Allow []string
	// Channels are the channels whose every message is addressed to the agent,
	// besides the messages anywhere that mention it.
	Channels []string
	// Profile is merged into the agent's kind:0 at start.
	Profile chat.Identity
	// Cursor is a file keeping when the subscription last heard from the
	// relay, so what was sent while the agent was away reaches it on its
	// return. Without it the agent hears only from its start.
	Cursor string
	// HTTPClient overrides the HTTP client (tests).
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Adapter is the Buzz implementation of [chat.Adapter].
type Adapter struct {
	key       key
	authTag   []string
	owner     string
	respondTo string
	allow     []string
	channels  []string
	profile   chat.Identity
	cursor    string
	// last is what the cursor holds, kept for reconnects without a file.
	last   int64
	http   *http.Client
	logger *slog.Logger
	// base is the relay's HTTP URL, socket its WebSocket one, and origin
	// what NIP-42 names it by: the scheme and host alone.
	base, socket, origin, media, host string

	mu            sync.Mutex
	conn          *websocket.Conn
	names         map[string]string
	conversations map[string]bool
	// said is the agent's last post with its tools in each conversation:
	// its rank among them all, posts (a count, since clock readings can tie),
	// and its text.
	said  map[string]post
	posts int
	seen  map[string]bool
}

type post struct {
	rank int
	text string
}

var (
	_ chat.Adapter = (*Adapter)(nil)
	// kinds are what a message to the agent is: a stream message, a forum
	// post or a forum comment.
	kinds = []int{9, 45001, 45003}
)

const (
	userAgent = "orb"
	// maxChunk keeps a message within the relay's 64 KiB content limit.
	maxChunk = 16000
	// skew re-reads a little before the cursor: created_at has a resolution
	// of seconds, and redelivery is deduplicated.
	skew = 5
)

// New creates a Buzz adapter. It performs no network calls.
func New(options Options) (*Adapter, error) {
	k, err := parseKey(options.Key)
	if err != nil {
		return nil, err
	}
	relay, err := url.Parse(strings.TrimRight(strings.TrimSpace(options.Relay), "/"))
	if err != nil || relay.Host == "" {
		return nil, errors.New("buzz: the relay URL is required, such as wss://buzz.example.com")
	}
	secure := map[string]bool{"wss": true, "https": true, "ws": false, "http": false}
	tls, known := secure[relay.Scheme]
	if !known {
		return nil, fmt.Errorf("buzz: unsupported relay URL scheme %q", relay.Scheme)
	}
	httpScheme, wsScheme := "http", "ws"
	if tls {
		httpScheme, wsScheme = "https", "wss"
	}
	a := &Adapter{
		key: k, respondTo: options.RespondTo, channels: options.Channels, profile: options.Profile, cursor: options.Cursor,
		http: options.HTTPClient, logger: options.Logger, host: relay.Host,
		base: httpScheme + "://" + relay.Host + relay.Path, socket: wsScheme + "://" + relay.Host + relay.Path, origin: wsScheme + "://" + relay.Host, media: httpScheme + "://" + relay.Host + "/media/",
		names: map[string]string{}, conversations: map[string]bool{}, said: map[string]post{}, seen: map[string]bool{},
	}
	if a.http == nil {
		a.http = &http.Client{Timeout: time.Minute}
	}
	if a.logger == nil {
		a.logger = slog.New(slog.DiscardHandler)
	}
	if tag := strings.TrimSpace(options.AuthTag); tag != "" {
		_ = json.Unmarshal([]byte(tag), &a.authTag)
		if err := checkAttestation(a.authTag, k.public); err != nil {
			return nil, err
		}
		a.owner = a.authTag[1]
	}
	for _, pubkey := range options.Allow {
		if pubkey = strings.ToLower(strings.TrimSpace(pubkey)); pubkey != "" {
			if !isHex32(pubkey) {
				return nil, fmt.Errorf("buzz: %q is not a pubkey of 64 hex characters", pubkey)
			}
			a.allow = append(a.allow, pubkey)
		}
	}
	switch a.respondTo {
	case "":
		a.respondTo = "owner-only"
		fallthrough
	case "owner-only":
		if a.owner == "" {
			return nil, errors.New("buzz: an owner-only agent needs its owner's auth tag")
		}
	case "allowlist", "anyone":
	default:
		return nil, fmt.Errorf("buzz: respond_to is owner-only, allowlist or anyone, got %q", a.respondTo)
	}
	return a, nil
}

// isHex32 reports a pubkey or an event id: 32 bytes in lowercase hex.
func isHex32(text string) bool {
	raw, err := hex.DecodeString(text)
	return err == nil && len(raw) == 32 && text == strings.ToLower(text)
}

// Platform implements [chat.Adapter].
func (a *Adapter) Platform() string { return "buzz" }

// Account is the agent's pubkey.
func (a *Adapter) Account() string { return a.key.public }

// Authorize admits the senders RespondTo names.
func (a *Adapter) Authorize(m chat.Message) error {
	if a.respondTo == "anyone" || m.SenderID == a.owner || a.respondTo == "allowlist" && slices.Contains(a.allow, m.SenderID) {
		return nil
	}
	return fmt.Errorf("buzz: this agent answers %s, not %s", a.respondTo, m.SenderID)
}

// Poll subscribes to the events addressed to the agent and publishes each as
// a message until ctx ends, reconnecting as long as it takes. Its profile is
// published once a connection holds.
func (a *Adapter) Poll(ctx context.Context, publish func(chat.Message) error) error {
	profiled := false
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		connected, err := a.listen(ctx, publish, &profiled)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			delay = time.Second
		}
		a.logger.Warn("buzz: relay connection lost; reconnecting", "in", delay, "error", err)
		if err := ctxsleep.Sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// listen runs one authenticated connection: the subscriptions' stored events
// up to their EOSE, then live ones. connected reports it got that far.
func (a *Adapter) listen(ctx context.Context, publish func(chat.Message) error, profiled *bool) (connected bool, err error) {
	conn, _, err := websocket.Dial(ctx, a.socket, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1 << 22)
	read := func(ctx context.Context) (string, []json.RawMessage, error) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return "", nil, err
		}
		var frame []json.RawMessage
		var label string
		if json.Unmarshal(data, &frame) != nil || len(frame) == 0 || json.Unmarshal(frame[0], &label) != nil {
			return "", nil, nil
		}
		return label, frame[1:], nil
	}
	send := func(frame ...any) error {
		data, _ := json.Marshal(frame)
		return conn.Write(ctx, websocket.MessageText, data)
	}
	// NIP-42: the relay challenges first, and takes nothing before an answer.
	authenticate := func(challenge string) (string, error) {
		tags := [][]string{{"relay", a.origin}, {"challenge", challenge}}
		if a.authTag != nil {
			tags = append(tags, a.authTag)
		}
		auth := a.key.event(22242, tags, "")
		return auth.ID, send("AUTH", auth)
	}
	handshake, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	authID := ""
	for {
		label, frame, err := read(handshake)
		if err != nil {
			return false, fmt.Errorf("authenticating: %w", err)
		}
		var first string
		if len(frame) > 0 {
			_ = json.Unmarshal(frame[0], &first)
		}
		if label == "AUTH" {
			if authID, err = authenticate(first); err != nil {
				return false, err
			}
		}
		if label == "OK" && authID != "" && first == authID {
			if accepted, message := ok(frame); !accepted {
				return false, fmt.Errorf("the relay refused the agent: %s", message)
			}
			break
		}
	}
	a.setConn(conn)
	defer a.setConn(nil)
	// Presence (kind 20001, ephemeral, so on the WebSocket) shows the agent
	// online, renewed with the pings, and offline when it stops.
	presence := func(ctx context.Context, status string) error {
		data, _ := json.Marshal([]any{"EVENT", a.key.event(20001, nil, status)})
		return conn.Write(ctx, websocket.MessageText, data)
	}
	defer func() {
		if ctx.Err() != nil {
			stopping, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = presence(stopping, "offline")
			cancel()
		}
	}()
	if !*profiled {
		*profiled = a.publishProfile(ctx) == nil
	}
	// The relay fans a channel's events out only to subscriptions naming the
	// channel: one per batch of the channels the agent is in, and one per
	// channel it is added to (kind 44100) while connected.
	resume := a.resume()
	pending := map[string]bool{}
	subscribe := func(prefix string, channels []string, mentions bool, since int64) error {
		for at := 0; at < len(channels); at += 128 {
			filter := map[string]any{"kinds": kinds, "#h": channels[at:min(at+128, len(channels))], "since": since - skew}
			if mentions {
				filter["#p"] = []string{a.key.public}
			}
			id := fmt.Sprintf("%s-%d", prefix, at)
			pending[id] = true
			if err := send("REQ", id, filter); err != nil {
				return err
			}
		}
		return nil
	}
	rosters, err := a.query(ctx, map[string]any{"kinds": []int{39002}, "#p": []string{a.key.public}})
	if err != nil {
		return true, fmt.Errorf("listing the agent's channels: %w", err)
	}
	var member []string
	for _, roster := range rosters {
		if channel := roster.tag("d"); channel != "" && !slices.Contains(member, channel) {
			member = append(member, channel)
		}
	}
	pending["joined"] = true
	err = errors.Join(subscribe("mentions", member, true, resume), subscribe("channels", a.channels, false, resume),
		send("REQ", "joined", map[string]any{"kinds": []int{44100}, "#p": []string{a.key.public}, "since": resume - skew}), presence(ctx, "online"))
	if err != nil {
		return true, err
	}
	go func() {
		for ctx.Err() == nil {
			if ctxsleep.Sleep(ctx, 30*time.Second) != nil {
				return
			}
			ping, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := errors.Join(conn.Ping(ping), presence(ping, "online"))
			cancel()
			if err != nil {
				_ = conn.CloseNow()
				return
			}
		}
	}()
	// The cursor moves only once stored events are all published: they come
	// newest first.
	newest := resume
	for {
		label, frame, err := read(ctx)
		if err != nil {
			return true, err
		}
		var subscription string
		if len(frame) > 0 {
			_ = json.Unmarshal(frame[0], &subscription)
		}
		switch label {
		case "EVENT":
			var e event
			if len(frame) < 2 || json.Unmarshal(frame[1], &e) != nil {
				continue
			}
			// created_at is the sender's clock: one ahead of the relay's must
			// not move the cursor past what is still to come.
			at := min(e.CreatedAt, time.Now().Unix())
			if e.Kind == 44100 {
				if channel := e.tag("h"); channel != "" {
					err = subscribe("mentions-"+channel, []string{channel}, true, at)
				}
			} else {
				err = a.receive(ctx, e, publish)
			}
			if err != nil {
				return true, err
			}
			newest = max(newest, at)
		case "CLOSED":
			// A closed subscription would leave the agent online but deaf to
			// its channels: reconnecting resubscribes, after a growing delay.
			var reason string
			if len(frame) > 1 {
				_ = json.Unmarshal(frame[1], &reason)
			}
			return false, fmt.Errorf("the relay closed subscription %s: %s", subscription, reason)
		case "EOSE":
			delete(pending, subscription)
		case "AUTH":
			if _, err := authenticate(subscription); err != nil {
				return true, err
			}
		case "NOTICE":
			a.logger.Info("buzz: relay notice", "notice", subscription)
		}
		if len(pending) == 0 && newest != a.last {
			a.save(newest)
		}
	}
}

func ok(frame []json.RawMessage) (bool, string) {
	var accepted bool
	var message string
	if len(frame) > 2 {
		_ = json.Unmarshal(frame[1], &accepted)
		_ = json.Unmarshal(frame[2], &message)
	}
	return accepted, message
}

func (a *Adapter) setConn(conn *websocket.Conn) {
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
}

// resume is when the subscriptions last heard from the relay: this
// connection's predecessor, the cursor file, or now.
func (a *Adapter) resume() int64 {
	if a.last > 0 {
		return a.last
	}
	data, err := os.ReadFile(cmp.Or(a.cursor, os.DevNull))
	if seconds, parseErr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil && parseErr == nil {
		return seconds
	}
	return time.Now().Unix()
}

func (a *Adapter) save(seconds int64) {
	if a.last = seconds; a.cursor == "" {
		return
	}
	temporary := a.cursor + ".tmp"
	err := os.MkdirAll(filepath.Dir(a.cursor), 0o700)
	if err == nil {
		err = os.WriteFile(temporary, []byte(strconv.FormatInt(seconds, 10)+"\n"), 0o600)
	}
	if err == nil {
		err = os.Rename(temporary, a.cursor)
	}
	if err != nil {
		a.logger.Warn("buzz: cursor not saved", "error", err)
	}
}

// receive publishes an event addressed to the agent as a message, unless it
// is the agent's own or was already published.
func (a *Adapter) receive(ctx context.Context, e event, publish func(chat.Message) error) error {
	channel := e.tag("h")
	a.mu.Lock()
	seen := a.seen[e.ID]
	if len(a.seen) > 4096 {
		a.seen = map[string]bool{}
	}
	a.seen[e.ID] = true
	a.mu.Unlock()
	if seen || e.PubKey == a.key.public || channel == "" {
		return nil
	}
	root, _ := thread(e)
	if root == "" && !a.conversational(ctx, channel) {
		root = e.ID
	}
	m := chat.Message{
		EventID: e.ID, Platform: "buzz", Account: a.key.public, ChatID: channel, ThreadID: root,
		SenderID: e.PubKey, SenderName: a.name(ctx, e.PubKey), SentAt: time.Unix(e.CreatedAt, 0).UTC(),
	}
	var text strings.Builder
	if root != "" && root != e.ID {
		if earlier := a.unseen(ctx, e, channel, root); len(earlier) > 0 {
			text.WriteString("Earlier in the thread:\n" + strings.Join(earlier, "\n") + "\n\n")
		}
	}
	fmt.Fprintf(&text, "%s: %s\n\n[buzz: channel %s, event %s, from %s]", m.SenderName, e.Content, channel, e.ID, e.PubKey)
	m.Text = text.String()
	for _, tag := range e.Tags {
		if len(tag) == 0 || tag[0] != "imeta" {
			continue
		}
		ref := chat.AttachmentRef{}
		for _, field := range tag[1:] {
			name, value, _ := strings.Cut(field, " ")
			switch name {
			case "url":
				ref.ID = value
			case "m":
				ref.MIME, ref.Kind = value, chat.AttachmentKind(value)
			case "size":
				ref.Size, _ = strconv.ParseInt(value, 10, 64)
			}
		}
		if ref.ID != "" && ref.Kind != "" {
			m.Attachments = append(m.Attachments, ref)
		}
	}
	return publish(m)
}

// saying records that the agent posted e itself, in its conversation: the
// channel and the thread root, the event's own id for a top-level message in a
// channel of threads.
func (a *Adapter) saying(e event) {
	root, _ := thread(e)
	if root == "" && !a.conversational(context.Background(), e.tag("h")) {
		root = e.ID
	}
	a.mu.Lock()
	a.posts++
	a.said[e.tag("h")+"/"+root] = post{a.posts, strings.TrimSpace(e.Content)}
	a.mu.Unlock()
}

// thread is the NIP-10 root and parent an event replies to, or "" for a
// top-level one, as Buzz reads them: a lone reply marker is both.
func thread(e event) (root, parent string) {
	for _, tag := range e.Tags {
		if len(tag) < 4 || tag[0] != "e" || !isHex32(tag[1]) {
			continue
		}
		switch tag[3] {
		case "root":
			root = tag[1]
		case "reply":
			parent = tag[1]
		}
	}
	if parent == "" {
		return "", ""
	}
	if root == "" {
		root = parent
	}
	return root, parent
}

// unseen are the thread's messages before e and since the agent last spoke
// there that never reached it, as "Name: text" lines: being mentioned
// mid-thread, it learns what the thread is about.
func (a *Adapter) unseen(ctx context.Context, e event, channel, root string) []string {
	events, err := a.query(ctx, map[string]any{"kinds": []int{9, 40002}, "#e": []string{root}, "#h": []string{channel}, "limit": 50}, map[string]any{"ids": []string{root}})
	if err != nil {
		a.logger.Warn("buzz: thread context unavailable", "error", err)
		return nil
	}
	slices.SortFunc(events, func(x, y event) int { return int(x.CreatedAt - y.CreatedAt) })
	var lines []string
	for _, earlier := range events {
		switch {
		case earlier.ID == e.ID || earlier.CreatedAt > e.CreatedAt:
		case earlier.PubKey == a.key.public:
			lines = nil
		case !a.addressed(earlier):
			lines = append(lines, a.name(ctx, earlier.PubKey)+": "+earlier.Content)
		}
	}
	return lines[max(0, len(lines)-12):]
}

// addressed reports that the subscriptions deliver e.
func (a *Adapter) addressed(e event) bool {
	return slices.Contains(a.channels, e.tag("h")) || slices.ContainsFunc(e.Tags, func(tag []string) bool {
		return len(tag) > 1 && tag[0] == "p" && tag[1] == a.key.public
	})
}

// conversational reports that channel is one conversation at its top level,
// not a thread per message: a direct message, or a Solo chat, where Buzz 1.0
// has its owner talk with their agents.
func (a *Adapter) conversational(ctx context.Context, channel string) bool {
	a.mu.Lock()
	known, cached := a.conversations[channel]
	a.mu.Unlock()
	if cached {
		return known
	}
	events, err := a.query(ctx, map[string]any{"kinds": []int{39000}, "#d": []string{channel}, "limit": 1})
	if err != nil || len(events) == 0 {
		return false
	}
	conversation := slices.ContainsFunc(events[0].Tags, func(tag []string) bool {
		return len(tag) > 0 && tag[0] == "hidden" || len(tag) > 1 && tag[0] == "t" && (tag[1] == "dm" || tag[1] == "buzz.solo")
	})
	a.mu.Lock()
	a.conversations[channel] = conversation
	a.mu.Unlock()
	return conversation
}

// name is how pubkey's profile names it, or the start of the key.
func (a *Adapter) name(ctx context.Context, pubkey string) string {
	a.mu.Lock()
	name, known := a.names[pubkey]
	a.mu.Unlock()
	if known {
		return name
	}
	events, err := a.query(ctx, map[string]any{"kinds": []int{0}, "authors": []string{pubkey}, "limit": 1})
	if err != nil {
		return pubkey[:8]
	}
	var profile struct {
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
	}
	if len(events) > 0 {
		_ = json.Unmarshal([]byte(events[0].Content), &profile)
	}
	name = strings.TrimSpace(profile.DisplayName)
	if name == "" {
		name = strings.TrimSpace(profile.Name)
	}
	if name == "" {
		name = pubkey[:8]
	}
	a.mu.Lock()
	a.names[pubkey] = name
	a.mu.Unlock()
	return name
}

// publishProfile merges Profile into the agent's kind:0 on the relay, which
// carries its owner's tag: Buzz shows it as an agent by that tag.
func (a *Adapter) publishProfile(ctx context.Context) error {
	fields := map[string]string{"name": a.profile.Name, "display_name": a.profile.Name, "about": a.profile.About, "picture": a.profile.Avatar}
	profile := map[string]any{}
	events, err := a.query(ctx, map[string]any{"kinds": []int{0}, "authors": []string{a.key.public}, "limit": 1})
	if err == nil && len(events) > 0 {
		_ = json.Unmarshal([]byte(events[0].Content), &profile)
	}
	changed := false
	for name, value := range fields {
		if value != "" && profile[name] != value {
			profile[name], changed = value, true
		}
	}
	if err != nil || !changed {
		return err
	}
	content, _ := json.Marshal(profile)
	if _, err := a.publish(ctx, 0, nil, string(content)); err != nil {
		a.logger.Warn("buzz: profile not published", "error", err)
		return err
	}
	a.logger.Info("buzz: published the agent's profile")
	return nil
}

// post sends body to the relay's path, authenticated by NIP-98.
func (a *Adapter) post(ctx context.Context, path string, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	target := a.base + path
	digest := sha256.Sum256(data)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	auth := a.key.event(27235, [][]string{{"u", target}, {"method", http.MethodPost}, {"nonce", hex.EncodeToString(nonce)}, {"payload", hex.EncodeToString(digest[:])}}, "")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	header, _ := json.Marshal(auth)
	request.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(header))
	request.Header.Set("Content-Type", "application/json")
	response, err := a.do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	return io.ReadAll(io.LimitReader(response.Body, 16<<20))
}

// do sends a request with the headers every relay request carries, and fails
// on an error status with the relay's reason.
func (a *Adapter) do(request *http.Request) (*http.Response, error) {
	request.Header.Set("User-Agent", userAgent)
	if a.authTag != nil {
		tag, _ := json.Marshal(a.authTag)
		request.Header.Set("x-auth-tag", string(tag))
	}
	response, err := a.http.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 {
		reason, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return nil, fmt.Errorf("buzz: %s %s: %s %s", request.Method, request.URL.Path, response.Status, bytes.TrimSpace(reason))
	}
	return response, nil
}

// query returns the events matching any of filters.
func (a *Adapter) query(ctx context.Context, filters ...map[string]any) ([]event, error) {
	data, err := a.post(ctx, "/query", filters)
	if err != nil {
		return nil, err
	}
	var events []event
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("buzz: query: %w", err)
	}
	return events, nil
}

// publish signs an event as the agent, with its owner's tag as Buzz's own
// clients add it, and has the relay take it.
func (a *Adapter) publish(ctx context.Context, kind int, tags [][]string, content string) (string, error) {
	if a.authTag != nil {
		tags = append(slices.Clip(tags), a.authTag)
	}
	e := a.key.event(kind, tags, content)
	data, err := a.post(ctx, "/events", e)
	if err != nil {
		return "", err
	}
	var result struct {
		Accepted bool   `json:"accepted"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(data, &result); err != nil || !result.Accepted {
		return "", fmt.Errorf("buzz: the relay refused the event: %s", cmp.Or(result.Message, string(data)))
	}
	return e.ID, nil
}

// Download implements [chat.Adapter] for media on the relay, which asks for a
// Blossom authorization.
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if !strings.HasPrefix(ref.ID, a.media) || strings.Contains(ref.ID, "..") {
		return nil, "", fmt.Errorf("buzz: %s is not media on the relay", ref.ID)
	}
	auth := a.key.event(24242, [][]string{{"t", "get"}, {"expiration", strconv.FormatInt(time.Now().Unix()+600, 10)}, {"server", a.host}}, "Get media")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ID, nil)
	if err != nil {
		return nil, "", err
	}
	header, _ := json.Marshal(auth)
	request.Header.Set("Authorization", "Nostr "+base64.RawURLEncoding.EncodeToString(header))
	response, err := a.do(request)
	if err != nil {
		return nil, "", err
	}
	return response.Body, cmp.Or(ref.MIME, response.Header.Get("Content-Type")), nil
}

// NewDelivery implements [chat.Adapter]: the reply goes in the thread of the
// event it answers, replyTo, and a conversation's top level stays there.
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo string, _ string) chat.Delivery {
	a.mu.Lock()
	d := &delivery{adapter: a, tags: [][]string{{"h", key.ChatID}}, place: key.ChatID + "/" + key.ThreadID, started: a.posts}
	a.mu.Unlock()
	switch key.ThreadID {
	case "":
	case replyTo:
		d.tags = append(d.tags, []string{"e", replyTo, "", "reply"})
	default:
		d.tags = append(d.tags, []string{"e", key.ThreadID, "", "root"}, []string{"e", replyTo, "", "reply"})
	}
	return d
}

// delivery is one turn's output: typing while it runs, then its reply.
type delivery struct {
	adapter *Adapter
	tags    [][]string
	// place is the conversation, a channel and a thread root, and started
	// the agent's post count when the turn began: see said.
	place   string
	started int
	typing  typing.Refresher
	// sent are the chunks already posted, so a retried Finalize resumes.
	sent []string
}

// Typing implements [chat.Delivery]: Buzz shows it for a few seconds, so it
// is renewed until the reply.
func (d *delivery) Typing(ctx context.Context) error {
	err := d.typingEvent(ctx)
	d.typing.Start(ctx, 3*time.Second, func() bool { return d.typingEvent(ctx) == nil })
	return err
}

// typingEvent is ephemeral, which the relay takes on the WebSocket only.
func (d *delivery) typingEvent(ctx context.Context) error {
	d.adapter.mu.Lock()
	conn := d.adapter.conn
	d.adapter.mu.Unlock()
	if conn == nil {
		return errors.New("buzz: not connected")
	}
	data, _ := json.Marshal([]any{"EVENT", d.adapter.key.event(20002, d.tags, "")})
	return conn.Write(ctx, websocket.MessageText, data)
}

// Preview implements [chat.Delivery]: Buzz shows typing, not drafts.
func (d *delivery) Preview(context.Context, string) error { return nil }

// PreviewID implements [chat.Delivery].
func (d *delivery) PreviewID() string { return "" }

// Finalize implements [chat.Delivery]; an empty reply posts nothing, and
// neither does one that repeats what the agent posted in the conversation with
// buzz_publish during the turn: no longer than that post, or quoting it. A
// longer reply after a short post ("on it") is the answer, and is posted.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	d.typing.Stop()
	d.adapter.mu.Lock()
	last := d.adapter.said[d.place]
	d.adapter.mu.Unlock()
	reply := strings.TrimSpace(text)
	if last.rank > d.started && (len(reply) <= len(last.text) || last.text != "" && strings.Contains(reply, last.text)) {
		return chat.Receipt{At: time.Now().UTC()}, nil
	}
	for _, chunk := range runechunk.Split(text, maxChunk)[len(d.sent):] {
		id, err := d.adapter.publish(ctx, 9, d.tags, chunk)
		if err != nil {
			return chat.Receipt{}, err
		}
		d.sent = append(d.sent, id)
	}
	return chat.Receipt{MessageIDs: d.sent, At: time.Now().UTC()}, nil
}

// Notify implements [chat.Delivery].
func (d *delivery) Notify(ctx context.Context, text string) error {
	d.typing.Stop()
	_, err := d.adapter.publish(ctx, 9, d.tags, text)
	return err
}

// Extension gives the agent's sessions its Buzz tools: everything on Buzz is
// an event, so reading any and publishing any as the agent covers it all.
func (a *Adapter) Extension(api extensions.API) error {
	guidelines := []string{
		"On Buzz you are " + a.key.public + "; every message, reaction, canvas and channel is a Nostr event. Your reply to the message you answer is posted for you, where it belongs: use buzz_publish for anything else.",
		`buzz_query filters: a channel's messages {"kinds":[9],"#h":[channel],"limit":50}; a thread {"kinds":[9],"#e":[root]} and {"ids":[root]}; a search {"kinds":[9],"search":"words"}; your channels {"kinds":[39002],"#p":[you]} (the d tag is the channel), their names {"kinds":[39000],"#d":[channels]}; profiles {"kinds":[0],"authors":[pubkeys]}; a canvas {"kinds":[40100],"#h":[channel]}.`,
		`buzz_publish events: a message is kind 9 with ["h",channel], in a thread also ["e",root,"","reply"], or ["e",root,"","root"] and ["e",parent,"","reply"] under a reply; mention with @Name in the content and ["p",pubkey]; a reaction is kind 7, the emoji as content, with ["e",event]; an edit of your message is kind 40003 with ["h",channel] and ["e",event]; a canvas is kind 40100 with ["h",channel], the whole document as content.`,
	}
	api.RegisterTool(extensions.ToolDefinition{
		Name: "buzz_query", Label: "Buzz Query", PromptGuidelines: guidelines,
		Description: "Read events from the Buzz relay, as yourself: the events matching any of the Nostr filters, one JSON event per line.",
		Parameters:  ai.JSONSchema(`{"type":"object","required":["filters"],"properties":{"filters":{"type":"array","minItems":1,"items":{"type":"object"},"description":"Nostr filters: ids, authors, kinds, #<tag>, since, until, limit, search."}}}`),
		Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			var input struct {
				Filters []map[string]any `json:"filters"`
			}
			if err := decode(raw, &input); err != nil || len(input.Filters) == 0 {
				return engine.AgentToolResult{}, errors.New("buzz_query: filters are required")
			}
			events, err := a.query(ctx, input.Filters...)
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			var lines []string
			for _, e := range events {
				e.Sig = ""
				line, _ := json.Marshal(e)
				lines = append(lines, string(line))
			}
			result := truncate.TruncateHead(strings.Join(lines, "\n"))
			text := cmp.Or(result.Content, "no events")
			if result.Truncated {
				text += fmt.Sprintf("\n[%d of %d events shown: narrow the filters or lower limit]", result.OutputLines, len(events))
			}
			return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}, nil
		},
	})
	api.RegisterTool(extensions.ToolDefinition{
		Name: "buzz_publish", Label: "Buzz Publish",
		Description: "Sign an event as yourself and publish it on the Buzz relay: a message, reaction, edit, canvas or any other event.",
		Parameters:  ai.JSONSchema(`{"type":"object","required":["kind"],"properties":{"kind":{"type":"integer"},"content":{"type":"string"},"tags":{"type":"array","items":{"type":"array","items":{"type":"string"}}}}}`),
		Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			var input struct {
				Kind    *int       `json:"kind"`
				Content string     `json:"content"`
				Tags    [][]string `json:"tags"`
			}
			if err := decode(raw, &input); err != nil || input.Kind == nil {
				return engine.AgentToolResult{}, errors.New("buzz_publish: kind is required")
			}
			id, err := a.publish(ctx, *input.Kind, input.Tags, input.Content)
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			if *input.Kind == 9 {
				a.saying(event{Tags: input.Tags, ID: id, Content: input.Content})
			}
			return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "Published event " + id}}}, nil
		},
	})
	return nil
}

func decode(raw, into any) error {
	data, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(data, into)
	}
	return err
}
