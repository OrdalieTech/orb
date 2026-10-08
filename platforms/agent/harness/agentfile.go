package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OrdalieTech/orb/agent/assembly"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/platforms"
	"github.com/OrdalieTech/orb/plugins/mcp"
)

// agentFile is /agent/agent.yaml. Secrets never go in it: they stay in the
// container's environment (its env file).
type agentFile struct {
	Name      string                    `yaml:"name"`
	About     string                    `yaml:"about"`
	Avatar    string                    `yaml:"avatar"`
	Model     string                    `yaml:"model"`
	Thinking  string                    `yaml:"thinking"`
	Persona   string                    `yaml:"persona"`
	Plugins   map[string]bool           `yaml:"plugins"`
	Memory    *bool                     `yaml:"memory"` // plugins.memory, as files spelled it before plugins
	Skills    []string                  `yaml:"skills"`
	MCP       map[string]map[string]any `yaml:"mcp"`
	Providers map[string]any            `yaml:"providers"`
	Browser   string                    `yaml:"browser"`
	Platforms map[string]map[string]any `yaml:"platforms"`
}

// layout is where the image keeps things; tests point it elsewhere.
type layout struct {
	// workspace is where the agent and its sidecars run: a sidecar that is an
	// ACP client, such as buzz-acp, opens the agent's sessions in its own
	// working directory, which the agent's tools must be able to enter.
	home, config, workspace string
	// engines are the browser engines the image ships, by name, and skills
	// is agent-browser's skill directory.
	engines map[string]string
	skills  string
	// socket is the agent's ACP socket, and relay the command a sidecar runs
	// to reach it.
	socket string
	relay  []string
}

var image = layout{
	home: "/agent", config: "/agent/config", workspace: "/agent/workspace", skills: "/usr/local/lib/agent-browser/skills",
	engines: map[string]string{"lightpanda": "/usr/local/bin/lightpanda", "chromium": "/usr/lib/chromium/chromium-headless-shell"},
	socket:  "/run/orb/acp.sock", relay: []string{"/usr/bin/nc", "-N", "-U", "/run/orb/acp.sock"},
}

func (image layout) settings() string { return filepath.Join(image.config, "settings.json") }
func (image layout) persona() string  { return filepath.Join(image.config, "AGENTS.md") }
func (image layout) mcp() string      { return filepath.Join(image.config, "mcp.json") }
func (image layout) models() string   { return filepath.Join(image.config, "models.json") }
func (image layout) browser() string {
	return filepath.Join(image.home, ".agent-browser", "config.json")
}

// plan is what running an agent file takes: the files to write (nil removes
// one the file no longer asks for), the platforms to run and the environment
// their settings become.
type plan struct {
	files     map[string][]byte
	platforms []string
	env       map[string]string
}

var modelPattern = regexp.MustCompile(`^[^/\s]+/\S+$`)

// load reads and validates an agent file and plans what running it takes.
func load(path string, image layout) (plan, error) {
	file, err := parse(path)
	if err != nil {
		return plan{}, err
	}
	browser, err := browserConfig(file.Browser, image)
	if err != nil {
		return plan{}, err
	}
	mcpServers, err := mcpConfig(file.MCP)
	if err != nil {
		return plan{}, err
	}
	names, env, err := platformEnv(file)
	if err != nil {
		return plan{}, err
	}
	files := map[string][]byte{
		image.settings(): settingsFile(file, image, browser != nil),
		image.persona():  nil,
		image.mcp():      mcpServers,
		image.models():   nil,
		image.browser():  browser,
	}
	if file.Persona != "" {
		files[image.persona()] = []byte(file.Persona)
	}
	if file.Providers != nil {
		files[image.models()] = jsonFile(map[string]any{"providers": file.Providers})
	}
	return plan{files: files, platforms: names, env: env}, nil
}

