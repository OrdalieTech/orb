package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const defaultConnectTimeoutMS = 10_000

// ServerConfig describes one settings-declared MCP server. Exactly one of
// Command and URL is set after ParseSettings succeeds.
type ServerConfig struct {
	Name       string
	Command    string
	Args       []string
	Env        map[string]string
	CWD        string
	URL        string
	Headers    map[string]string
	TimeoutMS  int
	MaxRetries *int
}

type rawServerConfig struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	CWD        string            `json:"cwd"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	Enabled    *bool             `json:"enabled"`
	TimeoutMS  int               `json:"timeoutMs"`
	MaxRetries *int              `json:"maxRetries"`
}

// ParseSettings reads the documented top-level mcpServers object. Unknown
// settings and unknown per-server fields are retained by the settings manager
// but ignored here so newer configurations remain forward-compatible.
func ParseSettings(settings map[string]any) ([]ServerConfig, error) {
	value, exists := settings["mcpServers"]
	if !exists || value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("mcpServers: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var entries map[string]json.RawMessage
	if err := decoder.Decode(&entries); err != nil || entries == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return nil, fmt.Errorf("mcpServers: %w", err)
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	servers := make([]ServerConfig, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("mcpServers: server name must not be empty")
		}
		var raw rawServerConfig
		if err := json.Unmarshal(entries[name], &raw); err != nil {
			return nil, fmt.Errorf("mcpServers.%s: %w", name, err)
		}
		if raw.Enabled != nil && !*raw.Enabled {
			continue
		}
		server := ServerConfig{
			Name: name, Command: raw.Command, Args: append([]string(nil), raw.Args...), Env: cloneStrings(raw.Env),
			CWD: raw.CWD, URL: raw.URL, Headers: cloneStrings(raw.Headers), TimeoutMS: raw.TimeoutMS, MaxRetries: raw.MaxRetries,
		}
		if server.TimeoutMS == 0 {
			server.TimeoutMS = defaultConnectTimeoutMS
		}
		if err := validateServer(server); err != nil {
			return nil, fmt.Errorf("mcpServers.%s: %w", name, err)
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func validateServer(server ServerConfig) error {
	stdio := strings.TrimSpace(server.Command) != ""
	http := strings.TrimSpace(server.URL) != ""
	if stdio == http {
		return fmt.Errorf("set exactly one of command or url")
	}
	if server.TimeoutMS < 0 {
		return fmt.Errorf("timeoutMs must be non-negative")
	}
	if server.MaxRetries != nil && *server.MaxRetries < -1 {
		return fmt.Errorf("maxRetries must be at least -1")
	}
	if stdio {
		if len(server.Headers) != 0 {
			return fmt.Errorf("headers require an HTTP url")
		}
		if server.MaxRetries != nil {
			return fmt.Errorf("maxRetries requires an HTTP url")
		}
		return nil
	}
	if len(server.Args) != 0 || len(server.Env) != 0 || server.CWD != "" {
		return fmt.Errorf("args, env, and cwd require a stdio command")
	}
	parsed, err := url.Parse(server.URL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("url must be an absolute http or https URL")
	}
	return nil
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
