package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type memoryStore struct {
	data []byte
	fail bool
}

func (s *memoryStore) Read(context.Context) ([]byte, error) {
	return append([]byte(nil), s.data...), nil
}
func (s *memoryStore) Update(_ context.Context, change func([]byte) ([]byte, error)) (err error) {
	if s.fail {
		return errors.New("disk failure")
	}
	s.data, err = change(s.data)
	return err
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
	first := s.Cursor()
	s.Publish(json.RawMessage(`{"n":2}`))
	events, err := s.Replay(first)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	second := s.Cursor()
	s.Publish(json.RawMessage(`{"n":3}`))
	s.Publish(json.RawMessage(`{"n":4}`))
	if _, err = s.Replay(first); Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if events, err = s.Replay(second); err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
}

func TestLedgerQuotaEvictsOldestTerminalReceipts(t *testing.T) {
	s := &memoryStore{}
	l, err := OpenLedger(s, 1024)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Principal: Principal{PeerID: "peer", Subject: Subject{Kind: "controller"}}, Call: Call{InstanceID: "instance", Method: "prompt", Args: json.RawMessage(`{"text":"hello"}`)}}
	op := func(id string) Request { next := req; next.Call.OperationID = id; return next }
	for i := 0; i < 20; i++ {
		if _, _, err = l.Accept(op(fmt.Sprint(i))); err != nil {
			t.Fatal(i, err)
		}
		if _, err = l.Set(op(fmt.Sprint(i)), "succeeded", json.RawMessage(`{}`), ""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = l.Get(req.Principal, "instance", "0"); Code(err) != "not_found" {
		t.Fatal("oldest terminal receipt kept past the quota", err)
	}
	if receipt, duplicate, err := l.Accept(op("19")); err != nil || !duplicate || receipt.Status != "succeeded" {
		t.Fatal(receipt, duplicate, err)
	}
	full := false
	for i := 0; i < 20 && !full; i++ {
		if _, _, err = l.Accept(op(fmt.Sprint("running", i))); Code(err) == "resource_exhausted" {
			full = true
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("quota not enforced for operations in flight")
	}
	if receipt, duplicate, err := l.Accept(op("running0")); err != nil || !duplicate || receipt.Status != "accepted" {
		t.Fatal("receipt in flight evicted", receipt, duplicate, err)
	}
}

type lostAcknowledgmentStore struct {
	memoryStore
	lose bool
}

func (s *lostAcknowledgmentStore) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	if err := s.memoryStore.Update(ctx, change); err != nil {
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
