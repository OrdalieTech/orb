package runner_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f9Fixture struct {
	SchemaVersion  int               `json:"schemaVersion"`
	PackageDir     string            `json:"packageDir"`
	PromptCases    []f9PromptCase    `json:"promptCases"`
	DiscoveryCases []f9DiscoveryCase `json:"discoveryCases"`
}

type f9PromptCase struct {
	Name     string        `json:"name"`
	Input    f9PromptInput `json:"input"`
	Expected string        `json:"expected"`
}

type f9PromptInput struct {
	CustomPrompt       *string           `json:"customPrompt"`
	SelectedTools      []string          `json:"selectedTools"`
	ToolSnippets       map[string]string `json:"toolSnippets"`
	PromptGuidelines   []string          `json:"promptGuidelines"`
	AppendSystemPrompt *string           `json:"appendSystemPrompt"`
	CWD                string            `json:"cwd"`
	ContextFiles       []f9ContextFile   `json:"contextFiles"`
	Skills             []f9Skill         `json:"skills"`
}

type f9Skill struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	FilePath               string `json:"filePath"`
	BaseDir                string `json:"baseDir"`
	DisableModelInvocation bool   `json:"disableModelInvocation"`
}

type f9ContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type f9DiscoveryCase struct {
	Name                  string              `json:"name"`
	CWD                   string              `json:"cwd"`
	AgentDir              string              `json:"agentDir"`
	Files                 []f9ContextFile     `json:"files"`
	NoContextFiles        bool                `json:"noContextFiles"`
	ProjectTrusted        bool                `json:"projectTrusted"`
	SystemPromptSet       bool                `json:"systemPromptSet"`
	SystemPrompt          string              `json:"systemPrompt"`
	AppendSystemPromptSet bool                `json:"appendSystemPromptSet"`
	AppendSystemPrompt    []string            `json:"appendSystemPrompt"`
	Expected              f9DiscoveryExpected `json:"expected"`
}

type f9DiscoveryExpected struct {
	ContextFiles       []f9ContextFile `json:"contextFiles"`
	SystemPrompt       *string         `json:"systemPrompt"`
	AppendSystemPrompt []string        `json:"appendSystemPrompt"`
	AssembledPrompt    string          `json:"assembledPrompt"`
}

func TestF9SystemPromptMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F9")
	if manifest.Family != "F9" || manifest.Generator != "conformance/extract/f9-system-prompt.ts" {
		t.Fatalf("unexpected F9 manifest: %+v", manifest)
	}

	fixture := loadF9Fixture(t)
	for _, fixtureCase := range fixture.PromptCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			got := codingagent.BuildSystemPrompt(codingagent.SystemPromptOptions{
				CustomPrompt:       fixtureCase.Input.CustomPrompt,
				SelectedTools:      fixtureCase.Input.SelectedTools,
				ToolSnippets:       fixtureCase.Input.ToolSnippets,
				PromptGuidelines:   fixtureCase.Input.PromptGuidelines,
				AppendSystemPrompt: fixtureCase.Input.AppendSystemPrompt,
				CWD:                fixtureCase.Input.CWD,
				ContextFiles:       f9CodingContextFiles(fixtureCase.Input.ContextFiles),
				Skills:             f9CodingSkills(fixtureCase.Input.Skills),
				PackageDir:         fixture.PackageDir,
			})
			if got != fixtureCase.Expected {
				t.Fatalf("system prompt mismatch:\n%s", runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
			}
		})
	}
}

