// Package peer makes a Worker object a Mitosis peer (DECISIONS.md P11): a
// persisted identity in the object's Store, the object's own Orb attached as
// its orb.instance/1 service, pinned-TLS streams over the object's
// WebSockets, and the owner's admin surface. The object only accepts
// streams: it reaches another peer over the connection that peer opened.
package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	attach "github.com/OrdalieTech/orb/agent/mitosis"
	mitosistool "github.com/OrdalieTech/orb/agent/mitosis/tool"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/engine"
	orbhost "github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

// Documents in the object's Store, beside settings.json and out of the file
// tools' reach.
const (
	StateDocument    = "/mitosis/state.json"
	instanceDocument = "/mitosis/instance.json"
	ledgerDocument   = "/mitosis/operations.json"
)

// StateKey is present once the object has a Mitosis identity; the shim serves
// the unauthenticated stream route only then.
var StateKey = worker.DocumentKey(StateDocument)

type identity struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	Credential string `json:"credential"`
}

type Peer struct {
	mitosis    *mitosis.Mitosis
	instanceID string
	attachment *attach.Attachment
	peers      *mitosis.Peers // set by Dial
}

// Memory is the memory plugin's journal in the object's Store, as the native agent directory keeps
// it, shared with the object's sync group.
func Memory(store orbhost.Store) memory.Store {
	return filestore.Journal{Document: store.Document(path.Join(worker.AgentDir, "memory", "memory.jsonl"))}
}

// Open loads or creates the object's Mitosis identity, enrolls the object's
// Orb once under alias, and attaches it for this runtime's lifetime.
func Open(instance *worker.Instance, alias string) (*Peer, error) {
	store := instance.Host.Store
	b, err := mitosis.Open(store.Document(StateDocument), true)
	if err != nil {
		return nil, err
	}
	b.SetClass("credentials", aiauth.NewDocumentStore(store.Document(path.Join(worker.AgentDir, "auth.json"))))
	b.SetClass("memory", memory.Shared{Store: Memory(store)})
	enrolled := store.Document(instanceDocument)
	raw, err := enrolled.Read(context.Background())
	if err != nil {
		return nil, err
	}
	var self identity
	if len(raw) == 0 {
		registration, credential, err := b.Enroll(Alias(alias))
		if err != nil {
			return nil, err
		}
		self = identity{1, registration.ID, credential}
		if err = document.Replace(context.Background(), enrolled, mitosis.JSON(self)); err != nil {
			return nil, err
		}
	} else if err = protocol.Decode(raw, &self); err != nil || self.Version != 1 || !protocol.ValidID(self.InstanceID) {
		return nil, errors.New("peer: corrupt instance registration")
	}
	attachment, err := attach.Attach(context.Background(), host{instance}, attach.Options{
		InstanceID: self.InstanceID, Store: store.Document(ledgerDocument), Authorize: b.Authorize,
	})
	if err != nil {
		return nil, err
	}
	generation, err := b.Attach(self.InstanceID, self.Credential, protocol.NewID(), attachment)
	if err == nil {
		err = attachment.SetGeneration(generation)
	}
	if err != nil {
		_ = attachment.Close()
		return nil, err
	}
	return &Peer{mitosis: b, instanceID: self.InstanceID, attachment: attachment}, nil
}

// Alias turns an object name into a Mitosis instance alias.
func Alias(name string) string {
	var out strings.Builder
	for _, c := range strings.ToLower(name) {
		if out.Len() == 48 {
			break
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out.WriteRune(c)
		} else {
			out.WriteByte('-')
		}
	}
	if out.Len() == 0 {
		return "worker"
	}
	return out.String()
}

func (p *Peer) PeerID() string     { return p.mitosis.PeerID() }
func (p *Peer) InstanceID() string { return p.instanceID }

