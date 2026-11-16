//go:build js && wasm

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	mitosistool "github.com/OrdalieTech/orb/agent/mitosis/tool"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

type mitosisRequest struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Connection string          `json:"connection"`
	URL        string          `json:"url"`
	Peer       string          `json:"peer"`
	Seed       string          `json:"seed"`
	AgentID    string          `json:"agentID"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
}

type browserMitosis struct {
	mu                        sync.Mutex
	conn                      *protocol.Conn
	connection, peer, agentID string
	cancel                    context.CancelFunc
	slots                     chan struct{}
}

func (b *browserMitosis) dispatch(raw string) string {
	var r mitosisRequest
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return err.Error()
	}
	if !protocol.ValidID(r.ID) {
		return "invalid Mitosis request ID"
	}
	select {
	case b.slots <- struct{}{}:
	default:
		return "too many Mitosis requests"
	}
	go func() {
		defer func() { <-b.slots }()
		result, err := b.execute(r)
		message := ""
		if err != nil {
			message = err.Error()
		}
		post(map[string]any{"type": "mitosis.result", "id": r.ID, "result": result, "error": message})
	}()
	return ""
}

func (b *browserMitosis) execute(r mitosisRequest) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch r.Type {
	case "mitosis.connect":
		seed, err := base64.RawStdEncoding.DecodeString(r.Seed)
		if err != nil || len(seed) != ed25519.SeedSize || !protocol.ValidID(r.AgentID) {
			return nil, errors.New("invalid browser Mitosis identity")
		}
		if _, err = mitosis.ParsePeerID(r.Peer); err != nil {
			return nil, err
		}
		if err = webtransport.ValidateURL(r.URL); err != nil {
			return nil, err
		}
		key := ed25519.NewKeyFromSeed(seed)
		id := protocol.NewID()
		b.mu.Lock()
		old := b.conn
		if b.cancel != nil {
			b.cancel()
		}
		b.conn = nil
		b.connection = id
		b.cancel = cancel
		b.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		stream, err := webtransport.Dial(ctx, r.URL)
		if err != nil {
			return nil, err
		}
		c, err := mitosis.ConnectClient(ctx, stream, key, r.Peer)
		if err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.connection != id {
			b.mu.Unlock()
			_ = c.Close()
			return nil, context.Canceled
		}
		b.conn = c
		b.peer = r.Peer
		b.agentID = r.AgentID
		b.cancel = nil
		b.mu.Unlock()
		go func() { <-c.Done(); post(map[string]string{"type": "mitosis.closed", "connection": id}) }()
		return map[string]string{"connection": id, "peer_id": mitosis.PeerID(key.Public().(ed25519.PublicKey)), "agent_id": r.AgentID}, nil
	case "mitosis.disconnect":
		b.mu.Lock()
		if r.Connection != "" && r.Connection != b.connection {
			b.mu.Unlock()
			return nil, mitosis.Fail("stale_target")
		}
		old := b.conn
		b.conn = nil
		b.connection = ""
		if b.cancel != nil {
			b.cancel()
			b.cancel = nil
		}
		b.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		return struct{}{}, nil
	case "mitosis.call":
		b.mu.Lock()
		c, id := b.conn, b.connection
		b.mu.Unlock()
		if c == nil || r.Connection != id {
			return nil, mitosis.Fail("unavailable")
		}
		var result json.RawMessage
		err := c.Call(ctx, r.Method, r.Params, &result)
		return result, err
	default:
		return nil, errors.New("unknown Mitosis request")
	}
}

func (b *browserMitosis) tool() (engine.AgentTool, error) {
	b.mu.Lock()
	c, peer, agentID := b.conn, b.peer, b.agentID
	b.mu.Unlock()
	if c == nil {
		return nil, errors.New("connect a Mitosis before enabling agent calls")
	}
	return mitosistool.NewTool(func(ctx context.Context, target string, call mitosis.Call) (json.RawMessage, error) {
		if target != peer {
			return nil, mitosis.Fail("unauthorized")
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var result json.RawMessage
		err := c.Call(ctx, "instances.call", struct {
			mitosis.Call
			Subject mitosis.Subject `json:"subject"`
		}{call, mitosis.Subject{Kind: "instance", InstanceID: agentID}}, &result)
		return result, err
	})
}
