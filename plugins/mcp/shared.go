package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"

	configpkg "github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/internal/document"
)

// A profile's remote MCP servers and their sign-ins reach its Mitosis sync group as two classes:
// "mcp", the servers of the global mcp.json, and "mcp-auth", mcp-auth.json's OAuth states of those
// servers. Only a server that makes no receiving device run a program or send its own secrets
// travels: https to a public host, every header and the client secret literal, no `orb login`
// provider (auth). A command, a ${NAME} or !command value, a loopback, private or local-network
// host or auth keeps a server and its sign-in on their device, where the group's never replace nor
// drop them. A sign-in travels as its grant alone: never the values of one under way (PKCE
// verifier, state) nor discovered endpoints, which each device finds from the server it holds.

// SharedServers is an mcp.json as the "mcp" class (mitosis.Class): its shareable servers by name.
type SharedServers struct{ Document document.Document }

func (s SharedServers) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	servers, err := readServers(ctx, s.Document)
	out := map[string]json.RawMessage{}
	for _, server := range servers {
		if value := shared(server); value != nil {
			out[server.name] = value
		}
	}
	return out, err
}

func (s SharedServers) Removed(ctx context.Context) (map[string]string, error) {
	data, err := s.Document.Read(ctx)
	if data = bytes.TrimPrefix(data, bom); err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, errors.New("not valid JSON")
	}
	root, err := orderedMembers(data)
	if err != nil {
		return nil, err
	}
	servers, err := serverMembers(data)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, server := range servers {
		live[server.name] = true
	}
	out := map[string]string{}
	for name, digest := range removedMembers(root) {
		if live[name] || digest == "" {
			continue
		}
		out[name] = digest
	}
	return out, nil
}

func (s SharedServers) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	err := s.Document.Update(ctx, func(data []byte) ([]byte, error) {
		changed = nil
		next, err := editServerData(data, func(servers []member, tombstones map[string]string) ([]member, bool) {
			wrote := false
			dropTombstone := func(name string) {
				if _, ok := tombstones[name]; ok {
					delete(tombstones, name)
					wrote = true
				}
			}
			for _, entries := range incoming {
				for name, raw := range entries {
					value, _, ok := shareableServer(name, raw)
					if !ok {
						continue
					}
					dropTombstone(name)
					index := slices.IndexFunc(servers, func(server member) bool { return server.name == name })
					if index >= 0 && (keepOwn || shared(servers[index]) == nil || bytes.Equal(shared(servers[index]), value)) {
						continue
					}
					if index >= 0 {
						servers[index].value = value
					} else {
						servers = append(servers, member{name: name, value: value})
					}
					changed = append(changed, name)
					wrote = true
				}
			}
			for _, name := range removed {
				if !keepOwn {
					dropTombstone(name)
				}
				index := slices.IndexFunc(servers, func(server member) bool { return server.name == name })
				if index >= 0 && !keepOwn && shared(servers[index]) != nil {
					servers = slices.Delete(servers, index, index+1)
					changed = append(changed, name)
					wrote = true
				}
			}
			return servers, wrote
		})
		if next == nil || err != nil {
			return data, err
		}
		return next, nil
	})
	return changed, err
}

// SharedLogins is an mcp-auth.json as the "mcp-auth" class (mitosis.Class): the OAuth grants of the
// servers Servers shares, by their mcp-auth.json key. A grant travels with its expiry, which orders
// its copies: the later wins, so a refresh on any device replaces the copies it made stale, and a
// rejected refresh, kept with its expiry (authorize), does not drop the copy another device just
// refreshed.
type SharedLogins struct{ Servers, Logins document.Document }

func (s SharedLogins) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	servers, err := SharedServers{s.Servers}.Shareable(ctx)
	if err != nil {
		return nil, err
	}
	data, err := s.Logins.Read(ctx)
	if err != nil {
		return nil, err
	}
	states, err := parseStates(data)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for name, raw := range servers {
		_, config, _ := shareableServer(name, raw)
		key, _ := credentialKeys(name, config.URL)
		if value, ok := shareableLogin(key, states[key]); ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s SharedLogins) Removed(ctx context.Context) (map[string]string, error) {
	servers, err := readServers(ctx, s.Servers)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, server := range servers {
		var config ServerConfig
		if shared(server) == nil && json.Unmarshal(server.value, &config) == nil && config.URL != "" {
			key, _ := credentialKeys(server.name, config.URL)
			own[key] = true
		}
	}
	data, err := s.Logins.Read(ctx)
	if err != nil {
		return nil, err
	}
	states, err := parseStates(data)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for key, state := range states {
		if own[key] || state == nil || state.TokensExpireAt != nil || state.Removed == "" {
			continue
		}
		out[key] = state.Removed
	}
	return out, nil
}