// Serve authenticates an inbound stream as the TLS server and keeps the
// channel until either side closes it.
func (p *Peer) Serve(ctx context.Context, stream net.Conn) error {
	channel, _, err := p.mitosis.Connect(ctx, stream, "", true)
	if err != nil {
		return err
	}
	select {
	case <-channel.Done():
	case <-ctx.Done():
		_ = channel.Close()
	}
	return nil
}

// Close ends the peer: its channels and its attachment.
func (p *Peer) Close() error { return p.mitosis.Close() }

// Follow runs a sync round each time the records change (a peer published here) and every 15 s,
// as a native device does, so a peer that only accepts streams keeps up with its group over the
// ones its peers opened, and a memory saved or a login refreshed here still reaches the group.
func (p *Peer) Follow(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.mitosis.Changes():
		}
		_, _ = p.mitosis.Sync(ctx, nil)
	}
}

// Dial makes the peer reach others itself, as a browser tab does over WebSocket: dial opens a
// stream to a peer's locator, and the peer keeps channels to the peers it knows, exchanges records
// with its scopes and runs its sync group's rounds, as a native Mitosis does. It can join an
// invitation, though no one can dial it. Call it once, before any Admin call, instead of Follow.
func (p *Peer) Dial(ctx context.Context, dial func(ctx context.Context, peer, locator string) (net.Conn, error)) {
	p.peers = mitosis.NewPeers(ctx, p.mitosis)
	p.peers.Dial = dial
	go p.peers.Reconcile()
	go p.peers.Greet()
}

// Call is the object's agent-initiated call: it needs this agent's own
// grant for the destination, and a channel the destination opened.
func (p *Peer) Call(ctx context.Context, peer string, call mitosis.AgentCall) (json.RawMessage, error) {
	subject, err := p.mitosis.Outbound(p.instanceID, peer, call.Method)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = p.remote(ctx, peer, "agent.call", struct {
		mitosis.AgentCall
		Subject mitosis.Subject `json:"subject"`
	}{call, subject}, &result)
	return result, err
}

func (p *Peer) remote(ctx context.Context, peer, method string, params, result any) error {
	if p.peers != nil {
		raw, err := p.peers.Remote(ctx, peer, method, params)
		if err == nil && result != nil {
			err = json.Unmarshal(raw, result)
		}
		return err
	}
	channel := p.mitosis.Connection(peer)
	if channel == nil {
		return fmt.Errorf("peer %s has no open connection to this object: %w", peer, mitosis.Fail("unavailable"))
	}
	return channel.Call(ctx, method, params, result)
}

// FullGrant is the invitation grant `orb mitosis pair invite` proposes: full
// control of every current and future instance.
func FullGrant(peer string) mitosis.Grant {
	return mitosis.ConversationGrant(peer)
}

