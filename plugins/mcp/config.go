package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	configpkg "github.com/OrdalieTech/orb/agent/config"
)

// Servers are read from mcp.json in the agent directory and, for trusted
// projects, from <project>/.pi/mcp.json, in the mcpServers shape other MCP
// clients share, so their configurations copy over. Project entries replace
// global entries of the same name.
//
//	{
//	  "mcpServers": {
//	    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] },
//	    "docs": { "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer ${DOCS_TOKEN}" } },
//	    "sentry": { "url": "https://mcp.sentry.dev/mcp" }
//	  }
//	}
//
// HTTP servers without an Authorization header sign in with OAuth when they
// answer 401. "auth": {"provider": "<p>"} sends the token of an `orb login`
// provider instead; project files cannot use it, so a repository cannot pick
// where a credential goes.

// Exposure is how the model reaches a server's tools:
//   - codemode (default): Orb has no codemode, so these are reached as deferred.
//   - deferred: declared once tool_search loads them, then called directly.
//   - direct: declared like built-in tools.
//   - hidden: registered but unreachable.
type Exposure string

const (
	ExposureCodemode Exposure = "codemode"
	ExposureDeferred Exposure = "deferred"
	ExposureDirect   Exposure = "direct"
	ExposureHidden   Exposure = "hidden"
)

var exposures = []Exposure{ExposureCodemode, ExposureDeferred, ExposureDirect, ExposureHidden}

// OAuthConfig configures OAuth for servers without dynamic client registration.
type OAuthConfig struct {
	// ClientID is a pre-registered client; without it Orb registers one.
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret may reference ${NAME} or !command.
	ClientSecret string `json:"clientSecret,omitempty"`
	// CallbackPort fixes the loopback port; without CallbackURL the redirect
	// URI is http://127.0.0.1:<port>/callback.
	CallbackPort int `json:"callbackPort,omitempty"`
	// CallbackURL is the redirect URI registered for ClientID: http on
	// localhost, 127.0.0.1 or [::1]; without a port, CallbackPort or a free one.
	CallbackURL string `json:"callbackUrl,omitempty"`
	// Scope is the space-separated scopes to request; default the advertised ones.
	Scope string `json:"scope,omitempty"`
	// ClientName is sent with dynamic client registration. Default: orb.
	ClientName string `json:"clientName,omitempty"`
	// AuthServerMetadataURL replaces discovery through the server; trusted as
	// configured, https except on loopback hosts.
	AuthServerMetadataURL string `json:"authServerMetadataUrl,omitempty"`
}

// ProviderAuth sends the token of an `orb login` provider.
type ProviderAuth struct {
	Provider string `json:"provider"`
}

// ServerConfig is one entry of mcpServers.
type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// CWD resolves relative paths against the session's working directory.
	CWD     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   *OAuthConfig      `json:"oauth,omitempty"`
	Auth    *ProviderAuth     `json:"auth,omitempty"`
	// Exposure defaults to codemode.
	Exposure Exposure `json:"exposure,omitempty"`
	// ToolExposure overrides Exposure for single tools, by the name the server
	// offers or a pattern where * matches anything; an exact name wins, then
	// the first matching pattern.
	ToolExposure ToolExposures `json:"toolExposure,omitempty"`
	// Description says what the server offers: the mcp_servers prompt section
	// lists it and tool search ranks the server's tools by it.
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	// Timeout is the per-request timeout in seconds; progress resets it. Default 60.
	Timeout float64 `json:"timeout,omitempty"`
}

// ToolExposures keeps toolExposure's entries in file order, which decides
// among patterns.
type ToolExposures []ToolExposureRule

type ToolExposureRule struct {
	Tool     string
	Exposure Exposure
}

func (rules *ToolExposures) UnmarshalJSON(data []byte) error {
	members, err := orderedMembers(data)
	if err != nil {
		return errors.New("toolExposure must map tool names to exposures")
	}
	*rules = ToolExposures{}
	for _, member := range members {
		var exposure Exposure
		if err := json.Unmarshal(member.value, &exposure); err != nil {
			return fmt.Errorf("toolExposure %q must be a string", member.name)
		}
		*rules = append(*rules, ToolExposureRule{Tool: member.name, Exposure: exposure})
	}
	return nil
}

