package codingagent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func writeSkillFixture(t *testing.T, directory, name, description string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nbody\n", name, description)
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeResourceFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultResourceLoaderOverridesAndSDKReuse(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetProjectTrusted(true)
	customPrompt := "custom SDK prompt"
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
		NoSkills: true, NoPromptTemplates: true, NoContextFiles: true,
		AppendSystemPrompt: []string{},
		ExtensionFactories: []extensions.Factory{func(extensions.API) error { return nil }},
		SkillsOverride: func(ResourceSkillsResult) ResourceSkillsResult {
			return ResourceSkillsResult{Skills: []Skill{{Name: "inspect", Description: "Inspect", Content: "inspect carefully", FilePath: "/virtual/SKILL.md"}}}
		},
		PromptsOverride: func(ResourcePromptsResult) ResourcePromptsResult {
			return ResourcePromptsResult{Prompts: []PromptTemplate{{Name: "deploy", Content: "deploy now", FilePath: "/virtual/deploy.md"}}}
		},
		ThemesOverride: func(ResourceThemesResult) ResourceThemesResult {
			return ResourceThemesResult{Themes: []extensions.ThemeInfo{{Name: "sdk"}}}
		},
		AgentsFilesOverride: func(ResourceAgentsFilesResult) ResourceAgentsFilesResult {
			return ResourceAgentsFilesResult{AgentsFiles: []ContextFile{{Path: "/virtual/AGENTS.md", Content: "SDK context"}}}
		},
		SystemPromptOverride: func(*string) *string { return &customPrompt },
		AppendSystemPromptOverride: func(base []string) []string {
			return append(base, "SDK append")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	trustCalls := 0
	if err := loader.Reload(context.Background(), &ResourceLoaderReloadOptions{
		ResolveProjectTrust: func(_ context.Context, registry *extensions.Registry) (bool, error) {
			trustCalls++
			if !registry.HasPath("<inline:sdk-1>") {
				t.Fatal("project trust ran before extension discovery")
			}
			return false, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if trustCalls != 1 || settings.IsProjectTrusted() {
		t.Fatalf("trust calls=%d trusted=%t", trustCalls, settings.IsProjectTrusted())
	}
	if got := loader.GetSystemPrompt(); got == nil || *got != customPrompt {
		t.Fatalf("system prompt = %#v", got)
	}
	if got := loader.GetAppendSystemPrompt(); !reflect.DeepEqual(got, []string{"SDK append"}) {
		t.Fatalf("append prompt = %#v", got)
	}
	if got := loader.GetSkills().Skills; len(got) != 1 || got[0].Name != "inspect" {
		t.Fatalf("skills = %#v", got)
	}
	if got := loader.GetPrompts().Prompts; len(got) != 1 || got[0].Name != "deploy" {
		t.Fatalf("prompts = %#v", got)
	}
	if got := loader.GetThemes().Themes; len(got) != 1 || got[0].Name != "sdk" {
		t.Fatalf("themes = %#v", got)
	}
	if got := loader.GetAgentsFiles().AgentsFiles; len(got) != 1 || got[0].Content != "SDK context" {
		t.Fatalf("agents files = %#v", got)
	}

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100_000)
	ignored := "legacy resources must not override the loader"
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple,
		Resources: &Resources{SystemPrompt: &ignored}, ResourceLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if result.Services.ResourceLoader != loader || result.ExtensionRegistry != loader.GetExtensions() {
		t.Fatal("SDK did not retain the supplied resource loader services")
	}
	prompt := result.Session.Agent().State().SystemPrompt
	if !strings.Contains(prompt, customPrompt) || strings.Contains(prompt, ignored) || !strings.Contains(prompt, "SDK context") {
		t.Fatalf("assembled prompt = %q", prompt)
	}
	if expanded, handled := result.Session.slashResolver.ResolvePrompt("/deploy"); handled || expanded != "deploy now" {
		t.Fatalf("prompt expansion handled=%t value=%q", handled, expanded)
	}
}

func TestDefaultResourceLoaderExtendResourcesLoadsImmediately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "extended")
	promptPath := filepath.Join(t.TempDir(), "review.md")
	writeSkillFixture(t, skillDir, "extended", "Extended skill")
	writeResourceFixture(t, promptPath, "review now")
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoContextFiles: true, AppendSystemPrompt: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	loader.ExtendResources(ResourceExtensionPaths{
		SkillPaths: []ResourcePath{{Path: skillDir, Metadata: PathMetadata{
			Source: "sdk", Scope: "temporary", Origin: "extension", BaseDir: filepath.Dir(skillDir),
		}}},
		PromptPaths: []ResourcePath{{Path: promptPath, Metadata: PathMetadata{
			Source: "sdk", Scope: "temporary", Origin: "extension", BaseDir: filepath.Dir(promptPath),
		}}},
	})
	if got := loader.GetSkills().Skills; len(got) != 1 || got[0].Name != "extended" || got[0].SourceInfo.Source != "sdk" || got[0].SourceInfo.Origin != "extension" {
		t.Fatalf("extended skills = %#v", got)
	}
	if got := loader.GetPrompts().Prompts; len(got) != 1 || got[0].Name != "review" || got[0].SourceInfo.Source != "sdk" || got[0].SourceInfo.Origin != "extension" {
		t.Fatalf("extended prompts = %#v", got)
	}
}

func TestDefaultResourceLoaderExtendResourcesNormalizesMergesAndRetags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	skillDir := filepath.Join(cwd, "extension resources", "skill")
	promptPath := filepath.Join(cwd, "extension resources", "review.md")
	writeSkillFixture(t, skillDir, "extension-skill", "Extension skill")
	writeResourceFixture(t, promptPath, "review now")
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoContextFiles: true, AppendSystemPrompt: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	secondBase := filepath.Join(cwd, "second base")
	loader.ExtendResources(ResourceExtensionPaths{
		SkillPaths: []ResourcePath{
			{Path: filepath.Join("extension resources", "skill"), Metadata: PathMetadata{Source: "first", Scope: "temporary", Origin: "extension", BaseDir: "first base"}},
			{Path: (&url.URL{Scheme: "file", Path: skillDir}).String(), Metadata: PathMetadata{Source: "second", Scope: "temporary", Origin: "extension", BaseDir: "second base"}},
		},
		PromptPaths: []ResourcePath{
			{Path: filepath.Join("extension resources", "review.md"), Metadata: PathMetadata{Source: "first", Scope: "temporary", Origin: "extension", BaseDir: "first base"}},
			{Path: (&url.URL{Scheme: "file", Path: promptPath}).String(), Metadata: PathMetadata{Source: "second", Scope: "temporary", Origin: "extension", BaseDir: "second base"}},
		},
	})

	skills := loader.GetSkills()
	if len(skills.Skills) != 1 || len(skills.Diagnostics) != 0 {
		t.Fatalf("skills = %#v diagnostics = %#v", skills.Skills, skills.Diagnostics)
	}
	skill := skills.Skills[0]
	if skill.FilePath != filepath.Join(skillDir, "SKILL.md") || skill.SourceInfo.Source != "second" || skill.SourceInfo.BaseDir != secondBase {
		t.Fatalf("skill source info = %#v", skill.SourceInfo)
	}
	prompts := loader.GetPrompts()
	if len(prompts.Prompts) != 1 || len(prompts.Diagnostics) != 0 {
		t.Fatalf("prompts = %#v diagnostics = %#v", prompts.Prompts, prompts.Diagnostics)
	}
	prompt := prompts.Prompts[0]
	if prompt.FilePath != promptPath || prompt.SourceInfo.Source != "second" || prompt.SourceInfo.BaseDir != secondBase {
		t.Fatalf("prompt source info = %#v", prompt.SourceInfo)
	}
}

func TestDefaultResourceLoaderKeepsSkillAndPromptDiagnosticsIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	missingSkill := filepath.Join(cwd, "missing-skill")
	missingPrompt := filepath.Join(cwd, "missing-prompt.md")
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoContextFiles: true, AppendSystemPrompt: []string{},
		AdditionalSkillPaths: []string{missingSkill}, AdditionalPromptTemplatePaths: []string{missingPrompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	skillDiagnostics := loader.GetSkills().Diagnostics
	if len(skillDiagnostics) != 1 || skillDiagnostics[0].Path != missingSkill || strings.Contains(skillDiagnostics[0].Message, "Prompt") {
		t.Fatalf("skill diagnostics = %#v", skillDiagnostics)
	}
	promptDiagnostics := loader.GetPrompts().Diagnostics
	if len(promptDiagnostics) != 1 || promptDiagnostics[0].Path != missingPrompt || !strings.Contains(promptDiagnostics[0].Message, "Prompt") {
		t.Fatalf("prompt diagnostics = %#v", promptDiagnostics)
	}
}

func TestDefaultResourceLoaderExtendResourcesLoadsThemesImmediately(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	themePath := filepath.Join(cwd, "extension-theme.json")
	builtin, err := os.ReadFile(filepath.Join("modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeResourceFixture(t, themePath, strings.Replace(string(builtin), `"name": "dark"`, `"name": "extension-theme"`, 1))
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoContextFiles: true, NoThemes: true, AppendSystemPrompt: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	loader.ExtendResources(ResourceExtensionPaths{
		ThemePaths: []ResourcePath{{Path: "extension-theme.json", Metadata: PathMetadata{
			Source: "extension:theme", Scope: "temporary", Origin: "extension", BaseDir: ".",
		}}},
	})

	themes := loader.GetThemes()
	if len(themes.Diagnostics) != 0 || len(themes.Themes) != 1 || themes.Themes[0].Name != "extension-theme" || themes.Themes[0].Path == nil || *themes.Themes[0].Path != themePath {
		t.Fatalf("themes = %#v diagnostics = %#v", themes.Themes, themes.Diagnostics)
	}
}

func TestResourcesFromLoaderPreservesPerTypeDiagnosticLists(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	diagnostic := ResourceDiagnostic{Type: "warning", Message: "same diagnostic", Path: "/shared/path"}
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoSkills: true, NoPromptTemplates: true, NoThemes: true, NoContextFiles: true,
		AppendSystemPrompt: []string{},
		SkillsOverride: func(result ResourceSkillsResult) ResourceSkillsResult {
			result.Diagnostics = []ResourceDiagnostic{diagnostic}
			return result
		},
		PromptsOverride: func(result ResourcePromptsResult) ResourcePromptsResult {
			result.Diagnostics = []ResourceDiagnostic{diagnostic}
			return result
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	resources := resourcesFromLoader(loader)
	if !reflect.DeepEqual(resources.Diagnostics, []ResourceDiagnostic{diagnostic, diagnostic}) {
		t.Fatalf("combined diagnostics = %#v", resources.Diagnostics)
	}
}
