package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
)

// defaultToolsSession builds a session over a settings file carrying
// `defaultTools` (upstream default-tools-setting.test.ts).
func defaultToolsSession(t *testing.T, defaultTools string, options AgentSessionOptions) *SessionRuntime {
	t.Helper()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"defaultTools":`+defaultTools+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	provider := testFaux(100000)
	options.AgentDir, options.CWD = agentDir, t.TempDir()
	options.StreamFn, options.Model = provider.StreamSimple, provider.GetModel()
	result, err := NewAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func TestDefaultToolsSettingSeedsBuiltInSelection(t *testing.T) {
	session := defaultToolsSession(t, `["grep","find"]`, AgentSessionOptions{})
	if got := session.GetActiveToolNames(); !slices.Equal(got, []string{"grep", "find"}) {
		t.Fatalf("active tools = %#v, want [grep find]", got)
	}
	if prompt := session.State().SystemPrompt; !strings.Contains(prompt, "- grep:") || strings.Contains(prompt, "- read:") {
		t.Fatalf("system prompt does not follow the configured selection: %q", prompt)
	}
}

func TestDefaultToolsSettingKeepsExtensionAndCustomTools(t *testing.T) {
	registry := extensions.NewRegistry(".")
	if err := registry.Register("<test:static>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{Name: "static_tool", Description: "extension tool"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session := defaultToolsSession(t, `["grep"]`, AgentSessionOptions{
		ExtensionRegistry: registry,
		CustomTools:       []extensions.ToolDefinition{{Name: "sdk_tool", Description: "SDK custom tool"}},
	})
	got := slices.Sorted(slices.Values(session.GetActiveToolNames()))
	if !slices.Equal(got, []string{"grep", "sdk_tool", "static_tool"}) {
		t.Fatalf("active tools = %#v, want [grep sdk_tool static_tool]", got)
	}
}

func TestDefaultToolsSettingYieldsToExplicitToolOptions(t *testing.T) {
	for _, test := range []struct {
		name, setting string
		options       AgentSessionOptions
		want          []string
	}{
		{"allowlist", `["grep"]`, AgentSessionOptions{Tools: []string{"read"}}, []string{"read"}},
		{"exclude", `["read","grep"]`, AgentSessionOptions{ExcludeTools: []string{"read"}}, []string{"grep"}},
		{"no-tools", `["read"]`, AgentSessionOptions{NoTools: "all"}, nil},
		{"unset", `null`, AgentSessionOptions{}, DefaultActiveToolNames},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := defaultToolsSession(t, test.setting, test.options).GetActiveToolNames()
			if len(got) != len(test.want) || !slices.Equal(got, test.want) {
				t.Fatalf("active tools = %#v, want %#v", got, test.want)
			}
		})
	}
}
