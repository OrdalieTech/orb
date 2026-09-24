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

	bridgetool "github.com/OrdalieTech/orb/agent/bridge/tool"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/engine"
	webtransport "github.com/OrdalieTech/orb/platforms/websocket"
)

type bridgeRequest struct {
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

type browserBridge struct {
	mu                        sync.Mutex
	conn                      *protocol.Conn
	connection, peer, agentID string
	cancel                    context.CancelFunc
	slots                     chan struct{}
}

func (b *browserBridge) dispatch(raw string) string {
	var r bridgeRequest
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return err.Error()
	}
	if !protocol.ValidID(r.ID) {
		return "invalid Bridge request ID"
	}
	select {
	case b.slots <- struct{}{}:
	default:
		return "too many Bridge requests"
	}
	go func() {
		defer func() { <-b.slots }()
		result, err := b.execute(r)
		message := ""
		if err != nil {
			message = err.Error()
		}
		post(map[string]any{"type": "bridge.result", "id": r.ID, "result": result, "error": message})
	}()
	return ""
}

func (b *browserBridge) execute(r bridgeRequest) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch r.Type {
	case "bridge.connect":
		seed, err := base64.RawStdEncoding.DecodeString(r.Seed)
		if err != nil || len(seed) != ed25519.SeedSize || !protocol.ValidID(r.AgentID) {
			return nil, errors.New("invalid browser Bridge identity")
		}
		if _, err = bridge.ParsePeerID(r.Peer); err != nil {
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
		c, err := bridge.ConnectClient(ctx, stream, key, r.Peer)
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
		go func() { <-c.Done(); post(map[string]string{"type": "bridge.closed", "connection": id}) }()
		return map[string]string{"connection": id, "peer_id": bridge.PeerID(key.Public().(ed25519.PublicKey)), "agent_id": r.AgentID}, nil
	case "bridge.disconnect":
		b.mu.Lock()
		if r.Connection != "" && r.Connection != b.connection {
			b.mu.Unlock()
			return nil, bridge.Fail("stale_target")
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
	case "bridge.call":
		b.mu.Lock()
		c, id := b.conn, b.connection
		b.mu.Unlock()
		if c == nil || r.Connection != id {
			return nil, bridge.Fail("unavailable")
		}
		var result json.RawMessage
		err := c.Call(ctx, r.Method, r.Params, &result)
		return result, err
	default:
		return nil, errors.New("unknown Bridge request")
	}
}

func (b *browserBridge) tool() (engine.AgentTool, error) {
	b.mu.Lock()
	c, peer, agentID := b.conn, b.peer, b.agentID
	b.mu.Unlock()
	if c == nil {
		return nil, errors.New("connect a Bridge before enabling agent calls")
	}
	return bridgetool.NewTool(func(ctx context.Context, target string, call bridge.Call) (json.RawMessage, error) {
		if target != peer {
			return nil, bridge.Fail("unauthorized")
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var result json.RawMessage
		err := c.Call(ctx, "instances.call", struct {
			bridge.Call
			Subject bridge.Subject `json:"subject"`
		}{call, bridge.Subject{Kind: "instance", InstanceID: agentID}}, &result)
		return result, err
	})
}
