package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/document"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// attached is a faux-model runtime with an attachment at generation 1.
func attached(t *testing.T, steps ...faux.ResponseStep) (*Attachment, *runtime.AgentSessionRuntime) {
	t.Helper()
	return attachedWith(t, Options{}, steps...)
}

func attachedWith(t *testing.T, options Options, steps ...faux.ResponseStep) (*Attachment, *runtime.AgentSessionRuntime) {
	t.Helper()
	ctx := t.Context()
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	provider.SetResponses(steps)
	skills := []runtime.Skill{{Name: "review", Description: "Review a change", Content: "Read the diff.", FilePath: "/virtual/SKILL.md"}}
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &runtime.Resources{Skills: skills}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	options.InstanceID, options.Store, options.Authorize = protocol.NewID(), &document.Memory{}, func(bridge.Request) bool { return true }
	a, err := Attach(ctx, host, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err = a.SetGeneration("1"); err != nil {
		t.Fatal(err)
	}
	return a, host
}

var peer = bridge.Principal{PeerID: "peer", Subject: bridge.Subject{Kind: "controller"}}

// request is a call on the attached instance's current session.
func request(a *Attachment, method string, args any) bridge.Request {
	target := a.control.Target()
	return bridge.Request{Principal: peer, Generation: "1", Call: bridge.Call{InstanceID: a.options.InstanceID, Service: protocol.Service, Method: method, SessionID: target.SessionID, Expected: bridge.Expected{Generation: "1", Revision: target.Revision}, OperationID: protocol.NewID(), Args: bridge.JSON(args)}}
}

// settle waits for a call's receipt to leave accepted and running.
func settle(t *testing.T, a *Attachment, r bridge.Request) bridge.Receipt {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		receipt, err := a.ledger.Get(r.Principal, r.Call.InstanceID, r.Call.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Status != "accepted" && receipt.Status != "running" || time.Now().After(deadline) {
			return receipt
		}
	}
}

// call sends a call and waits for its outcome; a call refused up front reports its error code.
func call(t *testing.T, a *Attachment, method string, args any) (bridge.Receipt, string) {
	t.Helper()
	r := request(a, method, args)
	if _, err := a.Invoke(t.Context(), "call", bridge.JSON(r)); err != nil {
		return bridge.Receipt{}, bridge.Code(err)
	}
	return settle(t, a, r), ""
}

func TestCloseAttachmentKeepsRuntime(t *testing.T) {
	a, host := attached(t)
	_ = a.Close()
	if _, err := host.NewSession(context.Background(), nil); err != nil {
		t.Fatal("attachment disposed runtime", err)
	}
}

func TestAcceptedWorkOutlivesConnectionAndAttachment(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a, host := attached(t, faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
		close(started)
		<-release
		return faux.AssistantMessage("finished independently"), nil
	}))
	r := request(a, "prompt", map[string]string{"text": "work"})
	connection, disconnect := context.WithCancel(t.Context())
	raw, err := a.Invoke(connection, "call", bridge.JSON(r))
	var receipt bridge.Receipt
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Status != "accepted" {
		t.Fatal(receipt, err)
	}
	<-started
	disconnect()
	_ = a.Close()
	close(release)
	if receipt = settle(t, a, r); receipt.Status != "succeeded" {
		t.Fatal(receipt)
	}
	if _, err = host.NewSession(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotExpiresWhenLocalTranscriptResets(t *testing.T) {
	a, host := attached(t)
	raw, err := a.observe("", "", "", 0)
	var snapshot struct {
		ID     string `json:"snapshot_id"`
		Cursor string `json:"cursor"`
	}
	if err != nil || json.Unmarshal(raw, &snapshot) != nil {
		t.Fatal(err)
	}
	host.Session().Agent().SetMessages(nil)
	if _, err = a.observe(snapshot.Cursor, "", "", 0); bridge.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if _, err = a.observe("", snapshot.ID, "", 0); bridge.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
}

func TestAPeerRenamesTheSession(t *testing.T) {
	a, host := attached(t)
	if receipt, _ := call(t, a, "session.name", map[string]string{"name": "  Pairing race  "}); receipt.Status != "succeeded" {
		t.Fatal(receipt)
	}
	if name := host.Session().Manager().GetSessionName(); name == nil || *name != "Pairing race" {
		t.Fatalf("name = %v", name)
	}
	if _, code := call(t, a, "session.name", map[string]string{"name": "   "}); code != "invalid_params" {
		t.Fatal("blank name accepted:", code)
	}
}

// A follower long-polls: the call waits while nothing happens, and answers at once when the
// conversation changes, with the stream's events and the instance's new pulse.
func TestAFollowerWaitsForTheConversationToChange(t *testing.T) {
	a, host := attached(t)
	raw, _ := a.observe("", "", "", 0)
	var first struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(raw, &first)
	start, _ := a.replay(first.Cursor, protocol.MaxPage)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = host.Session().SetSessionName("renamed")
	}()
	began := time.Now()
	raw, err := a.Invoke(t.Context(), "events.subscribe", bridge.JSON(map[string]any{"principal": peer, "params": map[string]any{"instance_id": a.options.InstanceID, "cursor": first.Cursor, "wait": true, "state": start.State}}))
	var got page
	if err != nil || json.Unmarshal(raw, &got) != nil {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited < 200*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("answered after %v", waited)
	}
	if got.State == start.State || len(got.Events) != 1 || !strings.Contains(string(got.Events[0].Data), `"session_info_changed"`) {
		t.Fatalf("after a rename: %+v", got)
	}
}