// Admin mirrors the owner methods of `orb mitosis`. invite advertises locator,
// the object's public stream URL. An object that cannot dial offers no join;
// publish and the service lifecycle are never offered; self reports the
// object's identity.
func (p *Peer) Admin(ctx context.Context, method string, params json.RawMessage, locator string) (json.RawMessage, error) {
	empty := len(params) == 0 || string(params) == "{}" || string(params) == "null"
	switch method {
	case "self":
		return mitosis.JSON(map[string]string{"peer_id": p.PeerID(), "instance_id": p.instanceID, "locator": locator}), nil
	case "invite":
		if !strings.HasPrefix(locator, "wss://") && !strings.HasPrefix(locator, "ws://") {
			return nil, errors.New("peer: an invitation needs the object's ws:// or wss:// URL")
		}
		if empty {
			params = mitosis.JSON(map[string]any{"grants": []mitosis.Grant{FullGrant("")}})
		}
		raw, err := p.mitosis.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invitation mitosis.Invitation
		if err = json.Unmarshal(raw, &invitation); err != nil {
			return nil, err
		}
		invitation.Locator = locator
		return mitosis.JSON(invitation), nil
	case "trust":
		var request struct {
			PeerID string `json:"peer_id"`
		}
		if err := protocol.Decode(params, &request); err != nil {
			return nil, err
		}
		grant := FullGrant(request.PeerID)
		var status struct {
			Grants []mitosis.Grant `json:"grants"`
		}
		raw, err := p.mitosis.Admin(ctx, "status", mitosis.JSON(struct{}{}))
		if err == nil {
			err = json.Unmarshal(raw, &status)
		}
		if err != nil {
			return nil, err
		}
		for _, old := range status.Grants {
			if old.Principal == grant.Principal && old.Destination == "" && slices.Equal(old.Permissions, grant.Permissions) {
				return mitosis.JSON(struct{}{}), nil
			}
		}
		if p.peers != nil {
			// Trusting the device it joined keeps its locator, so the peer dials it again.
			_, err = p.peers.Admin(ctx, "grant", mitosis.JSON(grant))
			return mitosis.JSON(struct{}{}), err
		}
		return mitosis.JSON(struct{}{}), p.mitosis.AddGrant(grant)
	case "remote":
		var request struct {
			PeerID string          `json:"peer_id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := protocol.Decode(params, &request); err != nil {
			return nil, err
		}
		var result json.RawMessage
		return result, p.remote(ctx, request.PeerID, request.Method, request.Params, &result)
	case "join", "sync", "block":
		if p.peers != nil {
			return p.peers.Admin(ctx, method, params)
		}
		if method == "join" {
			return nil, fmt.Errorf("peer: join is not available on a Worker object; create the invitation here and join it from the other Orb")
		}
		return p.mitosis.Admin(ctx, method, params)
	case "publish", "stop", "start", "ssh":
		return nil, fmt.Errorf("peer: %s is not available here; create the invitation here and join it from the other Orb", method)
	default:
		return p.mitosis.Admin(ctx, method, params)
	}
}

// AgentCalls adds the mitosis_call tool when the object's settings enable the
// mitosis-agent-calls plugin, as on the native CLI. open returns the object's
// peer when the tool runs.
func AgentCalls(open func(context.Context) (*Peer, error)) worker.ToolsFunc {
	return func(settings *config.SettingsManager) []extensions.ToolDefinition {
		if settings == nil || !settings.GetPlugins()["mitosis-agent-calls"] {
			return nil
		}
		return []extensions.ToolDefinition{callTool(open)}
	}
}

// host adapts the object to the non-owning attachment boundary.
type host struct{ instance *worker.Instance }

func (h host) Session() *agent.AgentSession                  { return h.instance.Session() }
func (h host) EnableControl() (*agent.SessionControl, error) { return h.instance.EnableControl() }
func (h host) ObserveSessions(observe func(*agent.AgentSession)) func() {
	return h.instance.ObserveSessions(observe)
}

func (h host) NewSession(ctx context.Context, options *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
	parent := ""
	if options != nil {
		parent = options.ParentSession
	}
	cancelled, err := h.instance.NewSessionContext(ctx, parent)
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

func (h host) SwitchSession(ctx context.Context, path string, _ *agent.AgentSessionRuntimeSwitchOptions) (extensions.SessionReplacementResult, error) {
	cancelled, err := h.instance.SwitchSessionContext(ctx, path)
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

func (h host) Fork(context.Context, string, *extensions.ForkOptions) (agent.AgentSessionRuntimeForkResult, error) {
	return agent.AgentSessionRuntimeForkResult{}, errors.New("peer: the Worker host does not fork sessions")
}

func callTool(open func(context.Context) (*Peer, error)) extensions.ToolDefinition {
	tool, _ := mitosistool.NewTool(func(ctx context.Context, peer string, call mitosis.AgentCall) (json.RawMessage, error) {
		self, err := open(ctx)
		if err != nil {
			return nil, err
		}
		return self.Call(ctx, peer, call)
	})
	spec := tool.Spec()
	return extensions.ToolDefinition{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters,
		Execute: func(ctx context.Context, id string, raw any, update engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			return tool.Execute(ctx, id, raw, update)
		},
	}
}