func (rules ToolExposures) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, rule := range rules {
		if index > 0 {
			buffer.WriteByte(',')
		}
		name, _ := json.Marshal(rule.Tool)
		value, _ := json.Marshal(rule.Exposure)
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// ExposureOf is a server's exposure, the default codemode when unset.
func (config ServerConfig) ExposureOf() Exposure {
	if config.Exposure == "" {
		return ExposureCodemode
	}
	return config.Exposure
}

// ToolExposureOf is the exposure of one tool: its toolExposure entry, else the server's.
func (config ServerConfig) ToolExposureOf(tool string) Exposure {
	for _, rule := range config.ToolExposure {
		if rule.Tool == tool {
			return rule.Exposure
		}
	}
	for _, rule := range config.ToolExposure {
		if strings.Contains(rule.Tool, "*") && toolPattern(rule.Tool).MatchString(tool) {
			return rule.Exposure
		}
	}
	return config.ExposureOf()
}

// exposuresOf are the exposures a server's tools can have, known before it connects.
func (config ServerConfig) exposuresOf() []Exposure {
	result := []Exposure{config.ExposureOf()}
	for _, rule := range config.ToolExposure {
		if !slices.Contains(result, rule.Exposure) {
			result = append(result, rule.Exposure)
		}
	}
	return result
}

func toolPattern(pattern string) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for index, part := range parts {
		parts[index] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// IsHTTP reports a streamable HTTP server.
func (config ServerConfig) IsHTTP() bool { return config.URL != "" }

// IsEnabled reports a server that connects; enabled false keeps the entry.
func (config ServerConfig) IsEnabled() bool { return config.Enabled == nil || *config.Enabled }

// Entry is a configured server and where it came from.
type Entry struct {
	Name   string
	Config ServerConfig
	// Source is the mcp.json that defines the entry; Scope is "global" or "project".
	Source string
	Scope  string
}

// Namespace is the namespace of a server's tools: mcp__<server> with - as _.
func Namespace(server string) string {
	return "mcp__" + strings.ReplaceAll(server, "-", "_")
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var loopbackHosts = []string{"localhost", "127.0.0.1", "[::1]", "::1"}

func isLoopbackHost(host string) bool { return slices.Contains(loopbackHosts, host) }

// isLoopbackRedirect reports a redirect URI Orb's loopback callback can serve.
func isLoopbackRedirect(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) && parsed.RawQuery == "" && parsed.Fragment == ""
}

// Validate checks one server entry, aliases resolved, against the mcpServers shape.
func Validate(name string, config *ServerConfig) error {
	if !serverName.MatchString(name) {
		return fmt.Errorf(`invalid server name "%s" (use letters, digits, "_" and "-")`, name)
	}
	resolve := func(exposure Exposure) Exposure {
		if exposure == "codemode-deferred" {
			return ExposureCodemode
		}
		return exposure
	}
	config.Exposure = resolve(config.Exposure)
	list := `"codemode", "deferred", "direct", "hidden"`
	if config.Exposure != "" && !slices.Contains(exposures, config.Exposure) {
		return fmt.Errorf(`server "%s": exposure must be one of %s`, name, list)
	}
	for index, rule := range config.ToolExposure {
		config.ToolExposure[index].Exposure = resolve(rule.Exposure)
		if !slices.Contains(exposures, config.ToolExposure[index].Exposure) {
			return fmt.Errorf(`server "%s": toolExposure "%s" must be one of %s`, name, rule.Tool, list)
		}
	}
	if config.Timeout < 0 {
		return fmt.Errorf(`server "%s": timeout must be a positive number of seconds`, name)
	}
	if config.Type == "sse" {
		return fmt.Errorf(`server "%s": legacy SSE transport is not supported; use the streamable HTTP URL`, name)
	}
	if config.URL != "" && (config.Type == "" || config.Type == "http" || config.Type == "streamable-http") {
		parsed, err := url.Parse(config.URL)
		if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf(`server "%s": url must be an http or https URL`, name)
		}
		if err := validateOAuth(config.OAuth); err != nil {
			return fmt.Errorf(`server "%s": %w`, name, err)
		}
		if config.Auth != nil {
			if config.Auth.Provider == "" {
				return fmt.Errorf(`server "%s": auth.provider must be a provider name`, name)
			}
			if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
				return fmt.Errorf(`server "%s": auth requires an https URL, or http on localhost, 127.0.0.1, or [::1]`, name)
			}
		}
		return nil
	}
	if config.Command != "" && (config.Type == "" || config.Type == "stdio") {
		return nil
	}
	return fmt.Errorf(`server "%s" needs either "command" (stdio) or "url" (streamable HTTP)`, name)
}

func validateOAuth(oauth *OAuthConfig) error {
	if oauth == nil {
		return nil
	}
	if oauth.CallbackPort < 0 || oauth.CallbackPort > 65535 {
		return errors.New("oauth.callbackPort must be a port number")
	}
	if oauth.CallbackURL != "" {
		if !isLoopbackRedirect(oauth.CallbackURL) {
			return errors.New("oauth.callbackUrl must be an http URI on localhost, 127.0.0.1, or [::1] without query or fragment")
		}
		if port, _ := url.Parse(oauth.CallbackURL); port.Port() != "" && oauth.CallbackPort != 0 && port.Port() != fmt.Sprint(oauth.CallbackPort) {
			return errors.New("oauth.callbackUrl and oauth.callbackPort name different ports")
		}
	}
	if oauth.ClientName != "" && strings.TrimSpace(oauth.ClientName) == "" {
		return errors.New("oauth.clientName must be a non-empty string")
	}
	if oauth.AuthServerMetadataURL != "" {
		parsed, err := url.Parse(oauth.AuthServerMetadataURL)
		if err != nil || parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
			return errors.New("oauth.authServerMetadataUrl must be an https URL, or http on localhost, 127.0.0.1, or [::1]")
		}
	}
	return nil
}

