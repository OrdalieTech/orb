package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

type Model struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Thinking []string `json:"thinking,omitempty"`
}

type Store interface {
	Load() ([]byte, error)
	Save([]byte) error
}

// Endpoint is implemented by in-process attachments and native IPC clients.
// Close releases remote availability; it never disposes the attached runtime.
type Endpoint interface {
	Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Close() error
}

type Subject struct {
	Kind       string `json:"kind"`
	InstanceID string `json:"instance_id,omitempty"`
}
type Principal struct {
	PeerID  string  `json:"peer_id"`
	Subject Subject `json:"subject"`
}
type Expected struct {
	Generation string `json:"registration_generation"`
	Revision   string `json:"session_revision"`
}
type Call struct {
	InstanceID  string          `json:"instance_id"`
	Service     string          `json:"service"`
	Method      string          `json:"method"`
	SessionID   string          `json:"session_id,omitempty"`
	Expected    Expected        `json:"expected"`
	OperationID string          `json:"operation_id,omitempty"`
	Args        json.RawMessage `json:"args"`
}
type Request struct {
	PageLimit  int       `json:"page_limit,omitempty"`
	Principal  Principal `json:"principal"`
	Generation string    `json:"generation"`
	Call       Call      `json:"call"`
}
type Receipt struct {
	InstanceID  string          `json:"instance_id"`
	SessionID   string          `json:"session_id,omitempty"`
	Method      string          `json:"method"`
	Expected    Expected        `json:"expected"`
	AcceptedAt  int64           `json:"accepted_at"`
	OperationID string          `json:"operation_id"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Digest      string          `json:"digest"`
}
type Error struct {
	Kind string `json:"kind"`
}

func (e *Error) Error() string { return e.Kind }
func Fail(code string) error   { return &Error{Kind: code} }
func Code(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	var rpc *protocol.RPCError
	if errors.As(err, &rpc) {
		switch rpc.Message {
		case "unauthorized", "not_found", "busy", "stale_target", "cursor_expired", "resource_exhausted", "operation_conflict", "identity_conflict", "unsupported_version":
			return rpc.Message
		}
	}
	return "unavailable"
}
func JSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

type Ledger struct {
	poisoned bool
	mu       sync.Mutex
	store    Store
	quota    int
	entries  map[string]Receipt
}
type ledgerFile struct {
	Version int                `json:"version"`
	Entries map[string]Receipt `json:"entries"`
}

func OpenLedger(store Store, quota int) (*Ledger, error) {
	if store == nil || quota < 1024 {
		return nil, errors.New("ledger requires storage and quota")
	}
	l := &Ledger{store: store, quota: quota, entries: map[string]Receipt{}}
	b, err := store.Load()
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return l, nil
	}
	if len(b) > quota {
		return nil, Fail("resource_exhausted")
	}
	var f ledgerFile
	if err = protocol.Decode(b, &f); err != nil {
		return nil, err
	}
	if f.Version != 1 || f.Entries == nil {
		return nil, Fail("unsupported_version")
	}
	l.entries = f.Entries
	changed := false
	for k, r := range l.entries {
		var parts []string
		digest, decodeErr := hex.DecodeString(r.Digest)
		if json.Unmarshal([]byte(k), &parts) != nil || len(parts) != 5 || parts[4] != r.OperationID || parts[3] != r.InstanceID || decodeErr != nil || len(digest) != 32 || r.AcceptedAt <= 0 {
			return nil, errors.New("corrupt operation receipt")
		}

		switch r.Status {
		case "accepted", "running":
			r.Status = "outcome_unknown"
			l.entries[k] = r
			changed = true
		case "succeeded", "failed", "cancelled", "outcome_unknown":
		default:
			return nil, errors.New("invalid receipt state")
		}
	}
	if changed {
		if err = l.persist(); err != nil {
			return nil, err
		}
	}
	return l, nil
}
func ledgerKey(p Principal, instance, op string) string {
	return string(JSON([]string{p.PeerID, p.Subject.Kind, p.Subject.InstanceID, instance, op}))
}
func digestRequest(r Request) (string, error) {
	b, err := protocol.Canonical(JSON(struct {
		Principal Principal `json:"principal"`
		Call      Call      `json:"call"`
	}{r.Principal, r.Call}))
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func (l *Ledger) Accept(r Request) (Receipt, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.poisoned {
		return Receipt{}, false, Fail("unavailable")
	}
	d, err := digestRequest(r)
	if err != nil {
		return Receipt{}, false, err
	}
	k := ledgerKey(r.Principal, r.Call.InstanceID, r.Call.OperationID)
	if old, ok := l.entries[k]; ok {
		if old.Digest != d {
			return Receipt{}, true, Fail("operation_conflict")
		}
		return old, true, nil
	}
	receipt := Receipt{InstanceID: r.Call.InstanceID, SessionID: r.Call.SessionID, Method: r.Call.Method, Expected: r.Call.Expected, AcceptedAt: time.Now().UnixMilli(), OperationID: r.Call.OperationID, Status: "accepted", Digest: d}
	l.entries[k] = receipt
	if err = l.persist(); err != nil {
		delete(l.entries, k)
		return Receipt{}, false, err
	}
	return receipt, false, nil
}
func (l *Ledger) Get(p Principal, instance, op string) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.entries[ledgerKey(p, instance, op)]
	if !ok {
		return Receipt{}, Fail("not_found")
	}
	r.Result = append(json.RawMessage(nil), r.Result...)
	return r, nil
}
func (l *Ledger) Set(r Request, status string, result json.RawMessage, code string) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey(r.Principal, r.Call.InstanceID, r.Call.OperationID)
	old, ok := l.entries[k]
	if !ok {
		return Receipt{}, Fail("not_found")
	}
	if l.poisoned {
		return Receipt{}, Fail("unavailable")
	}
	if status != "running" && status != "succeeded" && status != "failed" && status != "cancelled" && status != "outcome_unknown" {
		return Receipt{}, Fail("operation_conflict")
	}
	if old.Status != "accepted" && old.Status != "running" {
		return Receipt{}, Fail("operation_conflict")
	}
	next := old
	next.Status = status
	next.Result = append(json.RawMessage(nil), result...)
	next.Error = code
	l.entries[k] = next
	if err := l.persist(); err != nil {
		l.entries[k] = old
		return Receipt{}, err
	}
	return next, nil
}
func (l *Ledger) persist() error {
	b := JSON(ledgerFile{Version: 1, Entries: l.entries})
	if len(b) > l.quota {
		return Fail("resource_exhausted")
	}
	if err := l.store.Save(b); err != nil {
		l.poisoned = true
		return err
	}
	return nil
}

type Event struct {
	Cursor string          `json:"cursor"`
	Data   json.RawMessage `json:"data"`
}
type Snapshot struct {
	Cursor string  `json:"cursor"`
	Events []Event `json:"events"`
}
type Stream struct {
	mu                  sync.Mutex
	epoch               string
	sequence            uint64
	events              []Event
	limit, quota, bytes int
}

func NewStream(limit, quota int) *Stream {
	if limit < 1 || quota < 1 {
		panic("invalid stream limits")
	}
	return &Stream{epoch: protocol.NewID(), limit: limit, quota: quota}
}
func (s *Stream) cursor() string { return s.epoch + ":" + strconv.FormatUint(s.sequence, 10) }
func (s *Stream) Publish(data json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequence == ^uint64(0) {
		s.epoch = protocol.NewID()
		s.sequence = 0
		s.events = nil
		s.bytes = 0
	}
	s.sequence++
	if len(data) > s.quota {
		s.events = nil
		s.bytes = 0
		return
	}
	for len(s.events) > 0 && (len(s.events) >= s.limit || s.bytes+len(data) > s.quota) {
		s.bytes -= len(s.events[0].Data)
		s.events = s.events[1:]
	}
	s.events = append(s.events, Event{Cursor: s.cursor(), Data: append(json.RawMessage(nil), data...)})
	s.bytes += len(data)
}
func copyEvents(events []Event) []Event {
	out := make([]Event, len(events))
	for i, e := range events {
		out[i] = Event{Cursor: e.Cursor, Data: append(json.RawMessage(nil), e.Data...)}
	}
	return out
}
func (s *Stream) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{Cursor: s.cursor(), Events: copyEvents(s.events)}
}
func (s *Stream) Replay(cursor string) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch, n, ok := strings.Cut(cursor, ":")
	seq, err := protocol.Counter(n)
	if !ok || err != nil || epoch != s.epoch || seq > s.sequence || s.sequence-seq > uint64(len(s.events)) {
		return nil, Fail("cursor_expired")
	}
	return copyEvents(s.events[len(s.events)-int(s.sequence-seq):]), nil
}

func ValidateCall(c Call) error {
	if !protocol.ValidID(c.InstanceID) || c.Service != protocol.Service {
		return Fail("unsupported_version")
	}
	switch c.Method {
	case "inspect", "session.list":
	case "input.reply", "prompt", "steer", "follow_up", "cancel", "session.new", "session.switch", "session.fork", "session.model":
		if _, err := protocol.Counter(c.Expected.Generation); err != nil {
			return Fail("stale_target")
		}
		if _, err := protocol.Counter(c.Expected.Revision); err != nil {
			return Fail("stale_target")
		}
		if c.SessionID == "" || len(c.SessionID) > 128 {
			return Fail("stale_target")
		}
		if !protocol.ValidID(c.OperationID) {
			return Fail("operation_conflict")
		}
	default:
		return fmt.Errorf("unsupported method: %w", Fail("not_found"))
	}
	if _, err := protocol.Canonical(c.Args); err != nil {
		return err
	}
	return nil
}

// Local is a non-owning in-process registration channel. Closing it fences
// delivery without closing the runtime adapter or its operation ledger.
type Local struct {
	mu      sync.RWMutex
	handler func(context.Context, string, json.RawMessage) (json.RawMessage, error)
	closed  bool
}

func NewLocal(handler func(context.Context, string, json.RawMessage) (json.RawMessage, error)) *Local {
	return &Local{handler: handler}
}
func (l *Local) Invoke(ctx context.Context, method string, p json.RawMessage) (json.RawMessage, error) {
	l.mu.RLock()
	closed, handler := l.closed, l.handler
	l.mu.RUnlock()
	if closed || handler == nil {
		return nil, Fail("unavailable")
	}
	return handler(ctx, method, p)
}
func (l *Local) Close() error { l.mu.Lock(); l.closed = true; l.mu.Unlock(); return nil }