// parse decodes an agent file strictly and checks its own fields.
func parse(path string) (agentFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentFile{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var file agentFile
	if err := decoder.Decode(&file); err != nil {
		return agentFile{}, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case !modelPattern.MatchString(file.Model):
		return agentFile{}, fmt.Errorf("model must be <provider>/<model>, got %q", file.Model)
	case file.Thinking != "" && !slices.Contains([]string{"off", "minimal", "low", "medium", "high", "xhigh"}, file.Thinking):
		return agentFile{}, fmt.Errorf("thinking must be off, minimal, low, medium, high or xhigh, got %q", file.Thinking)
	case len(file.Platforms) == 0:
		return agentFile{}, errors.New("platforms: name at least one")
	}
	for name := range file.Plugins {
		if !slices.Contains(assembly.Names(), name) {
			return agentFile{}, fmt.Errorf("plugins: unknown plugin %q (known: %s)", name, strings.Join(assembly.Names(), ", "))
		}
	}
	return file, nil
}

// settingsFile is Orb's settings.json: the model, the plugins (memory on unless
// turned off) and the skills, agent-browser's included when the agent has a
// browser.
func settingsFile(file agentFile, image layout, browser bool) []byte {
	provider, model, _ := strings.Cut(file.Model, "/")
	plugins := map[string]any{"memory": file.Memory == nil || *file.Memory}
	for name, on := range file.Plugins {
		plugins[name] = on
	}
	settings := map[string]any{"defaultProvider": provider, "defaultModel": model, "plugins": plugins}
	if file.Thinking != "" {
		settings["defaultThinkingLevel"] = file.Thinking
	}
	var skills []string
	for _, skill := range file.Skills {
		if !filepath.IsAbs(skill) {
			skill = filepath.Join(image.home, skill)
		}
		skills = append(skills, skill)
	}
	if browser {
		skills = append(skills, image.skills)
	}
	if len(skills) > 0 {
		settings["skills"] = skills
	}
	return jsonFile(settings)
}

// browserConfig is agent-browser's ~/.agent-browser/config.json, which its
// tools read with no extra environment, or nil without a browser. Logins
// persist in the volume: Lightpanda's cookies and storage under
// ~/.agent-browser/sessions, Chromium's whole profile.
func browserConfig(engine string, image layout) ([]byte, error) {
	if engine == "" {
		return nil, nil
	}
	executable, ok := image.engines[engine]
	if !ok {
		return nil, fmt.Errorf("browser must be lightpanda or chromium, got %q", engine)
	}
	if _, err := os.Stat(executable); err != nil {
		return nil, fmt.Errorf("browser %s: this image has no %s; use the orb-agent image with that browser", engine, executable)
	}
	if engine == "chromium" {
		// Chromium's sandbox needs user namespaces, which containers deny; the
		// container is the sandbox.
		return jsonFile(map[string]any{"engine": "chrome", "executablePath": executable, "profile": filepath.Join(image.home, "browser", "profile"), "args": "--no-sandbox"}), nil
	}
	return jsonFile(map[string]any{"engine": "lightpanda", "executablePath": executable, "restore": "agent"}), nil
}

// mcpConfig validates the MCP servers as Orb does and renders mcp.json, or
// nil without servers.
func mcpConfig(servers map[string]map[string]any) ([]byte, error) {
	if servers == nil {
		return nil, nil
	}
	for name, server := range servers {
		var config mcp.ServerConfig
		if err := roundTrip(server, &config); err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		if err := mcp.Validate(name, &config); err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
	}
	return jsonFile(map[string]any{"mcpServers": servers}), nil
}

// platformEnv maps each platform's section onto the environment it reads,
// as its catalog row declares; allow, for a platform orb chat routes, is orb
// chat's shared sender allowlist.
func platformEnv(file agentFile) ([]string, map[string]string, error) {
	identity := chat.Identity{Name: file.Name, About: file.About, Avatar: file.Avatar}
	names := slices.Sorted(maps.Keys(file.Platforms))
	env := map[string]string{}
	var allowed []string
	for _, name := range names {
		platform, ok := platforms.Lookup(name)
		if !ok {
			return nil, nil, fmt.Errorf("platforms: unknown platform %q (known: %s)", name, strings.Join(platforms.Names(), ", "))
		}
		section := maps.Clone(file.Platforms[name])
		if platform.Open != nil {
			ids, err := stringList(section["allow"])
			if err != nil {
				return nil, nil, fmt.Errorf("platforms.%s.allow: %w", name, err)
			}
			allowed = append(allowed, ids...)
			delete(section, "allow")
		}
		if platform.Configure == nil {
			if len(section) > 0 {
				return nil, nil, fmt.Errorf("platforms.%s: takes no settings besides allow, got %s", name, strings.Join(slices.Sorted(maps.Keys(section)), ", "))
			}
			continue
		}
		settings, err := platform.Configure(identity, section)
		if err != nil {
			return nil, nil, fmt.Errorf("platforms.%s: %w", name, err)
		}
		for key, value := range settings {
			if previous, ok := env[key]; ok && previous != value {
				return nil, nil, fmt.Errorf("platforms: %s is set twice", key)
			}
			env[key] = value
		}
	}
	if len(allowed) > 0 {
		env[platforms.AllowedSenders] = strings.Join(allowed, ",")
	}
	return names, env, nil
}

func jsonFile(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return append(data, '\n')
}

func roundTrip(from, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(to)
}

func stringList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("must be a list")
	}
	list := make([]string, len(items))
	for i, item := range items {
		list[i] = fmt.Sprint(item)
	}
	return list, nil
}
