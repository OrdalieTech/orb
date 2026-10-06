// Command orb-agent is the team agent image's entrypoint: it sets an agent up
// from one file, /agent/agent.yaml, and runs it, `orb chat` as the agent's user
// and each sidecar its platforms declare as another. What it knows of a
// platform is what the platform's package declares (chat.Platform): its
// environment, how its section of the file maps onto it, and its sidecar.
//
//	orb-agent [orb chat arguments]   run the agent (as root, the image's entrypoint)
//	orb-agent check <file>           validate an agent file and print its plan
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OrdalieTech/orb/chat"
	// The platforms orb chat links; each registers itself.
	_ "github.com/OrdalieTech/orb/chat/buzz"
	_ "github.com/OrdalieTech/orb/chat/discord"
	_ "github.com/OrdalieTech/orb/chat/googlechat"
	_ "github.com/OrdalieTech/orb/chat/messenger"
	_ "github.com/OrdalieTech/orb/chat/slack"
	_ "github.com/OrdalieTech/orb/chat/teams"
	_ "github.com/OrdalieTech/orb/chat/telegram"
	_ "github.com/OrdalieTech/orb/chat/whatsapp"
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
	Memory    *bool                     `yaml:"memory"`
	Skills    []string                  `yaml:"skills"`
	MCP       map[string]map[string]any `yaml:"mcp"`
	Providers map[string]any            `yaml:"providers"`
	Browser   string                    `yaml:"browser"`
	Platforms map[string]map[string]any `yaml:"platforms"`
}

// layout is where the image keeps things; tests point it elsewhere.
type layout struct {
	home, config string
	// engines are the browser engines the image ships, by name.
	engines map[string]string
	skills  string
}

var image = layout{
	home: "/agent", config: "/agent/config", skills: "/usr/local/lib/agent-browser/skills",
	engines: map[string]string{"lightpanda": "/usr/local/bin/lightpanda", "chromium": "/usr/lib/chromium/chromium-headless-shell"},
}

// plan is what running an agent file takes: the files to write, the
// platforms to run and the environment their settings become.
type plan struct {
	files     map[string][]byte
	platforms []string
	env       map[string]string
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "check" {
		plan, err := load(os.Args[2], image)
		if err != nil {
			fail(err)
		}
		fmt.Printf("platforms: %s\n", strings.Join(plan.platforms, ", "))
		for _, name := range sortedKeys(plan.files) {
			fmt.Printf("writes %s\n", name)
		}
		for _, name := range sortedKeys(plan.env) {
			fmt.Printf("sets %s\n", name)
		}
		return
	}
	code, err := run(os.Args[1:])
	if err != nil {
		fail(err)
	}
	os.Exit(code)
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "orb-agent: "+err.Error())
	os.Exit(1)
}

var modelPattern = regexp.MustCompile(`^[^/\s]+/\S+$`)

// load reads and validates an agent file and plans what running it takes.
func load(path string, image layout) (plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return plan{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var file agentFile
	if err := decoder.Decode(&file); err != nil {
		return plan{}, fmt.Errorf("%s: %w", path, err)
	}
	if !modelPattern.MatchString(file.Model) {
		return plan{}, fmt.Errorf("model must be <provider>/<model>, got %q", file.Model)
	}
	if file.Thinking != "" && !slices.Contains([]string{"off", "minimal", "low", "medium", "high", "xhigh"}, file.Thinking) {
		return plan{}, fmt.Errorf("thinking must be off, minimal, low, medium, high or xhigh, got %q", file.Thinking)
	}
	if len(file.Platforms) == 0 {
		return plan{}, errors.New("platforms: name at least one")
	}
	result := plan{files: map[string][]byte{}, env: map[string]string{}}
	provider, model, _ := strings.Cut(file.Model, "/")
	settings := map[string]any{"defaultProvider": provider, "defaultModel": model, "plugins": map[string]any{"memory": file.Memory == nil || *file.Memory}}
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
	if file.Browser != "" {
		executable, ok := image.engines[file.Browser]
		if !ok {
			return plan{}, fmt.Errorf("browser must be lightpanda or chromium, got %q", file.Browser)
		}
		if _, err := os.Stat(executable); err != nil {
			return plan{}, fmt.Errorf("browser %s: this image has no %s; use the orb-agent image with that browser", file.Browser, executable)
		}
		// agent-browser reads ~/.agent-browser/config.json, so the agent's
		// tools need no extra environment. Logins persist in the volume:
		// Lightpanda's cookies and storage under ~/.agent-browser/sessions,
		// Chromium's whole profile.
		browser := map[string]any{"engine": "lightpanda", "executablePath": executable, "restore": "agent"}
		if file.Browser == "chromium" {
			// Chromium's sandbox needs user namespaces, which containers
			// deny; the container is the sandbox.
			browser = map[string]any{"engine": "chrome", "executablePath": executable, "profile": filepath.Join(image.home, "browser", "profile"), "args": "--no-sandbox"}
		}
		result.files[filepath.Join(image.home, ".agent-browser", "config.json")] = jsonFile(browser)
		skills = append(skills, image.skills)
	}
	if len(skills) > 0 {
		settings["skills"] = skills
	}
	result.files[filepath.Join(image.config, "settings.json")] = jsonFile(settings)
	if file.Persona != "" {
		result.files[filepath.Join(image.config, "AGENTS.md")] = []byte(file.Persona)
	}
	if file.MCP != nil {
		for name, server := range file.MCP {
			var config mcp.ServerConfig
			if err := roundTrip(server, &config); err != nil {
				return plan{}, fmt.Errorf("mcp.%s: %w", name, err)
			}
			if err := mcp.Validate(name, &config); err != nil {
				return plan{}, fmt.Errorf("mcp.%s: %w", name, err)
			}
		}
		result.files[filepath.Join(image.config, "mcp.json")] = jsonFile(map[string]any{"mcpServers": file.MCP})
	}
	if file.Providers != nil {
		result.files[filepath.Join(image.config, "models.json")] = jsonFile(map[string]any{"providers": file.Providers})
	}
	identity := chat.Identity{Name: file.Name, About: file.About, Avatar: file.Avatar}
	var allowed []string
	for _, name := range sortedKeys(file.Platforms) {
		platform, ok := chat.LookupPlatform(name)
		if !ok {
			return plan{}, fmt.Errorf("platforms: unknown platform %q (known: %s)", name, strings.Join(chat.PlatformNames(), ", "))
		}
		section := file.Platforms[name]
		if platform.Open != nil {
			// orb chat routes these platforms' messages; allow is its
			// sender allowlist, shared by all of them.
			ids, err := stringList(section["allow"])
			if err != nil {
				return plan{}, fmt.Errorf("platforms.%s.allow: %w", name, err)
			}
			allowed = append(allowed, ids...)
			delete(section, "allow")
		}
		if platform.Configure == nil {
			if len(section) > 0 {
				return plan{}, fmt.Errorf("platforms.%s: takes no settings besides allow, got %s", name, strings.Join(sortedKeys(section), ", "))
			}
		} else {
			env, err := platform.Configure(identity, section)
			if err != nil {
				return plan{}, fmt.Errorf("platforms.%s: %w", name, err)
			}
			for key, value := range env {
				if previous, ok := result.env[key]; ok && previous != value {
					return plan{}, fmt.Errorf("platforms: %s is set twice", key)
				}
				result.env[key] = value
			}
		}
		result.platforms = append(result.platforms, name)
	}
	if len(allowed) > 0 {
		result.env["ORB_CHAT_ALLOWED_SENDERS"] = strings.Join(allowed, ",")
	}
	return result, nil
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

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
