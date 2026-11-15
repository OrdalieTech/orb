package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
		home: filepath.Join(root, "agent"), config: filepath.Join(root, "agent", "config"), workspace: filepath.Join(root, "agent", "workspace"), skills: "/skills",
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
plugins: {websearch: true, subagents: {models: [openai-codex/gpt-6-luna]}}
mcp:
  notion: {command: notion-mcp, args: [--stdio]}
platforms:
  buzz:
    respond_to: allowlist
    allow: [f59bcde6, 97303c06]
    channels: [4de8c05e]
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
	plugins := settings["plugins"].(map[string]any)
	if settings["defaultProvider"] != "openai-codex" || settings["defaultModel"] != "gpt-5.4" || settings["defaultThinkingLevel"] != "medium" ||
		plugins["memory"] != true || plugins["websearch"] != true || fmt.Sprint(plugins["subagents"]) != "map[models:[openai-codex/gpt-6-luna]]" {
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
		"BUZZ_NAME": "Sales", "BUZZ_ABOUT": "Answers the sales team", "BUZZ_RESPOND_TO": "allowlist", "BUZZ_CHANNELS": "4de8c05e",
		"ORB_CHAT_ALLOWED_SENDERS": "f59bcde6,97303c06,7311893094",
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
		"buzz: settings are respond_to":    "model: a/b\nplatforms: {buzz: {respond: anyone}}\n",
		"takes no settings besides allow":  "model: a/b\nplatforms: {telegram: {token: x}}\n",
		"this image has no":                "model: a/b\nbrowser: chromium\nplatforms: {telegram: {}}\n",
		"mcp.broken":                       "model: a/b\nmcp: {broken: {}}\nplatforms: {telegram: {}}\n",
		`unknown plugin "websearh"`:        "model: a/b\nplugins: {websearh: true}\nplatforms: {telegram: {}}\n",
		"plugins.websearch: true, false":   "model: a/b\nplugins: {websearch: yes}\nplatforms: {telegram: {}}\n",
		"entries must be provider/id":      "model: a/b\nplugins: {subagents: {models: [luna]}}\nplatforms: {telegram: {}}\n",
	} {
		if _, err := load(agentFileAt(t, content), image); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", content, err, want)
		}
	}
}

// The agent file is the configuration of record: a section it drops removes
// the file that section made.
func TestDroppedSectionsRemoveTheirFiles(t *testing.T) {
	image := testLayout(t)
	plan, err := load(agentFileAt(t, "model: a/b\nplatforms: {telegram: {}}\n"), image)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{image.persona(), image.mcp(), image.models(), image.browser()} {
		if data, managed := plan.files[path]; !managed || data != nil {
			t.Errorf("%s: managed %t, data %q; want removed", path, managed, data)
		}
	}
}

// Secrets reach the agent on its descriptor, never in its environment, and it
// serves its ACP socket.
func TestProcessesGetOnlyTheirOwnEnvironment(t *testing.T) {
	image := testLayout(t)
	image.socket = "/run/orb/acp.sock"
	environ := []string{"PATH=/bin", "HOME=/agent", "ORB_STATE_HOME=/agent/state", "OPENROUTER_API_KEY=sk-or", "BUZZ_PRIVATE_KEY=nostr"}
	rendered := map[string]string{"BUZZ_RESPOND_TO": "allowlist", "BUZZ_ABOUT": "Sales"}

	agent := agentProcess([]string{"buzz", "--tools"}, environ, rendered, image)
	if strings.Join(agent.argv, " ") != "orb chat buzz --tools" {
		t.Fatalf("argv = %v", agent.argv)
	}
	for _, entry := range agent.env {
		if strings.Contains(entry, "sk-or") || strings.Contains(entry, "nostr") || strings.Contains(entry, "allowlist") {
			t.Errorf("the agent's environment holds %s", entry)
		}
	}
	for _, want := range []string{"PATH=/bin", "ORB_STATE_HOME=/agent/state", "ORB_SECRETS_FD=3", "ORB_ACP_SOCKET=/run/orb/acp.sock"} {
		if !slices.Contains(agent.env, want) {
			t.Errorf("the agent's environment lacks %s: %v", want, agent.env)
		}
	}
	for _, want := range []string{"OPENROUTER_API_KEY=sk-or\n", "BUZZ_PRIVATE_KEY=nostr\n", "BUZZ_RESPOND_TO=allowlist\n", "BUZZ_ABOUT=Sales\n"} {
		if !strings.Contains(agent.secrets, want) {
			t.Errorf("the agent's descriptor lacks %q", want)
		}
	}

	if agent.dir != image.workspace {
		t.Errorf("the agent runs in %q, want the workspace %q", agent.dir, image.workspace)
	}
}
