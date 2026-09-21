package protocol

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestBidirectionalRPC(t *testing.T) {
	a, b := net.Pipe()
	echo := func(_ context.Context, m string, p json.RawMessage) (json.RawMessage, error) { return p, nil }
	left := NewConn(a, echo)
	right := NewConn(b, echo)
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, c := range []*Conn{left, right} {
		var got map[string]string
		if err := c.Call(ctx, "echo", map[string]string{"a": "b"}, &got); err != nil || got["a"] != "b" {
			t.Fatal(got, err)
		}
	}
}
func TestCloseWakesCalls(t *testing.T) {
	a, b := net.Pipe()
	left := NewConn(a, nil)
	defer func() { _ = b.Close() }()
	_ = left.Close()
	if err := left.Call(context.Background(), "ping", struct{}{}, nil); err == nil {
		t.Fatal("closed call succeeded")
	}
}

func TestNegotiatedFrameLimitReturnsBoundedError(t *testing.T) {
	a, b := net.Pipe()
	left := NewConn(a, nil)
	right := NewConn(b, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(make([]string, 2000))
	})
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	left.SetFrameLimit(1024)
	right.SetFrameLimit(1024)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := left.Call(ctx, "large", struct{}{}, nil)
	if err == nil || err.Error() != "resource_exhausted" {
		t.Fatal(err)
	}
}