func TestF9ResourceDiscoveryMatchesUpstream(t *testing.T) {
	fixture := loadF9Fixture(t)
	for _, fixtureCase := range fixture.DiscoveryCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			fixtureRoot := t.TempDir()
			writeF9Tree(t, fixtureRoot, fixtureCase.Files)
			if fixtureCase.Name == "global-root-ancestor-cwd-order-and-case-priority" {
				if _, err := os.Stat(filepath.Join(fixtureRoot, "project", "AGENTS.md")); err == nil {
					t.Skip("case-insensitive filesystem cannot represent the upstream case-priority fixture")
				}
			}

			cwd := filepath.Join(fixtureRoot, filepath.FromSlash(fixtureCase.CWD))
			agentDir := filepath.Join(fixtureRoot, filepath.FromSlash(fixtureCase.AgentDir))
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatalf("create cwd: %v", err)
			}
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("create agent dir: %v", err)
			}

			options := codingagent.ResourceOptions{
				CWD:               cwd,
				AgentDir:          agentDir,
				ProjectTrusted:    &fixtureCase.ProjectTrusted,
				NoContextFiles:    fixtureCase.NoContextFiles,
				NoSkills:          true,
				NoPromptTemplates: true,
			}
			if fixtureCase.SystemPromptSet {
				systemPrompt := f9MaterializePath(fixtureCase.SystemPrompt, fixtureRoot)
				options.SystemPrompt = &systemPrompt
			}
			if fixtureCase.AppendSystemPromptSet {
				options.AppendSystemPrompt = make([]string, len(fixtureCase.AppendSystemPrompt))
				for index, source := range fixtureCase.AppendSystemPrompt {
					options.AppendSystemPrompt[index] = f9MaterializePath(source, fixtureRoot)
				}
			}

			resources := codingagent.LoadResources(options)
			if len(resources.Diagnostics) != 0 {
				t.Fatalf("resource diagnostics: %+v", resources.Diagnostics)
			}
			appendPrompt := strings.Join(resources.AppendSystemPrompt, "\n\n")
			var appendPromptPointer *string
			if appendPrompt != "" {
				appendPromptPointer = &appendPrompt
			}
			assembled := codingagent.BuildSystemPrompt(codingagent.SystemPromptOptions{
				CustomPrompt:  resources.SystemPrompt,
				SelectedTools: []string{"read", "bash", "edit", "write"},
				ToolSnippets: map[string]string{
					"read":  "Read file contents",
					"bash":  "Execute bash commands",
					"edit":  "Make surgical edits",
					"write": "Create or overwrite files",
				},
				AppendSystemPrompt: appendPromptPointer,
				CWD:                cwd,
				ContextFiles:       resources.ContextFiles,
				PackageDir:         fixture.PackageDir,
			})

			got := f9DiscoveryExpected{
				ContextFiles:       f9FixtureContextFiles(resources.ContextFiles, fixtureRoot),
				SystemPrompt:       resources.SystemPrompt,
				AppendSystemPrompt: resources.AppendSystemPrompt,
				AssembledPrompt:    f9NormalizeFixturePath(assembled, fixtureRoot),
			}
			if diff := runner.ByteDiff([]byte(fixtureCase.Expected.AssembledPrompt), []byte(got.AssembledPrompt)); diff != "" {
				t.Fatalf("assembled system prompt mismatch:\n%s", diff)
			}
			if !reflect.DeepEqual(got, fixtureCase.Expected) {
				t.Fatalf("resource result mismatch\nwant: %+v\n got: %+v", fixtureCase.Expected, got)
			}
		})
	}
}

func loadF9Fixture(t testing.TB) f9Fixture {
	t.Helper()
	var fixture f9Fixture
	runner.LoadJSON(t, "F9", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.PromptCases) != 6 || len(fixture.DiscoveryCases) != 6 {
		t.Fatalf(
			"F9 fixture header = version %d, prompt cases %d, discovery cases %d",
			fixture.SchemaVersion,
			len(fixture.PromptCases),
			len(fixture.DiscoveryCases),
		)
	}
	return fixture
}

func f9CodingSkills(skills []f9Skill) []codingagent.Skill {
	if skills == nil {
		return nil
	}
	converted := make([]codingagent.Skill, len(skills))
	for index, skill := range skills {
		converted[index] = codingagent.Skill{
			Name:                   skill.Name,
			Description:            skill.Description,
			FilePath:               skill.FilePath,
			BaseDir:                skill.BaseDir,
			DisableModelInvocation: skill.DisableModelInvocation,
		}
	}
	return converted
}

func writeF9Tree(t testing.TB, root string, files []f9ContextFile) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", file.Path, err)
		}
		if err := os.WriteFile(path, []byte(file.Content), 0o644); err != nil {
			t.Fatalf("write %s: %v", file.Path, err)
		}
	}
}

func f9CodingContextFiles(files []f9ContextFile) []codingagent.ContextFile {
	if files == nil {
		return nil
	}
	converted := make([]codingagent.ContextFile, len(files))
	for index, file := range files {
		converted[index] = codingagent.ContextFile{Path: file.Path, Content: file.Content}
	}
	return converted
}

func f9FixtureContextFiles(files []codingagent.ContextFile, fixtureRoot string) []f9ContextFile {
	converted := make([]f9ContextFile, len(files))
	for index, file := range files {
		converted[index] = f9ContextFile{
			Path:    f9NormalizeFixturePath(file.Path, fixtureRoot),
			Content: file.Content,
		}
	}
	return converted
}

func f9MaterializePath(value, fixtureRoot string) string {
	return strings.ReplaceAll(value, "<fixture>", fixtureRoot)
}

func f9NormalizeFixturePath(value, fixtureRoot string) string {
	return runner.ReplacePathAliases(value, filepath.ToSlash(fixtureRoot), "<fixture>")
}
