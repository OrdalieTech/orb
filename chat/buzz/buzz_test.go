package buzz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/OrdalieTech/orb/chat"
)

var (
	agentKey, _ = parseKey("0000000000000000000000000000000000000000000000000000000000000003")
	ownerKey, _ = parseKey("B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF")
	authTag     = attestation(ownerKey, agentKey.public)
	channel     = "7b0f4c3e-9d2a-4c55-8f1e-2a3b4c5d6e7f"
)

// relay is a fake Buzz relay: it challenges the agent, serves stored events
// on its subscription, answers queries from stored and takes events,
// checking the authentication of each.
type relay struct {
	t      *testing.T
	server *httptest.Server
	stored []event

	mu        sync.Mutex
	published []event
	ephemeral []event
	filters   []map[string]any
	live      chan event
}

func newRelay(t *testing.T, stored ...event) *relay {
	r := &relay{t: t, stored: stored, live: make(chan event, 4)}
	r.server = httptest.NewServer(r)
	t.Cleanup(r.server.Close)
	return r
}

func (r *relay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		r.subscribe(w, request)
		return
	}
	body, _ := io.ReadAll(request.Body)
	header, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(request.Header.Get("Authorization"), "Nostr "))
	var auth event
	digest := sha256.Sum256(body)
	_ = json.Unmarshal(header, &auth)
	var tag []string
	attested := request.Header.Get("x-auth-tag") == "" || json.Unmarshal([]byte(request.Header.Get("x-auth-tag")), &tag) == nil && checkAttestation(tag, auth.PubKey) == nil
	if !valid(auth) || auth.Kind != 27235 || auth.tag("u") != "http://"+request.Host+request.URL.Path ||
		auth.tag("payload") != hex.EncodeToString(digest[:]) || !attested {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch request.URL.Path {
	case "/query":
		var filters []map[string]any
		_ = json.Unmarshal(body, &filters)
		var found []event
		for _, e := range r.stored {
			if slices.ContainsFunc(filters, func(filter map[string]any) bool { return matches(filter, e) }) {
				found = append(found, e)
			}
		}
		_ = json.NewEncoder(w).Encode(found)
	case "/events":
		var e event
		_ = json.Unmarshal(body, &e)
		r.mu.Lock()
		r.published = append(r.published, e)
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"event_id": e.ID, "accepted": valid(e)})
	}
}

// valid reports that e's id and signature are its author's.
func valid(e event) bool {
	id := sha256.Sum256([]byte(e.serialize()))
	public, _ := hex.DecodeString(e.PubKey)
	signature, _ := hex.DecodeString(e.Sig)
	return e.ID == hex.EncodeToString(id[:]) && verify(public, id[:], signature)
}

// matches is the part of NIP-01 filtering the adapter's queries use.
func matches(filter map[string]any, e event) bool {
	for name, value := range filter {
		values, _ := value.([]any)
		has := func(candidate any) bool { return slices.Contains(values, candidate) }
		switch {
		case name == "ids" && !has(e.ID), name == "authors" && !has(e.PubKey), name == "kinds" && !has(float64(e.Kind)):
			return false
		case value == nil:
		case strings.HasPrefix(name, "#") && !slices.ContainsFunc(e.Tags, func(tag []string) bool { return len(tag) > 1 && tag[0] == name[1:] && has(tag[1]) }):
			return false
		}
	}
	return true
}

func (r *relay) subscribe(w http.ResponseWriter, request *http.Request) {
	conn, err := websocket.Accept(w, request, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx := request.Context()
	write := func(frame ...any) { data, _ := json.Marshal(frame); _ = conn.Write(ctx, websocket.MessageText, data) }
	write("AUTH", "challenge-1")
	// As Buzz's relay, it fans a channel's live events out only to
	// subscriptions that name the channel.
	var subscriptionsMu sync.Mutex
	subscriptions := map[string]map[string]any{}
	go func() {
		for e := range r.live {
			subscriptionsMu.Lock()
			for id, filter := range subscriptions {
				// Membership notifications are stored outside any channel.
				if _, named := filter["#h"]; named != (e.Kind == 44100) && matches(map[string]any{"#p": filter["#p"], "#h": filter["#h"], "kinds": filter["kinds"]}, e) {
					write("EVENT", id, e)
				}
			}
			subscriptionsMu.Unlock()
		}
	}()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame []json.RawMessage
		var label string
		_ = json.Unmarshal(data, &frame)
		_ = json.Unmarshal(frame[0], &label)
		switch label {
		case "AUTH":
			var auth event
			_ = json.Unmarshal(frame[1], &auth)
			valid := auth.Kind == 22242 && auth.tag("relay") == "ws://"+request.Host && auth.tag("challenge") == "challenge-1" && auth.tag("auth") == ownerKey.public
			write("OK", auth.ID, valid, "")
		case "REQ":
			var id string
			var filter map[string]any
			_ = json.Unmarshal(frame[1], &id)
			_ = json.Unmarshal(frame[2], &filter)
			r.mu.Lock()
			r.filters = append(r.filters, filter)
			r.mu.Unlock()
			subscriptionsMu.Lock()
			subscriptions[id] = filter
			subscriptionsMu.Unlock()
			for _, e := range r.stored {
				if matches(map[string]any{"#p": filter["#p"], "#h": filter["#h"], "kinds": filter["kinds"]}, e) {
					write("EVENT", id, e)
				}
			}
			write("EOSE", id)
		case "EVENT":
			var e event
			_ = json.Unmarshal(frame[1], &e)
			r.mu.Lock()
			r.ephemeral = append(r.ephemeral, e)
			r.mu.Unlock()
		}
	}
}

