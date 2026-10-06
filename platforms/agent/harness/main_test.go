package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLayout(t *testing.T) layout {
	root := t.TempDir()
	lightpanda := filepath.Join(root, "lightpanda")
	if err := os.WriteFile(lightpanda, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return layout{
		home: filepath.Join(root, "agent"), config: filepath.Join(root, "agent", "config"), skills: "/skills",
		engines: map[string]string{"lightpanda": lightpanda, "chromium": filepath.Join(root, "missing-chromium")},
	}
}

func agentFileAt(t *testing.T, content string) string {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// One file sets the agent up: Orb's settings, persona, MCP servers and
// browser, and each platform's settings in the environment it already reads.
func TestAgentFileRendersEveryConsumersSettings(t *testing.T) {
	image := testLayout(t)
	plan, err := load(agentFileAt(t, `
name: Sales
about: Answers the sales team
model: openai-codex/gpt-5.4
thinking: medium
persona: |
  You are the sales team's agent.
skills: [skills/revops]
browser: lightpanda
mcp:
  notion: {command: notion-mcp, args: [--stdio]}
platforms:
  buzz:
    respond_to: allowlist
    allow: [f59bcde6, 97303c06]
    subscribe: mentions
  telegram:
    allow: [7311893094]
`), image)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.platforms, " ") != "buzz telegram" {
		t.Fatalf("platforms = %v", plan.platforms)
	}
	var settings map[string]any
	if err := json.Unmarshal(plan.files[filepath.Join(image.config, "settings.json")], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["defaultProvider"] != "openai-codex" || settings["defaultModel"] != "gpt-5.4" || settings["defaultThinkingLevel"] != "medium" ||
		settings["plugins"].(map[string]any)["memory"] != true {
		t.Fatalf("settings = %v", settings)
	}
	skills := settings["skills"].([]any)
	if len(skills) != 2 || skills[0] != filepath.Join(image.home, "skills/revops") || skills[1] != "/skills" {
		t.Fatalf("skills = %v", skills)
	}
	if string(plan.files[filepath.Join(image.config, "AGENTS.md")]) != "You are the sales team's agent.\n" {
		t.Fatalf("persona = %q", plan.files[filepath.Join(image.config, "AGENTS.md")])
	}
	if !strings.Contains(string(plan.files[filepath.Join(image.config, "mcp.json")]), `"notion-mcp"`) {
		t.Fatalf("mcp.json = %s", plan.files[filepath.Join(image.config, "mcp.json")])
	}
	var browser map[string]any
	if err := json.Unmarshal(plan.files[filepath.Join(image.home, ".agent-browser", "config.json")], &browser); err != nil {
		t.Fatal(err)
	}
	if browser["engine"] != "lightpanda" || browser["executablePath"] != image.engines["lightpanda"] || browser["restore"] != "agent" {
		t.Fatalf("browser = %v", browser)
	}
	for name, value := range map[string]string{
		"BUZZ_ACP_DISPLAY_NAME": "Sales", "ORB_BUZZ_ABOUT": "Answers the sales team",
		"BUZZ_ACP_RESPOND_TO": "allowlist", "BUZZ_ACP_RESPOND_TO_ALLOWLIST": "f59bcde6,97303c06", "BUZZ_ACP_SUBSCRIBE": "mentions",
		"ORB_CHAT_ALLOWED_SENDERS": "7311893094",
	} {
		if plan.env[name] != value {
			t.Errorf("%s = %q, want %q", name, plan.env[name], value)
		}
	}
}

// A mistake stops the agent at start, saying what is wrong, instead of
// running it with a setting silently ignored.
func TestAgentFileMistakesAreRefused(t *testing.T) {
	image := testLayout(t)
	for want, content := range map[string]string{
		"field modle not found":            "modle: x/y\nplatforms: {telegram: {}}\n",
		"model must be <provider>/<model>": "model: gpt-5.4\nplatforms: {telegram: {}}\n",
		`unknown platform "telgram"`:       "model: a/b\nplatforms: {telgram: {}}\n",
		`buzz: unknown setting "respond"`:  "model: a/b\nplatforms: {buzz: {respond: anyone}}\n",
		"takes no settings besides allow":  "model: a/b\nplatforms: {telegram: {token: x}}\n",
		"this image has no":                "model: a/b\nbrowser: chromium\nplatforms: {telegram: {}}\n",
		"mcp.broken":                       "model: a/b\nmcp: {broken: {}}\nplatforms: {telegram: {}}\n",
	} {
		if _, err := load(agentFileAt(t, content), image); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", content, err, want)
		}
	}
}
