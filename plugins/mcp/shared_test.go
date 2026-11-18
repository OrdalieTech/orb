package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
)

func TestSharedServersShareOnlyWhatCannotRunOrLeak(t *testing.T) {
	ctx := context.Background()
	servers := &document.Memory{}
	_ = document.Replace(ctx, servers, []byte(`{"mcpServers": {
		"docs": {"url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer literal"}},
		"stdio": {"command": "npx", "args": ["server"]},
		"env": {"url": "https://mcp.example.com/a", "headers": {"Authorization": "Bearer ${TOKEN}"}},
		"cmd": {"url": "https://mcp.example.com/b", "oauth": {"clientSecret": "!pass mcp"}},
		"plain": {"url": "http://mcp.example.com/c"},
		"local": {"url": "https://localhost:8443/mcp"},
		"lan": {"url": "https://192.168.1.4/mcp"},
		"mdns": {"url": "https://nas.local/mcp"},
		"login": {"url": "https://mcp.example.com/d", "auth": {"provider": "github-copilot"}},
		"meta": {"url": "https://mcp.example.com/e", "oauth": {"authServerMetadataUrl": "http://127.0.0.1:9/meta"}},
		"zone": {"url": "https://[::ffff:127.0.0.1%25en0]/mcp"},
		"wide": {"url": "https://nas\u3002local/mcp"}
	}}`))
	logins := &document.Memory{}
	docs, other := "mcp__docs|https://mcp.example.com/mcp", "mcp__env|https://mcp.example.com/a"
	_ = document.Replace(ctx, logins, []byte(`{
		"`+docs+`": {"serverUrl": "https://mcp.example.com/mcp", "tokens": {"access_token": "a", "token_type": "Bearer", "refresh_token": "r"}, "tokensExpireAt": 100, "codeVerifier": "v", "oauthState": "s", "discovery": {"authorizationServerUrl": "https://auth.example.com"}},
		"`+other+`": {"serverUrl": "https://mcp.example.com/a", "tokens": {"access_token": "b", "token_type": "Bearer"}}
	}`))
	if none, err := (SharedLogins{&document.Memory{}, &document.Memory{}}).Shareable(ctx); err != nil || len(none) != 0 {
		t.Fatalf("a device without mcp.json shares %v, %v", none, err)
	}
	torn := &document.Memory{}
	_ = document.Replace(ctx, torn, []byte(`{"mcpServers": {"docs": {"url": "https://mcp.example.com/mcp"}`))
	if _, err := (SharedServers{torn}).Shareable(ctx); err == nil {
		t.Fatal("a file cut short mid-write read as servers removed")
	}
	shared, err := SharedServers{servers}.Shareable(ctx)
	if err != nil || len(shared) != 1 || shared["docs"] == nil {
		t.Fatalf("servers = %v, %v; want docs alone", slices.Collect(maps.Keys(shared)), err)
	}
	signIns, err := SharedLogins{servers, logins}.Shareable(ctx)
	if err != nil || len(signIns) != 1 || strings.Contains(string(signIns[docs]), `"v"`) || strings.Contains(string(signIns[docs]), "oauthState") || strings.Contains(string(signIns[docs]), "auth.example.com") {
		t.Fatalf("logins = %s, %v; want docs' grant alone", signIns[docs], err)
	}
}

func TestSharedMergeKeepsLocalServersAndFresherGrants(t *testing.T) {
	ctx := context.Background()
	servers := &document.Memory{}
	_ = document.Replace(ctx, servers, []byte(`{"mcpServers": {"tools": {"command": "./tools"},
		"mine": {"url": "https://mcp.example.com/mine", "oauth": {"clientId": "c", "clientSecret": "${MINE}"}}}}`))
	docs := json.RawMessage(`{"url": "https://mcp.example.com/mcp"}`)
	changed, err := SharedServers{servers}.MergeShared(ctx, []map[string]json.RawMessage{{"docs": docs, "tools": docs, "evil": json.RawMessage(`{"command": "sh"}`)}}, false, []string{"tools"})
	data, _ := servers.Read(ctx)
	if err != nil || !slices.Equal(changed, []string{"docs"}) || !strings.Contains(string(data), `"./tools"`) || strings.Contains(string(data), `"sh"`) {
		t.Fatalf("merge changed %v (%v):\n%s\nwant docs added, the local stdio server neither replaced nor dropped, the shared command refused", changed, err, data)
	}

	key := "mcp__docs|https://mcp.example.com/mcp"
	grant := func(access string, expires int64, tokens bool) map[string]json.RawMessage {
		state := map[string]any{"serverUrl": "https://mcp.example.com/mcp", "tokensExpireAt": expires}
		if tokens {
			state["tokens"] = map[string]string{"access_token": access, "token_type": "Bearer", "refresh_token": "r-" + access}
		}
		raw, _ := json.Marshal(state)
		return map[string]json.RawMessage{key: raw}
	}
	logins := &document.Memory{}
	_ = document.Replace(ctx, logins, []byte(`null`))
	class := SharedLogins{servers, logins}
	mine := "mcp__mine|https://mcp.example.com/mine"
	injected := map[string]json.RawMessage{
		mine: json.RawMessage(`{"serverUrl": "https://mcp.example.com/mine", "tokensExpireAt": 9, "tokens": {"access_token": "x", "token_type": "Bearer"}}`),
		key:  json.RawMessage(`{"serverUrl": "https://mcp.example.com/mcp", "tokensExpireAt": 1, "tokens": {"access_token": "x", "token_type": "Bearer"}, "discovery": {"authorizationServerUrl": "https://evil.example.com"}}`),
	}
	if _, err := class.MergeShared(ctx, []map[string]json.RawMessage{injected}, false, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := logins.Read(ctx); strings.Contains(string(data), "mine") || strings.Contains(string(data), "evil") {
		t.Fatalf("took a grant for a server of this device's own, or the group's endpoints:\n%s", data)
	}
	held := func() string {
		data, _ := logins.Read(ctx)
		states, _ := parseStates(data)
		if states[key] == nil || states[key].Tokens == nil {
			return "none"
		}
		return states[key].Tokens.AccessToken
	}
	steps := []struct {
		in      map[string]json.RawMessage
		keepOwn bool
		want    string
	}{
		{grant("first", 100, true), false, "first"},    // over the injected one, which expires sooner
		{grant("stale", 50, true), false, "first"},     // an older copy never replaces a refresh
		{grant("fresh", 200, true), true, "fresh"},     // an owner takes a member's refresh
		{grant("same", 200, true), true, "fresh"},      // nor, keeping its own, an equal one
		{grant("", 100, false), false, "fresh"},        // a rejection of the old grant leaves the refreshed one
		{grant("", 200, false), false, "none"},         // the group's rejection of this grant drops it
		{grant("relogin", 300, true), true, "relogin"}, // and a new sign-in replaces the rejection
	}
	for index, step := range steps {
		if _, err := class.MergeShared(ctx, []map[string]json.RawMessage{step.in}, step.keepOwn, nil); err != nil || held() != step.want {
			t.Fatalf("step %d: holds %q (%v), want %q", index, held(), err, step.want)
		}
	}
	if _, err := class.MergeShared(ctx, nil, false, []string{key}); err != nil || held() != "none" {
		t.Fatalf("removed grant still held: %q (%v)", held(), err)
	}
}