// A new plan-limit reading moves the pulse: a waiting follower answers, and describes it.
func TestAFollowerSeesANewUsageReading(t *testing.T) {
	var reading atomic.Pointer[usage.Snapshot]
	a, _ := attachedWith(t, Options{Usage: func(*runtime.AgentSession) *usage.Snapshot { return reading.Load() }})
	raw, _ := a.observe("", "", "", 0)
	var first struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(raw, &first)
	start, _ := a.replay(first.Cursor, protocol.MaxPage)
	go func() {
		time.Sleep(300 * time.Millisecond)
		reading.Store(&usage.Snapshot{Windows: []usage.Window{{Name: "5h", Remaining: 62}}, CheckedAt: time.Now()})
	}()
	raw, err := a.Invoke(t.Context(), "events.subscribe", bridge.JSON(map[string]any{"principal": peer, "params": map[string]any{"instance_id": a.options.InstanceID, "cursor": first.Cursor, "wait": true, "state": start.State}}))
	var got page
	if err != nil || json.Unmarshal(raw, &got) != nil || got.State == start.State {
		t.Fatalf("follower kept waiting: %s %v", raw, err)
	}
	var d Descriptor
	if json.Unmarshal(a.inspect(""), &d) != nil || d.Usage == nil || d.Usage.Windows[0].Remaining != 62 {
		t.Fatalf("descriptor = %+v", d)
	}
}