func (r *relay) adapter(t *testing.T, options Options) *Adapter {
	options.Key, options.Relay, options.AuthTag = "0000000000000000000000000000000000000000000000000000000000000003", strings.Replace(r.server.URL, "http", "ws", 1), authTag
	adapter, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func signed(k key, kind int, tags [][]string, content string, at int64) event {
	e := k.event(kind, tags, content)
	e.CreatedAt = at
	id := sha256.Sum256([]byte(e.serialize()))
	e.ID = hex.EncodeToString(id[:])
	return e
}

// A mention mid-thread reaches the agent as a message of the thread, with
// what was said there before it and never reached the agent; the agent's own
// events never do, and the cursor moves past what was published.
func TestPollPublishesMentionsWithTheirThread(t *testing.T) {
	alice, _ := parseKey("C90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B14E5C9")
	now := time.Now().Unix()
	root := signed(alice, 9, [][]string{{"h", channel}}, "The release is on Friday.", now-30)
	mention := signed(ownerKey, 9, [][]string{{"h", channel}, {"e", root.ID, "", "reply"}, {"p", agentKey.public},
		{"imeta", "url http://relay/media/" + strings.Repeat("a", 64) + ".png", "m image/png", "size 3"}}, "@Agent is that right?", now-10)
	own := signed(agentKey, 9, [][]string{{"h", channel}, {"p", agentKey.public}}, "my own", now-5)
	r := newRelay(t, root, mention, own,
		signed(alice, 0, nil, `{"display_name":"Alice"}`, now-100), signed(ownerKey, 0, nil, `{"name":"Baud"}`, now-100),
		signed(alice, 39000, [][]string{{"d", channel}, {"t", "stream"}}, "", now-100),
		signed(alice, 39000, [][]string{{"d", "solo-chat"}, {"private"}, {"t", "stream"}, {"t", "buzz.solo"}}, "", now-100),
		signed(alice, 39002, [][]string{{"d", channel}, {"p", agentKey.public}}, "", now-100),
		signed(alice, 39002, [][]string{{"d", "solo-chat"}, {"p", agentKey.public}}, "", now-100))
	// The agent was away for a minute: the subscription resumes from then.
	cursor := filepath.Join(t.TempDir(), "cursor")
	if err := os.WriteFile(cursor, []byte(strconv.FormatInt(now-60, 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := r.adapter(t, Options{Cursor: cursor})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan chat.Message, 4)
	go func() { _ = adapter.Poll(ctx, func(m chat.Message) error { messages <- m; return nil }) }()

	m := <-messages
	if m.EventID != mention.ID || m.ChatID != channel || m.ThreadID != root.ID || m.SenderID != ownerKey.public || m.SenderName != "Baud" || m.Account != agentKey.public {
		t.Fatalf("message %+v", m)
	}
	want := "Earlier in the thread:\nAlice: The release is on Friday.\n\nBaud: @Agent is that right?\n\n[buzz: channel " + channel + ", event " + mention.ID + ", from " + ownerKey.public + "]"
	if m.Text != want {
		t.Fatalf("text\n%s\nwant\n%s", m.Text, want)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Kind != "photo" || m.Attachments[0].Size != 3 {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	waitFor(t, func() bool {
		data, _ := os.ReadFile(cursor)
		return strings.TrimSpace(string(data)) == strconv.FormatInt(own.CreatedAt, 10)
	})
	r.mu.Lock()
	filter := r.filters[slices.IndexFunc(r.filters, func(filter map[string]any) bool { return filter["#p"] != nil })]
	r.mu.Unlock()
	if since, _ := filter["since"].(float64); int64(since) != now-60-skew || filter["#p"].([]any)[0] != agentKey.public || len(filter["#h"].([]any)) != 2 {
		t.Fatalf("subscription %v", filter)
	}

	// A live top-level mention opens a thread of its own.
	top := signed(alice, 9, [][]string{{"h", channel}, {"p", agentKey.public}}, "new topic", now)
	r.live <- top
	if m := <-messages; m.ThreadID != top.ID || !strings.HasPrefix(m.Text, "Alice: new topic") {
		t.Fatalf("live message %+v", m)
	}
	// Added to a channel while connected, it hears its mentions there.
	r.live <- signed(alice, 44100, [][]string{{"h", "joined-later"}, {"p", agentKey.public}}, "", now)
	waitFor(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.filters, func(filter map[string]any) bool { return fmt.Sprint(filter["#h"]) == "[joined-later]" })
	})
	later := signed(alice, 9, [][]string{{"h", "joined-later"}, {"p", agentKey.public}}, "welcome", now)
	r.live <- later
	if m := <-messages; m.EventID != later.ID {
		t.Fatalf("message in a joined channel %+v", m)
	}
	// It shows online, the way Buzz's clients do.
	waitFor(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.ephemeral, func(e event) bool { return e.Kind == 20001 && e.Content == "online" && valid(e) })
	})
	// A Solo chat, as a direct message, is one conversation.
	solo := signed(ownerKey, 9, [][]string{{"h", "solo-chat"}, {"p", agentKey.public}}, "hi", now)
	r.live <- solo
	if m := <-messages; m.ChatID != "solo-chat" || m.ThreadID != "" {
		t.Fatalf("solo message %+v", m)
	}
	select {
	case extra := <-messages:
		t.Fatalf("unexpected message %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// Replies go in the thread of the event they answer, signed by the agent
// with its owner's tag; a conversation's top level stays there.
func TestDeliveryRepliesInTheThread(t *testing.T) {
	r := newRelay(t)
	adapter := r.adapter(t, Options{})
	root, parent := strings.Repeat("1", 64), strings.Repeat("2", 64)
	for _, test := range []struct {
		key     chat.ConversationKey
		replyTo string
		want    [][]string
	}{
		{chat.ConversationKey{ChatID: channel, ThreadID: root}, parent, [][]string{{"h", channel}, {"e", root, "", "root"}, {"e", parent, "", "reply"}}},
		{chat.ConversationKey{ChatID: channel, ThreadID: root}, root, [][]string{{"h", channel}, {"e", root, "", "reply"}}},
		{chat.ConversationKey{ChatID: channel}, parent, [][]string{{"h", channel}}},
	} {
		receipt, err := adapter.NewDelivery(test.key, test.replyTo, "").Finalize(context.Background(), "Yes, Friday.")
		if err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		got := r.published[len(r.published)-1]
		r.mu.Unlock()
		want := append(test.want, strings.Split(strings.Trim(authTag, `[]"`), `","`))
		if got.Kind != 9 || got.Content != "Yes, Friday." || !slices.EqualFunc(got.Tags, want, slices.Equal) || receipt.MessageIDs[0] != got.ID {
			t.Fatalf("published %+v, want tags %v", got, want)
		}
	}
	if receipt, err := adapter.NewDelivery(chat.ConversationKey{ChatID: channel}, root, "").Finalize(context.Background(), ""); err != nil || len(receipt.MessageIDs) != 0 {
		t.Fatalf("an empty reply posted %v (%v)", receipt, err)
	}

	// An agent that posted in the conversation itself during the turn has
	// said its piece: its last words are not posted on top.
	during := adapter.NewDelivery(chat.ConversationKey{ChatID: channel, ThreadID: root}, parent, "")
	adapter.saying(event{ID: strings.Repeat("3", 64), Tags: [][]string{{"h", channel}, {"e", root, "", "root"}, {"e", parent, "", "reply"}}})
	r.mu.Lock()
	before := len(r.published)
	r.mu.Unlock()
	if receipt, err := during.Finalize(context.Background(), "I posted it."); err != nil || len(receipt.MessageIDs) != 0 {
		t.Fatalf("a reply after its own post: %v (%v)", receipt, err)
	}
	if receipt, err := adapter.NewDelivery(chat.ConversationKey{ChatID: channel, ThreadID: root}, parent, "").Finalize(context.Background(), "Next turn."); err != nil || len(receipt.MessageIDs) != 1 {
		t.Fatalf("the next turn's reply: %v (%v)", receipt, err)
	}
	r.mu.Lock()
	after := len(r.published)
	r.mu.Unlock()
	if after != before+1 {
		t.Fatalf("published %d events, want 1", after-before)
	}
}

func TestAuthorizeFollowsRespondTo(t *testing.T) {
	stranger := strings.Repeat("c", 64)
	friend := strings.Repeat("d", 64)
	for respondTo, admitted := range map[string][]string{"owner-only": {ownerKey.public}, "allowlist": {ownerKey.public, friend}, "anyone": {ownerKey.public, friend, stranger}} {
		adapter, err := New(Options{Key: strings.Repeat("0", 63) + "3", Relay: "wss://relay.example", AuthTag: authTag, RespondTo: respondTo, Allow: []string{friend}})
		if err != nil {
			t.Fatal(err)
		}
		for _, sender := range []string{ownerKey.public, friend, stranger} {
			if got := adapter.Authorize(chat.Message{SenderID: sender}) == nil; got != slices.Contains(admitted, sender) {
				t.Errorf("%s admits %s: %t", respondTo, sender, got)
			}
		}
	}
	if _, err := New(Options{Key: strings.Repeat("0", 63) + "3", Relay: "wss://relay.example"}); err == nil {
		t.Error("an owner-only agent without its owner's tag was accepted")
	}
}

func attestation(owner key, agent string) string {
	data, _ := json.Marshal(owner.attest(agent))
	return string(data)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !condition(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
