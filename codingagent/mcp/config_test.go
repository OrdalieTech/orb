package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseSettingsFiltersDisabledServersAndSortsNames(t *testing.T) {
	settings := decodeSettings(t, `{
		"unrelated": true,
		"mcpServers": {
			"zeta": {"url": "https://example.test/mcp", "headers": {"Authorization": "Bearer token"}},
			"disabled": {"command": "ignored", "enabled": false},
			"alpha": {"command": "server", "args": ["--stdio"], "env": {"MODE": "test"}, "cwd": ".pi"}
		}
	}`)
	servers, err := ParseSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{servers[0].Name, servers[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("server order = %v", got)
	}
	if servers[0].TimeoutMS != defaultConnectTimeoutMS || servers[1].TimeoutMS != defaultConnectTimeoutMS {
		t.Fatalf("default timeouts = %d, %d", servers[0].TimeoutMS, servers[1].TimeoutMS)
	}
	if servers[0].Command != "server" || !reflect.DeepEqual(servers[0].Args, []string{"--stdio"}) || servers[0].Env["MODE"] != "test" {
		t.Fatalf("stdio server = %#v", servers[0])
	}
	if servers[1].URL != "https://example.test/mcp" || servers[1].Headers["Authorization"] != "Bearer token" {
		t.Fatalf("http server = %#v", servers[1])
	}
}

func TestParseSettingsValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		message string
	}{
		{"server not an object", `[]`, "cannot unmarshal array"},
		{"missing transport", `{}`, "set exactly one"},
		{"both transports", `{"command":"x","url":"https://example.test"}`, "set exactly one"},
		{"relative URL", `{"url":"/mcp"}`, "absolute http or https"},
		{"stdio headers", `{"command":"x","headers":{"X":"y"}}`, "headers require"},
		{"stdio retries", `{"command":"x","maxRetries":1}`, "maxRetries requires"},
		{"http args", `{"url":"https://example.test","args":["x"]}`, "require a stdio command"},
		{"negative timeout", `{"command":"x","timeoutMs":-1}`, "non-negative"},
		{"invalid retries", `{"url":"https://example.test","maxRetries":-2}`, "at least -1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := decodeSettings(t, `{"mcpServers":{"server":`+test.config+`}}`)
			_, err := ParseSettings(settings)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want substring %q", err, test.message)
			}
		})
	}
}

func TestParseSettingsRejectsNonObjectServerMap(t *testing.T) {
	_, err := ParseSettings(decodeSettings(t, `{"mcpServers":[]}`))
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal array") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseSettingsWithoutMCPDoesNoWork(t *testing.T) {
	for _, settings := range []map[string]any{nil, {}, {"mcpServers": nil}} {
		servers, err := ParseSettings(settings)
		if err != nil || len(servers) != 0 {
			t.Fatalf("servers = %#v, error = %v", servers, err)
		}
	}
}

func decodeSettings(t *testing.T, value string) map[string]any {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}