// Load reads the global and, when trusted, the project mcp.json. Disabled
// servers are included so they can be enabled again; problems come back as
// messages while the valid entries still load.
func Load(agentDir, cwd string, projectTrusted bool) ([]Entry, []string) {
	var entries []Entry
	var problems []string
	read := func(path, scope string) {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return
		}
		servers, err := serverMembers(data)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return
		}
		for _, member := range servers {
			var config ServerConfig
			if err := json.Unmarshal(member.value, &config); err != nil {
				problems = append(problems, fmt.Sprintf(`%s: server "%s": %v`, path, member.name, err))
				continue
			}
			if err := Validate(member.name, &config); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			// Names that differ only in - and _ would share a namespace.
			if clash := slices.IndexFunc(entries, func(other Entry) bool {
				return other.Name != member.name && Namespace(other.Name) == Namespace(member.name)
			}); clash >= 0 {
				problems = append(problems, fmt.Sprintf(`%s: server "%s" conflicts with "%s"`, path, member.name, entries[clash].Name))
				continue
			}
			if scope == "project" && config.Auth != nil {
				problems = append(problems, fmt.Sprintf(`%s: server "%s": auth is only allowed in the global mcp.json`, path, member.name))
				continue
			}
			entry := Entry{Name: member.name, Config: config, Source: path, Scope: scope}
			if index := slices.IndexFunc(entries, func(other Entry) bool { return other.Name == member.name }); index >= 0 {
				entries[index] = entry
			} else {
				entries = append(entries, entry)
			}
		}
	}
	read(GlobalPath(agentDir), "global")
	if projectTrusted {
		read(ProjectPath(cwd), "project")
	}
	return entries, problems
}

// GlobalPath and ProjectPath are the mcp.json files.
func GlobalPath(agentDir string) string { return filepath.Join(agentDir, "mcp.json") }

func ProjectPath(cwd string) string { return filepath.Join(cwd, configpkg.ConfigDirName, "mcp.json") }

// AddServer adds or replaces a server in an mcp.json, creating the file, and
// reports whether it replaced one.
func AddServer(path, name string, config ServerConfig) (bool, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return false, err
	}
	replaced := false
	return replaced, editServers(path, func(servers []member) ([]member, bool) {
		index := slices.IndexFunc(servers, func(entry member) bool { return entry.name == name })
		if replaced = index >= 0; replaced {
			servers[index].value = encoded
		} else {
			servers = append(servers, member{name: name, value: encoded})
		}
		return servers, true
	})
}

// RemoveServer removes a server and reports whether the file defined it.
func RemoveServer(path, name string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	removed := false
	return removed, editServers(path, func(servers []member) ([]member, bool) {
		index := slices.IndexFunc(servers, func(entry member) bool { return entry.name == name })
		if removed = index >= 0; !removed {
			return servers, false
		}
		return slices.Delete(servers, index, index+1), true
	})
}

type member struct {
	name  string
	value json.RawMessage
}

// editServers reads an mcp.json (empty when missing), lets edit change its
// mcpServers and writes it back with its indentation; other content is kept.
func editServers(path string, edit func([]member) ([]member, bool)) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	root, err := orderedMembers(data)
	if err != nil {
		return fmt.Errorf("%s: expected an object with an \"mcpServers\" object", path)
	}
	servers, err := serverMembers(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	servers, changed := edit(servers)
	if !changed {
		return nil
	}
	encoded := encodeMembers(servers)
	if index := slices.IndexFunc(root, func(entry member) bool { return entry.name == "mcpServers" }); index >= 0 {
		root[index].value = encoded
	} else {
		root = append(root, member{name: "mcpServers", value: encoded})
	}
	indent := "  "
	if match := regexp.MustCompile(`(?m)^([ \t]+)\S`).FindSubmatch(data); match != nil {
		indent = string(match[1])
	}
	var output bytes.Buffer
	if err := json.Indent(&output, encodeMembers(root), "", indent); err != nil {
		return err
	}
	output.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0o600)
}

func serverMembers(data []byte) ([]member, error) {
	root, err := orderedMembers(data)
	if err != nil {
		return nil, errors.New(`expected an object with an "mcpServers" object`)
	}
	for _, entry := range root {
		if entry.name == "mcpServers" {
			servers, err := orderedMembers(entry.value)
			if err != nil {
				return nil, errors.New(`expected an object with an "mcpServers" object`)
			}
			return servers, nil
		}
	}
	return nil, nil
}

// orderedMembers decodes a JSON object's members in order.
func orderedMembers(data []byte) ([]member, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	var members []member
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{name: key.(string), value: value})
	}
	return members, nil
}

func encodeMembers(members []member) json.RawMessage {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, entry := range members {
		if index > 0 {
			buffer.WriteByte(',')
		}
		name, _ := json.Marshal(entry.name)
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(entry.value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes()
}
