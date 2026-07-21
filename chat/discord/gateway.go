package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
	"github.com/OrdalieTech/pigo/chat/internal/wsclient"
)

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// GUILDS + GUILD_MESSAGES + DIRECT_MESSAGES + privileged MESSAGE_CONTENT.
const gatewayIntents = 1<<0 | 1<<9 | 1<<12 | 1<<15

const helloTimeout = 30 * time.Second

const maxReconnectDelay = time.Minute

var fatalCloseReasons = map[int]string{
	4004: "authentication failed — check the bot token",
	4010: "invalid shard",
	4011: "sharding required — this adapter runs a single shard",
	4012: "invalid gateway API version",
	4013: "invalid intents bitmask",
	4014: "disallowed intents — enable the Message Content Intent for the bot in the Discord Developer Portal (Bot settings), then reconnect",
}

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

type gatewayState struct {
	mu                 sync.Mutex
	seq                int64
	seqSet             bool
	sessionID          string
	resumeURL          string
	resumeDialFailures int
}

func (s *gatewayState) setSeq(seq int64) {
	s.mu.Lock()
	s.seq, s.seqSet = seq, true
	s.mu.Unlock()
}

func (s *gatewayState) lastSeq() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, s.seqSet
}

func (s *gatewayState) setSession(id, resumeURL string) {
	s.mu.Lock()
	s.sessionID, s.resumeURL = id, resumeURL
	s.resumeDialFailures = 0
	s.mu.Unlock()
}

func (s *gatewayState) session() (id, resumeURL string, seq int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID, s.resumeURL, s.seq, s.sessionID != "" && s.resumeURL != "" && s.seqSet
}

func (s *gatewayState) clearSession() {
	s.mu.Lock()
	s.sessionID, s.resumeURL, s.seqSet = "", "", false
	s.resumeDialFailures = 0
	s.mu.Unlock()
}

func (s *gatewayState) noteResumeDialFailure() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeDialFailures++
	if s.resumeDialFailures < 3 {
		return false
	}
	s.sessionID, s.resumeURL, s.seqSet = "", "", false
	s.resumeDialFailures = 0
	return true
}

func (s *gatewayState) resetResumeDialFailures() {
	s.mu.Lock()
	s.resumeDialFailures = 0
	s.mu.Unlock()
}

// Connection-local state prevents a retiring heartbeater from arming the
// next connection's watchdog.
type heartbeatState struct {
	mu          sync.Mutex
	awaitingAck bool
	sentAt      time.Time
}

func (s *heartbeatState) arm(now time.Time) {
	s.mu.Lock()
	s.awaitingAck, s.sentAt = true, now
	s.mu.Unlock()
}

func (s *heartbeatState) ack() {
	s.mu.Lock()
	s.awaitingAck = false
	s.mu.Unlock()
}

func (s *heartbeatState) pending(now time.Time, interval time.Duration) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.awaitingAck {
		return false, 0
	}
	return true, interval - now.Sub(s.sentAt)
}

