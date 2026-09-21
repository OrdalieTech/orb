package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type memoryStore struct {
	data []byte
	fail bool
}

func (s *memoryStore) Load() ([]byte, error) { return append([]byte(nil), s.data...), nil }
func (s *memoryStore) Save(b []byte) error {
	if s.fail {
		return errors.New("disk failure")
	}
	s.data = append([]byte(nil), b...)
	return nil
}

func TestLedgerAcceptanceAndRecovery(t *testing.T) {
	s := &memoryStore{}
	l, err := OpenLedger(s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Principal: Principal{PeerID: "peer", Subject: Subject{Kind: "controller"}}, Call: Call{InstanceID: "instance", OperationID: "op", Method: "prompt", Args: json.RawMessage(`{"text":"hello"}`)}}
	s.fail = true
	if _, _, err = l.Accept(req); err == nil {
		t.Fatal("accepted without durability")
	}
	s.fail = false
	l, err = OpenLedger(s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	r, exists, err := l.Accept(req)
	if err != nil || exists || r.Status != "accepted" {
		t.Fatal(r, exists, err)
	}
	r, exists, err = l.Accept(req)
	if err != nil || !exists {
		t.Fatal(r, exists, err)
	}
	req.Call.Args = json.RawMessage(`{"text":"other"}`)
	if _, _, err = l.Accept(req); Code(err) != "operation_conflict" {
		t.Fatal(err)
	}
	l, err = OpenLedger(s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	r, err = l.Get(req.Principal, "instance", "op")
	if err != nil || r.Status != "outcome_unknown" {
		t.Fatal(r, err)
	}
}

func TestObservationBoundsAndSnapshots(t *testing.T) {
	s := NewStream(2, 1024)
	s.Publish(json.RawMessage(`{"n":1}`))
	first := s.Snapshot()
	s.Publish(json.RawMessage(`{"n":2}`))
	events, err := s.Replay(first.Cursor)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	s.Publish(json.RawMessage(`{"n":3}`))
	s.Publish(json.RawMessage(`{"n":4}`))
	if _, err = s.Replay(first.Cursor); Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.Events) != 2 {
		t.Fatal(snap)
	}
}

func TestLedgerQuotaRetainsDeduplicationAndTerminalReceipts(t *testing.T) {
	s := &memoryStore{}
	l, err := OpenLedger(s, 1024)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Principal: Principal{PeerID: "peer", Subject: Subject{Kind: "controller"}}, Call: Call{InstanceID: "instance", OperationID: "original", Method: "prompt", Args: json.RawMessage(`{"text":"hello"}`)}}
	if _, _, err = l.Accept(req); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Set(req, "succeeded", json.RawMessage(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Set(req, "running", nil, ""); err == nil {
		t.Fatal("terminal receipt resumed")
	}
	full := false
	for i := 0; i < 20; i++ {
		next := req
		next.Call.OperationID = fmt.Sprint(i)
		if _, _, err = l.Accept(next); Code(err) == "resource_exhausted" {
			full = true
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("quota not enforced")
	}
	receipt, duplicate, err := l.Accept(req)
	if err != nil || !duplicate || receipt.Status != "succeeded" {
		t.Fatal(receipt, duplicate, err)
	}
}

type lostAcknowledgmentStore struct {
	memoryStore
	lose bool
}

func (s *lostAcknowledgmentStore) Save(b []byte) error {
	if err := s.memoryStore.Save(b); err != nil {
		return err
	}
	if s.lose {
		return errors.New("barrier outcome uncertain")
	}
	return nil
}
func TestLedgerAmbiguousPersistenceNeverReexecutes(t *testing.T) {
	s := &lostAcknowledgmentStore{lose: true}
	l, err := OpenLedger(s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Principal: Principal{PeerID: "peer", Subject: Subject{Kind: "controller"}}, Call: Call{InstanceID: "instance", OperationID: "op", Method: "prompt", Args: json.RawMessage(`{"text":"hello"}`)}}
	if _, _, err = l.Accept(req); err == nil {
		t.Fatal("ambiguous write acknowledged")
	}
	if _, _, err = l.Accept(req); Code(err) != "unavailable" {
		t.Fatal("poisoned writer admitted a retry", err)
	}
	s.lose = false
	l, err = OpenLedger(s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	receipt, duplicate, err := l.Accept(req)
	if err != nil || !duplicate || receipt.Status != "outcome_unknown" {
		t.Fatal(receipt, duplicate, err)
	}
}
