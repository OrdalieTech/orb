// Package peer makes a Worker object a Bridge peer (DECISIONS.md P11): a
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
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/connect"
	attach "github.com/OrdalieTech/orb/connect/agent"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/engine"
	orbhost "github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/plugins/bridge"
	bridgeagent "github.com/OrdalieTech/orb/plugins/bridge/agent"
)

// Documents in the object's Store, beside settings.json and out of the file
// tools' reach.
const (
	StateDocument    = "/bridge/state.json"
	instanceDocument = "/bridge/instance.json"
	ledgerDocument   = "/bridge/operations.json"
)

// StateKey is present once the object has a Bridge identity; the shim serves
// the unauthenticated stream route only then.
var StateKey = worker.DocumentKey(StateDocument)

// DocumentStore is a connect.Store over one Store document.
type DocumentStore struct{ Document orbhost.Document }

func (s DocumentStore) Load() ([]byte, error) { return s.Document.Read(context.Background()) }

func (s DocumentStore) Save(data []byte) error {
	if len(data) == 0 {
		return errors.New("peer: refusing to save an empty document")
	}
	return s.Document.Update(context.Background(), func([]byte) ([]byte, error) { return slices.Clone(data), nil })
}

type identity struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	Credential string `json:"credential"`
}

type Peer struct {
	bridge     *bridge.Bridge
	instanceID string
	attachment *attach.Attachment
}

// Open loads or creates the object's Bridge identity, enrolls the object's
// Orb once under alias, and attaches it for this runtime's lifetime.
func Open(instance *worker.Instance, alias string) (*Peer, error) {
	store := instance.Host.Store
	b, err := bridge.Open(DocumentStore{store.Document(StateDocument)}, true)
	if err != nil {
		return nil, err
	}
	enrolled := DocumentStore{store.Document(instanceDocument)}
	raw, err := enrolled.Load()
	if err != nil {
		return nil, err
	}
	var self identity
	if len(raw) == 0 {
		registration, credential, err := b.Enroll(Alias(alias), b.PersonalGroup())
		if err != nil {
			return nil, err
		}
		self = identity{1, registration.ID, credential}
		if err = enrolled.Save(connect.JSON(self)); err != nil {
			return nil, err
		}
	} else if err = protocol.Decode(raw, &self); err != nil || self.Version != 1 || !protocol.ValidID(self.InstanceID) {
		return nil, errors.New("peer: corrupt instance registration")
	}
	attachment, err := attach.Attach(context.Background(), host{instance}, attach.Options{
		InstanceID: self.InstanceID, Store: DocumentStore{store.Document(ledgerDocument)}, Authorize: b.Authorize,
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
	return &Peer{bridge: b, instanceID: self.InstanceID, attachment: attachment}, nil
}

// Alias turns an object name into a Bridge instance alias.
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

func (p *Peer) PeerID() string     { return p.bridge.PeerID() }
func (p *Peer) InstanceID() string { return p.instanceID }

// Serve authenticates an inbound stream as the TLS server and keeps the
// channel until either side closes it.
func (p *Peer) Serve(ctx context.Context, stream net.Conn) error {
	channel, _, err := p.bridge.Connect(ctx, stream, "", true)
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

// Call is the object's agent-initiated call: it needs this object's own
// instance grant for the destination, and a channel the destination opened.
func (p *Peer) Call(ctx context.Context, peer string, call connect.Call) (json.RawMessage, error) {
	subject, err := p.bridge.Outbound(p.instanceID, peer, call)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = p.remote(ctx, peer, "instances.call", struct {
		connect.Call
		Subject connect.Subject `json:"subject"`
	}{call, subject}, &result)
	return result, err
}

func (p *Peer) remote(ctx context.Context, peer, method string, params, result any) error {
	channel := p.bridge.Connection(peer)
	if channel == nil {
		return fmt.Errorf("peer %s has no open connection to this object: %w", peer, connect.Fail("unavailable"))
	}
	return channel.Call(ctx, method, params, result)
}

// FullGrant is the invitation grant `orb bridge pair invite` proposes: full
// control of every current and future instance.
func FullGrant(peer string) bridge.Grant {
	return bridge.Grant{Principal: connect.Principal{PeerID: peer, Subject: connect.Subject{Kind: "controller"}}, GroupID: "*", IncludeFuture: true,
		Permissions: []string{"instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.input.reply", "instance.cancel", "instance.session.manage"}}
}

// Admin mirrors the owner methods of `orb bridge`. invite advertises locator,
// the object's public stream URL. The object cannot dial, so join, publish and
// the service lifecycle are not offered; self reports the object's identity.
func (p *Peer) Admin(ctx context.Context, method string, params json.RawMessage, locator string) (json.RawMessage, error) {
	empty := len(params) == 0 || string(params) == "{}" || string(params) == "null"
	switch method {
	case "self":
		return connect.JSON(map[string]string{"peer_id": p.PeerID(), "instance_id": p.instanceID, "locator": locator}), nil
	case "invite":
		if !strings.HasPrefix(locator, "wss://") && !strings.HasPrefix(locator, "ws://") {
			return nil, errors.New("peer: an invitation needs the object's ws:// or wss:// URL")
		}
		if empty {
			params = connect.JSON(map[string]any{"grants": []bridge.Grant{FullGrant("")}})
		}
		raw, err := p.bridge.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invitation bridge.Invitation
		if err = json.Unmarshal(raw, &invitation); err != nil {
			return nil, err
		}
		invitation.Locator = locator
		return connect.JSON(invitation), nil
	case "trust":
		var request struct {
			PeerID string `json:"peer_id"`
		}
		if err := protocol.Decode(params, &request); err != nil {
			return nil, err
		}
		grant := FullGrant(request.PeerID)
		var status struct {
			Grants []bridge.Grant `json:"grants"`
		}
		raw, err := p.bridge.Admin(ctx, "status", connect.JSON(struct{}{}))
		if err == nil {
			err = json.Unmarshal(raw, &status)
		}
		if err != nil {
			return nil, err
		}
		for _, old := range status.Grants {
			if old.Principal == grant.Principal && old.GroupID == "*" && old.IncludeFuture && old.Destination == "" && slices.Equal(old.Permissions, grant.Permissions) {
				return connect.JSON(struct{}{}), nil
			}
		}
		return connect.JSON(struct{}{}), p.bridge.AddGrant(grant)
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
	case "join", "publish", "stop", "start", "connect-ssh":
		return nil, fmt.Errorf("peer: %s is not available on a Worker object; create the invitation here and join it from the other Orb", method)
	default:
		return p.bridge.Admin(ctx, method, params)
	}
}

// AgentCalls adds the bridge_call tool when the object's settings enable the
// bridge-agent-calls plugin, as on the native CLI. open returns the object's
// peer when the tool runs.
func AgentCalls(open func(context.Context) (*Peer, error)) worker.ToolsFunc {
	return func(settings *config.SettingsManager) []extensions.ToolDefinition {
		if settings == nil || !settings.GetPlugins()["bridge-agent-calls"] {
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
	tool, _ := bridgeagent.NewTool(func(ctx context.Context, peer string, call connect.Call) (json.RawMessage, error) {
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