// Run connects to the Discord gateway and pumps normalized MESSAGE_CREATE
// dispatches into publish until ctx ends or a fatal configuration error
// (bad token, disallowed intents) occurs. Connection losses reconnect with
// capped exponential backoff, resuming the previous session whenever the
// close code allows it — a fresh IDENTIFY is budgeted (1000/day), so resume
// is always tried first. Run one Run per bot account.
//
// publish must enqueue durably and return fast: it is the ingress
// publish-then-ack edge and must never wait on turn processing.
func (a *Adapter) Run(ctx context.Context, publish func(chat.Message) error) error {
	st := &gatewayState{}
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		established, err := a.gatewayCycle(ctx, st, publish)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if established {
			delay = time.Second
		}
		if err := a.sleep(ctx, delay); err != nil {
			return err
		}
		if delay *= 2; delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (a *Adapter) gatewayCycle(ctx context.Context, st *gatewayState, publish func(chat.Message) error) (established bool, err error) {
	sessionID, resumeURL, seq, resuming := st.session()
	rawURL := resumeURL
	if !resuming {
		gb, err := a.client.getGatewayBot(ctx)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
				return false, fmt.Errorf("discord: gateway discovery: %w — check the bot token", err)
			}
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			a.logger.Warn("discord: GET /gateway/bot failed", "error", err)
			return false, nil
		}
		if gb.SessionStartLimit.Remaining <= 0 {
			// Burning past the IDENTIFY budget invalidates the token.
			wait := time.Duration(gb.SessionStartLimit.ResetAfter) * time.Millisecond
			a.logger.Warn("discord: identify budget exhausted; waiting", "resetAfter", wait)
			if err := a.sleep(ctx, wait); err != nil {
				return false, err
			}
		}
		rawURL = gb.URL
	}

	wsURL, err := gatewayWSURL(rawURL)
	if err != nil {
		st.clearSession() // a bad stored resume URL must not loop forever
		a.logger.Warn("discord: bad gateway url", "error", err)
		return false, nil
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, a.gatewayDialTimeout)
	conn, err := wsclient.Dial(dialCtx, wsURL, nil, nil)
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if resuming && st.noteResumeDialFailure() {
			a.logger.Warn("discord: abandoning unreachable resume gateway after repeated dial failures")
		}
		a.logger.Warn("discord: gateway dial failed", "error", err)
		return false, nil
	}
	st.resetResumeDialFailures()
	defer func() { _ = conn.Close() }()

	// Hello must arrive promptly; a silent socket reconnects.
	helloCtx, cancelHello := context.WithTimeout(ctx, helloTimeout)
	hello, err := a.readHello(helloCtx, conn)
	cancelHello()
	if err != nil {
		return false, a.classifyGatewayError(st, err)
	}

	hb := &heartbeatState{}
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		a.heartbeatLoop(ctx, conn, st, hb, hello, stopHeartbeat)
	}()
	defer func() {
		close(stopHeartbeat)
		_ = conn.Close()
		<-heartbeatDone
	}()

	if resuming {
		err = writeGatewayJSON(conn, opResume, struct {
			Token     string `json:"token"`
			SessionID string `json:"session_id"`
			Seq       int64  `json:"seq"`
		}{Token: a.client.token, SessionID: sessionID, Seq: seq})
	} else {
		st.clearSession()
		err = writeGatewayJSON(conn, opIdentify, struct {
			Token      string `json:"token"`
			Intents    int    `json:"intents"`
			Properties struct {
				OS      string `json:"os"`
				Browser string `json:"browser"`
				Device  string `json:"device"`
			} `json:"properties"`
		}{
			Token:   a.client.token,
			Intents: gatewayIntents,
			Properties: struct {
				OS      string `json:"os"`
				Browser string `json:"browser"`
				Device  string `json:"device"`
			}{OS: runtime.GOOS, Browser: "pigo", Device: "pigo"},
		})
	}
	if err != nil {
		return false, a.classifyGatewayError(st, err)
	}

	for {
		opcode, data, err := conn.ReadMessage(ctx)
		if err != nil {
			return established, a.classifyGatewayError(st, err)
		}
		if opcode != wsclient.OpText {
			continue
		}
		var payload gatewayPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			a.logger.Warn("discord: undecodable gateway payload", "error", err)
			continue
		}
		switch payload.Op {
		case opDispatch:
			if payload.S != nil {
				st.setSeq(*payload.S)
			}
			if a.handleDispatch(st, payload, publish) {
				established = true
			}
		case opHeartbeat:
			// The server may request an immediate beat.
			if err := a.sendHeartbeat(conn, st, hb); err != nil {
				return established, a.classifyGatewayError(st, err)
			}
		case opHeartbeatACK:
			hb.ack()
		case opReconnect:
			// Close non-1000 so the session stays resumable, then cycle.
			_ = conn.WriteClose(4000, "reconnect requested")
			return established, nil
		case opInvalidSession:
			var canResume bool
			_ = json.Unmarshal(payload.D, &canResume)
			if !canResume {
				st.clearSession()
				// The docs mandate a 1–5s random wait before re-identifying.
				wait := time.Second + time.Duration(a.jitter()*float64(4*time.Second))
				if err := a.sleep(ctx, wait); err != nil {
					return established, err
				}
			}
			_ = conn.WriteClose(4000, "invalid session")
			return established, nil
		case opHello:
			// Duplicate hello; the heartbeater is already running.
		}
	}
}