// An image travels by reference: the snapshot names it, and a follower fetches it at the size it
// shows, fitted by the Orb, so neither the stream nor any frame carries the original.
func TestAFollowerFetchesAnImageAtTheSizeItShows(t *testing.T) {
	a, host := attached(t, faux.AssistantMessage("a gradient"))
	var original bytes.Buffer
	_ = png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 800, 400)))
	data := base64.StdEncoding.EncodeToString(original.Bytes())
	if err := host.Session().Prompt(t.Context(), "look", &ai.ImageContent{Data: data, MimeType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := a.observe("", "", "", 0)
	ref := regexp.MustCompile(`"ref":"([\w-]+)"`).FindSubmatch(raw)
	if ref == nil || bytes.Contains(raw, []byte(data)) {
		t.Fatalf("snapshot = %s", raw)
	}
	out, err := a.Invoke(t.Context(), "call", bridge.JSON(request(a, "image", map[string]any{"ref": string(ref[1]), "size": 200})))
	var got struct {
		Data string `json:"data"`
	}
	if err != nil || json.Unmarshal(out, &got) != nil {
		t.Fatal(out, err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(got.Data)
	if config, _, err := image.DecodeConfig(bytes.NewReader(decoded)); err != nil || config.Width != 200 || config.Height != 100 {
		t.Fatalf("fitted = %+v %v", config, err)
	}
	if _, code := call(t, a, "image", map[string]any{"ref": "gone", "size": 200}); code != "not_found" {
		t.Fatal("unknown image:", code)
	}
}

// A conversation larger than the window, with one output far larger than a page, still opens at
// its end: the oldest messages fall out of the window and the giant one keeps the ends of its text.
func TestAFollowerOpensAConversationLargerThanTheWindow(t *testing.T) {
	a, _ := attached(t)
	a.mu.Lock()
	for i := range 50 {
		role, text := "user", strings.Repeat("x", 200<<10)
		if i == 49 {
			role, text = "toolResult", strings.Repeat("y", 3<<20)
		}
		b, _ := json.Marshal(map[string]any{"role": role, "content": []map[string]string{{"type": "text", "text": text}}})
		a.appendMessage(a.swapImages(b))
	}
	a.mu.Unlock()
	var pages []json.RawMessage
	id, offset, from := "", "", -1
	for range 10 {
		raw, err := a.observe("", id, offset, 5)
		var got struct {
			ID       string            `json:"snapshot_id"`
			Messages []json.RawMessage `json:"messages"`
			From     int               `json:"from"`
			Offset   string            `json:"offset"`
		}
		if err != nil || json.Unmarshal(raw, &got) != nil || len(got.Messages) == 0 {
			t.Fatalf("page after %d: %v", len(pages), err)
		}
		if from < 0 {
			from = got.From
		}
		pages, id, offset = append(pages, got.Messages...), got.ID, got.Offset
		if offset == "" {
			break
		}
	}
	if last := pages[len(pages)-1]; from != 45 || len(pages) != 5 || len(last) > 16<<10 || !bytes.Contains(last, []byte("y\\n…\\ny")) {
		t.Fatalf("from %d, %d messages, last %d bytes", from, len(pages), len(last))
	}
	// Paging back from the start reaches where the window begins, past the messages it let go.
	raw, _ := a.observe("", "", "0", 0)
	var start struct {
		From int `json:"from"`
	}
	if json.Unmarshal(raw, &start) != nil || start.From == 0 || start.From >= 45 {
		t.Fatalf("window starts at %d", start.From)
	}
}

// A describe that sends the catalog digest it holds gets the descriptor without models and commands.
func TestADescribeLeavesOutTheCatalogItHolds(t *testing.T) {
	a, _ := attached(t)
	var full, brief map[string]json.RawMessage
	_ = json.Unmarshal(a.inspect(""), &full)
	var catalog string
	_ = json.Unmarshal(full["catalog"], &catalog)
	_ = json.Unmarshal(a.inspect(catalog), &brief)
	if catalog == "" || full["commands"] == nil || brief["models"] != nil || brief["commands"] != nil || string(brief["catalog"]) != string(full["catalog"]) {
		t.Fatalf("full %d bytes, brief %v", len(a.inspect("")), brief)
	}
}

// A follower that polls after a streamed turn receives the finished message, not every update
// it replaced, and no update repeats its message.
func TestAFollowerGetsTheLatestOfWhatStreamed(t *testing.T) {
	a, host := attachedWith(t, Options{}, faux.AssistantMessage(strings.Repeat("streamed words ", 200)))
	raw, _ := a.observe("", "", "", 0)
	var first struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(raw, &first)
	if err := host.Session().Prompt(t.Context(), "talk"); err != nil {
		t.Fatal(err)
	}
	all, _ := a.stream.Replay(first.Cursor)
	got, err := a.replay(first.Cursor, protocol.MaxPage)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, e := range all {
		if bytes.Contains(e.Data, []byte(`"message_update"`)) {
			updates++
			if bytes.Contains(e.Data, []byte("assistantMessageEvent")) {
				t.Fatal("an update repeats its message")
			}
		}
	}
	for _, e := range got.Events {
		if bytes.Contains(e.Data, []byte(`"message_update"`)) {
			t.Fatalf("sent a replaced update (%d of %d events)", len(got.Events), len(all))
		}
	}
	if updates < 2 || !bytes.Contains(got.Events[len(got.Events)-1].Data, []byte("agent_end")) {
		t.Fatalf("%d updates streamed, page ends %s", updates, got.Events[len(got.Events)-1].Data)
	}
}

// A controller compacts and runs a shell command as RPC's compact and bash would; the command
// and its output join the conversation, and the descriptor offers both with the instance's
// commands, reasoning and usage.
func TestAControllerRunsAShellCommandInTheConversation(t *testing.T) {
	a, host := attached(t)
	var d Descriptor
	if json.Unmarshal(a.inspect(""), &d) != nil || !d.Waits || d.Stats == nil || d.Thinking == "" || !strings.Contains(strings.Join(d.Methods, " "), "session.compact shell") {
		t.Fatalf("descriptor = %+v", d)
	}
	if goruntime.GOOS == "js" || goruntime.GOOS == "wasip1" {
		t.Skip("no processes to run a shell in")
	}
	if receipt, _ := call(t, a, "shell", map[string]string{"command": "echo from-the-phone"}); receipt.Status != "succeeded" || !strings.Contains(string(receipt.Result), "from-the-phone") {
		t.Fatal(receipt)
	}
	messages, _ := json.Marshal(host.Session().State().Messages)
	if !strings.Contains(string(messages), `"bashExecution"`) {
		t.Fatalf("messages = %s", messages)
	}
	if _, code := call(t, a, "shell", map[string]string{"command": " "}); code != "invalid_params" {
		t.Fatal("blank command accepted:", code)
	}
}

// A follower on a phone opens a long conversation at its end: the snapshot starts at the last
// tail messages and says where, so it can keep that window on a reload and page back from it.
func TestAFollowerOpensALongConversationAtItsEnd(t *testing.T) {
	a, host := attached(t)
	var messages engine.AgentMessages
	for i := range 10 {
		messages = append(messages, &ai.UserMessage{Content: ai.NewUserText(fmt.Sprint("message ", i)), Timestamp: int64(i + 1)})
	}
	host.Session().Agent().SetMessages(messages)
	read := func(offset string, tail int) (got struct {
		Messages []json.RawMessage `json:"messages"`
		From     int               `json:"from"`
	}) {
		raw, err := a.observe("", "", offset, tail)
		if err != nil || json.Unmarshal(raw, &got) != nil {
			t.Fatal(err)
		}
		return got
	}
	if end := read("", 3); end.From != 7 || len(end.Messages) != 3 || !strings.Contains(string(end.Messages[0]), "message 7") {
		t.Fatalf("tail: from %d, %d messages", end.From, len(end.Messages))
	}
	if again := read("7", 0); again.From != 7 || len(again.Messages) != 3 {
		t.Fatalf("same window: from %d, %d messages", again.From, len(again.Messages))
	}
	if all := read("", 0); all.From != 0 || len(all.Messages) != 10 {
		t.Fatalf("whole: from %d, %d messages", all.From, len(all.Messages))
	}
}