func (s SharedLogins) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	servers, err := readServers(ctx, s.Servers)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{} // the sign-ins of servers this device keeps to itself
	for _, server := range servers {
		var config ServerConfig
		if shared(server) == nil && json.Unmarshal(server.value, &config) == nil && config.URL != "" {
			key, _ := credentialKeys(server.name, config.URL)
			own[key] = true
		}
	}
	var changed []string
	err = s.Logins.Update(ctx, func(data []byte) ([]byte, error) {
		changed = nil
		wrote := false
		states, err := parseStates(data)
		if err != nil {
			return nil, err
		}
		for _, entries := range incoming {
			for key, raw := range entries {
				var next oauthState
				if own[key] || json.Unmarshal(raw, &next) != nil {
					continue
				}
				next.CodeVerifier, next.OAuthState, next.Discovery, next.Removed = "", "", nil, ""
				value, ok := shareableLogin(key, &next)
				have := states[key]
				same, held := shareableLogin(key, have)
				if !ok {
					continue
				}
				if have != nil && have.Removed != "" && !held {
					have.Removed = ""
					wrote = true
				}
				if held && (bytes.Equal(same, value) || expiry(&next) < expiry(have) || keepOwn && expiry(&next) <= expiry(have)) {
					continue
				}
				if have != nil {
					next.CodeVerifier, next.OAuthState, next.Discovery = have.CodeVerifier, have.OAuthState, have.Discovery
				}
				states[key] = &next
				changed = append(changed, key)
				wrote = true
			}
		}
		for _, key := range removed {
			if have, ok := states[key]; ok && !keepOwn && !own[key] {
				delete(states, key)
				wrote = true
				if have != nil && have.TokensExpireAt != nil {
					changed = append(changed, key)
				}
			}
		}
		if !wrote {
			return data, nil
		}
		encoded, err := json.MarshalIndent(states, "", "  ")
		return append(encoded, '\n'), err
	})
	return changed, err
}

// readServers is an mcp.json's servers, none when it is empty or missing.
func readServers(ctx context.Context, doc document.Document) ([]member, error) {
	data, err := doc.Read(ctx)
	if data = bytes.TrimPrefix(data, bom); err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, err
	}
	return serverMembers(data)
}

// shared is a server entry as it travels, nil when it stays on its device.
func shared(server member) json.RawMessage {
	value, _, _ := shareableServer(server.name, server.value)
	return value
}

// shareableServer is a server entry as it travels, and its configuration, or false when it stays.
func shareableServer(name string, raw json.RawMessage) (json.RawMessage, ServerConfig, bool) {
	var config ServerConfig
	if json.Unmarshal(raw, &config) != nil || Validate(name, &config) != nil || config.Command != "" || config.Auth != nil || !publicHTTPS(config.URL) {
		return nil, config, false
	}
	values := slices.Collect(maps.Values(config.Headers))
	if config.OAuth != nil {
		if config.OAuth.AuthServerMetadataURL != "" && !publicHTTPS(config.OAuth.AuthServerMetadataURL) {
			return nil, config, false
		}
		values = append(values, config.OAuth.ClientSecret)
	}
	for _, value := range values {
		if configpkg.IsCommandConfigValue(value) || len(configpkg.GetConfigValueEnvVarNames(value)) > 0 {
			return nil, config, false
		}
	}
	value, err := json.Marshal(config)
	return value, config, err == nil
}

// shareableLogin is a stored grant with its expiry as it travels, a rejected one included, held
// under the key of its server, a public https one.
func shareableLogin(key string, state *oauthState) (json.RawMessage, bool) {
	if state == nil || state.TokensExpireAt == nil {
		return nil, false
	}
	name, serverURL, _ := strings.Cut(strings.TrimPrefix(key, "mcp__"), "|")
	if key != Namespace(name)+"|"+serverURL || !serverName.MatchString(name) || serverURL != state.ServerURL || !publicHTTPS(serverURL) {
		return nil, false
	}
	shared := *state
	shared.CodeVerifier, shared.OAuthState, shared.Discovery, shared.Removed = "", "", nil, ""
	value, err := json.Marshal(shared)
	return value, err == nil
}

func expiry(state *oauthState) int64 { return *state.TokensExpireAt }

// sharedSpace is carrier-grade NAT's (RFC 6598), Tailscale's included: each network its own.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// publicHTTPS reports an https URL whose host is the same for every device: a global address, or
// an ASCII name that is not one of a local network (a resolver maps other forms, say a fullwidth
// dot, to names this check never saw).
func publicHTTPS(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		return addr.Zone() == "" && addr.IsGlobalUnicast() && !addr.IsPrivate() && !sharedSpace.Contains(addr)
	}
	if strings.IndexFunc(host, func(r rune) bool { return r > unicode.MaxASCII || r == ':' || r == '%' }) >= 0 || !strings.Contains(host, ".") {
		return false
	}
	return !slices.ContainsFunc([]string{".localhost", ".local", ".lan", ".home.arpa", ".internal"}, func(suffix string) bool { return strings.HasSuffix(host, suffix) })
}