func (a *Adapter) handleDispatch(st *gatewayState, payload gatewayPayload, publish func(chat.Message) error) bool {
	switch payload.T {
	case "READY":
		var ready struct {
			SessionID        string `json:"session_id"`
			ResumeGatewayURL string `json:"resume_gateway_url"`
			User             gwUser `json:"user"`
		}
		if err := json.Unmarshal(payload.D, &ready); err != nil {
			a.logger.Warn("discord: undecodable READY", "error", err)
			return false
		}
		st.setSession(ready.SessionID, ready.ResumeGatewayURL)
		a.setIdentity(ready.User.ID)
		a.logger.Info("discord: gateway ready", "sessionId", ready.SessionID)
		return true
	case "RESUMED":
		a.logger.Info("discord: gateway resumed")
		return true
	case "MESSAGE_CREATE":
		var msg gwMessage
		if err := json.Unmarshal(payload.D, &msg); err != nil {
			a.logger.Warn("discord: undecodable MESSAGE_CREATE", "error", err)
			return false
		}
		m, ok := a.normalize(&msg)
		if !ok {
			return false
		}
		if err := publish(m); err != nil {
			// ponytail: the gateway has no redelivery — a failed durable
			// enqueue drops the message; surface loudly and move on.
			a.logger.Error("discord: publish failed, message dropped", "eventId", m.EventID, "error", err)
		}
		return false
	}
	return false
}

func (a *Adapter) readHello(ctx context.Context, conn *wsclient.Conn) (time.Duration, error) {
	_, data, err := conn.ReadMessage(ctx)
	if err != nil {
		return 0, err
	}
	var payload gatewayPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0, fmt.Errorf("discord: decode hello: %w", err)
	}
	if payload.Op != opHello {
		return 0, fmt.Errorf("discord: expected hello, got op %d", payload.Op)
	}
	var hello struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(payload.D, &hello); err != nil {
		return 0, fmt.Errorf("discord: decode hello: %w", err)
	}
	if hello.HeartbeatInterval <= 0 {
		return 0, fmt.Errorf("discord: hello carries no heartbeat_interval")
	}
	return time.Duration(hello.HeartbeatInterval) * time.Millisecond, nil
}

func (a *Adapter) heartbeatLoop(ctx context.Context, conn *wsclient.Conn, st *gatewayState, hb *heartbeatState, interval time.Duration, stop <-chan struct{}) {
	timer := time.NewTimer(time.Duration(float64(interval) * a.jitter()))
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if pending, remaining := hb.pending(time.Now(), interval); pending {
			if remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			a.logger.Warn("discord: heartbeat ack missed; recycling connection")
			_ = conn.WriteClose(4000, "heartbeat ack timeout")
			_ = conn.Close()
			return
		}
		if err := a.sendHeartbeat(conn, st, hb); err != nil {
			return
		}
		timer.Reset(interval)
	}
}

func (a *Adapter) sendHeartbeat(conn *wsclient.Conn, st *gatewayState, hb *heartbeatState) error {
	payload := struct {
		Op int    `json:"op"`
		D  *int64 `json:"d"`
	}{Op: opHeartbeat}
	if seq, ok := st.lastSeq(); ok {
		payload.D = &seq
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: encode heartbeat: %w", err)
	}
	hb.arm(time.Now())
	if err := conn.WriteText(data); err != nil {
		hb.ack()
		return err
	}
	return nil
}

func (a *Adapter) classifyGatewayError(st *gatewayState, err error) error {
	var ce *wsclient.CloseError
	if errors.As(err, &ce) {
		if reason, ok := fatalCloseReasons[ce.Code]; ok {
			return fmt.Errorf("discord: gateway closed with code %d: %s", ce.Code, reason)
		}
		a.logger.Warn("discord: gateway connection closed", "code", ce.Code, "reason", ce.Reason)
		return nil
	}
	a.logger.Warn("discord: gateway connection lost", "error", err)
	return nil
}

func writeGatewayJSON(conn *wsclient.Conn, op int, d any) error {
	data, err := json.Marshal(struct {
		Op int `json:"op"`
		D  any `json:"d"`
	}{Op: op, D: d})
	if err != nil {
		return fmt.Errorf("discord: encode gateway payload: %w", err)
	}
	return conn.WriteText(data)
}

func gatewayWSURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("discord: parse gateway url: %w", err)
	}
	u.RawQuery = "v=10&encoding=json"
	return u.String(), nil
}
