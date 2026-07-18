package runner_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentharness "github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f8Fixture struct {
	SchemaVersion       int                  `json:"schemaVersion"`
	ArgumentCases       []f8ArgumentCase     `json:"argumentCases"`
	SubstitutionCases   []f8SubstitutionCase `json:"substitutionCases"`
	TemplateCases       []f8TemplateCase     `json:"templateCases"`
	InvocationCases     []f8InvocationCase   `json:"invocationCases"`
	HarnessPrompts      f8HarnessPrompts     `json:"harnessPrompts"`
	Discovery           f8Discovery          `json:"discovery"`
	ResolutionTemplates []f8ResolutionPrompt `json:"resolutionTemplates"`
	ResolutionCases     []f8ResolutionCase   `json:"resolutionCases"`
}

type f8ArgumentCase struct {
	Name     string   `json:"name"`
	Input    string   `json:"input"`
	Expected []string `json:"expected"`
}

type f8SubstitutionCase struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Args     []string `json:"args"`
	Expected string   `json:"expected"`
}

type f8TemplateCase struct {
	Name      string             `json:"name"`
	Text      string             `json:"text"`
	Templates []f8PromptTemplate `json:"templates"`
	Expected  string             `json:"expected"`
}

type f8InvocationCase struct {
	Name                   string `json:"name"`
	AdditionalInstructions string `json:"additionalInstructions"`
	Expected               string `json:"expected"`
}

type f8HarnessPrompts struct {
	PromptTemplates []f8HarnessPrompt `json:"promptTemplates"`
	Diagnostics     []any             `json:"diagnostics"`
	Invocation      string            `json:"invocation"`
}

type f8HarnessPrompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

type f8FixtureFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type f8SourceInfo struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	BaseDir string `json:"baseDir,omitempty"`
}

type f8Skill struct {
	Name                   string       `json:"name"`
	Description            string       `json:"description"`
	FilePath               string       `json:"filePath"`
	BaseDir                string       `json:"baseDir"`
	DisableModelInvocation bool         `json:"disableModelInvocation"`
	SourceInfo             f8SourceInfo `json:"sourceInfo"`
}

type f8PromptTemplate struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ArgumentHint *string       `json:"argumentHint"`
	Content      string        `json:"content"`
	FilePath     string        `json:"filePath"`
	SourceInfo   *f8SourceInfo `json:"sourceInfo"`
}

type f8Command struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Source      string       `json:"source"`
	SourceInfo  f8SourceInfo `json:"sourceInfo"`
}

type f8Discovery struct {
	Files       []f8FixtureFile    `json:"files"`
	Skills      []f8Skill          `json:"skills"`
	Diagnostics []any              `json:"diagnostics"`
	Templates   []f8PromptTemplate `json:"templates"`
	Commands    []f8Command        `json:"commands"`
}

type f8ResolutionPrompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	FilePath    string `json:"filePath"`
}

type f8ResolutionCase struct {
	Name     string   `json:"name"`
	Text     string   `json:"text"`
	Handled  bool     `json:"handled"`
	Expanded *string  `json:"expanded"`
	Trace    []string `json:"trace"`
}

func TestF8PromptExpansionMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	for _, fixtureCase := range fixture.ArgumentCases {
		fixtureCase := fixtureCase
		t.Run("args/"+fixtureCase.Name, func(t *testing.T) {
			if got := codingagent.ParseCommandArgs(fixtureCase.Input); !reflect.DeepEqual(got, fixtureCase.Expected) {
				t.Fatalf("arguments mismatch\nwant: %#v\n got: %#v", fixtureCase.Expected, got)
			}
		})
	}
	for _, fixtureCase := range fixture.SubstitutionCases {
		fixtureCase := fixtureCase
		t.Run("substitution/"+fixtureCase.Name, func(t *testing.T) {
			if got := codingagent.SubstituteArgs(fixtureCase.Content, fixtureCase.Args); got != fixtureCase.Expected {
				t.Fatalf("substitution mismatch:\n%s", runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
			}
		})
	}
	for _, fixtureCase := range fixture.TemplateCases {
		fixtureCase := fixtureCase
		t.Run("template/"+fixtureCase.Name, func(t *testing.T) {
			templates := make([]codingagent.PromptTemplate, len(fixtureCase.Templates))
			for index, template := range fixtureCase.Templates {
				templates[index] = codingagent.PromptTemplate{Name: template.Name, Description: template.Description, Content: template.Content}
			}
			if got := codingagent.ExpandPromptTemplate(fixtureCase.Text, templates); got != fixtureCase.Expected {
				t.Fatalf("template mismatch:\n%s", runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
			}
		})
	}
}

func TestF8ResourceDiscoveryMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.Discovery.Files)
	skillsDir := filepath.Join(fixtureRoot, "skills")
	promptsDir := filepath.Join(fixtureRoot, "prompts")

	skillResult := codingagent.LoadSkills(codingagent.LoadSkillsOptions{
		CWD: fixtureRoot, AgentDir: filepath.Join(fixtureRoot, "agent"), SkillPaths: []string{skillsDir},
	})
	if len(skillResult.Diagnostics) != len(fixture.Discovery.Diagnostics) {
		t.Fatalf("skill diagnostics\nwant: %+v\n got: %+v", fixture.Discovery.Diagnostics, skillResult.Diagnostics)
	}
	gotSkills := make([]f8Skill, len(skillResult.Skills))
	for index, skill := range skillResult.Skills {
		gotSkills[index] = f8FixtureSkill(skill, fixtureRoot)
	}
	if !reflect.DeepEqual(gotSkills, fixture.Discovery.Skills) {
		t.Fatalf("skills mismatch\nwant: %+v\n got: %+v", fixture.Discovery.Skills, gotSkills)
	}

	templates := codingagent.LoadPromptTemplates(codingagent.LoadPromptTemplatesOptions{
		CWD: fixtureRoot, AgentDir: filepath.Join(fixtureRoot, "agent"), PromptPaths: []string{promptsDir},
	})
	gotTemplates := make([]f8PromptTemplate, len(templates))
	for index, template := range templates {
		gotTemplates[index] = f8FixtureTemplate(template, fixtureRoot)
	}
	if !reflect.DeepEqual(gotTemplates, fixture.Discovery.Templates) {
		t.Fatalf("templates mismatch\nwant: %+v\n got: %+v", fixture.Discovery.Templates, gotTemplates)
	}

	resolver := codingagent.SlashResolver{Skills: skillResult.Skills, PromptTemplates: templates}
	commands := resolver.Commands(true)
	gotCommands := make([]f8Command, len(commands))
	for index, command := range commands {
		gotCommands[index] = f8Command{
			Name: command.Name, Description: command.Description, Source: string(command.Source),
			SourceInfo: f8FixtureSourceInfo(command.SourceInfo, fixtureRoot),
		}
	}
	if !reflect.DeepEqual(gotCommands, fixture.Discovery.Commands) {
		t.Fatalf("commands mismatch\nwant: %+v\n got: %+v", fixture.Discovery.Commands, gotCommands)
	}

	harnessResult := agentharness.LoadSkills(agentharness.LocalExecutionEnv{CWD: fixtureRoot}, skillsDir)
	var inspect *agentharness.Skill
	for index := range harnessResult.Skills {
		if harnessResult.Skills[index].Name == "inspect" {
			inspect = &harnessResult.Skills[index]
			break
		}
	}
	if inspect == nil {
		t.Fatal("harness did not load inspect skill")
	}
	for _, fixtureCase := range fixture.InvocationCases {
		got := agentharness.FormatSkillInvocation(*inspect, fixtureCase.AdditionalInstructions)
		got = f8NormalizePath(got, fixtureRoot)
		if got != fixtureCase.Expected {
			t.Fatalf("%s invocation mismatch:\n%s", fixtureCase.Name, runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
		}
	}
	harnessPrompts := agentharness.LoadPromptTemplates(agentharness.LocalExecutionEnv{CWD: fixtureRoot}, promptsDir)
	gotHarnessPrompts := make([]f8HarnessPrompt, len(harnessPrompts.PromptTemplates))
	for index, template := range harnessPrompts.PromptTemplates {
		gotHarnessPrompts[index] = f8HarnessPrompt{Name: template.Name, Description: template.Description, Content: template.Content}
	}
	if len(harnessPrompts.Diagnostics) != len(fixture.HarnessPrompts.Diagnostics) || !reflect.DeepEqual(gotHarnessPrompts, fixture.HarnessPrompts.PromptTemplates) {
		t.Fatalf("harness prompts mismatch\nwant: %+v / %+v\n got: %+v / %+v", fixture.HarnessPrompts.PromptTemplates, fixture.HarnessPrompts.Diagnostics, gotHarnessPrompts, harnessPrompts.Diagnostics)
	}
	var review agentharness.PromptTemplate
	for _, template := range harnessPrompts.PromptTemplates {
		if template.Name == "review" {
			review = template
			break
		}
	}
	if got := agentharness.FormatPromptTemplateInvocation(review, []string{"file.go", "focus", "errors"}); got != fixture.HarnessPrompts.Invocation {
		t.Fatalf("harness prompt invocation mismatch:\n%s", runner.ByteDiff([]byte(fixture.HarnessPrompts.Invocation), []byte(got)))
	}
}

func TestF8SlashResolutionMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.Discovery.Files)
	skills := codingagent.LoadSkills(codingagent.LoadSkillsOptions{
		CWD: fixtureRoot, AgentDir: filepath.Join(fixtureRoot, "agent"), SkillPaths: []string{filepath.Join(fixtureRoot, "skills")},
	}).Skills
	templates := make([]codingagent.PromptTemplate, len(fixture.ResolutionTemplates))
	for index, template := range fixture.ResolutionTemplates {
		templates[index] = codingagent.PromptTemplate{
			Name: template.Name, Description: template.Description, Content: template.Content,
			FilePath: f8MaterializePath(template.FilePath, fixtureRoot),
		}
	}

	for _, fixtureCase := range fixture.ResolutionCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			trace := []string{}
			resolver := codingagent.SlashResolver{
				Skills: skills, PromptTemplates: templates,
				ExecuteExtension: func(name, args string) (bool, error) {
					if name != "ext" {
						return false, nil
					}
					trace = append(trace, "extension:"+args)
					return true, nil
				},
				InterceptInput: func(text string) (codingagent.InputResult, error) {
					trace = append(trace, "input:"+text)
					switch {
					case strings.HasPrefix(text, "/alias "):
						return codingagent.InputResult{Action: codingagent.InputTransform, Text: "/review " + text[7:]}, nil
					case strings.HasPrefix(text, "/choose "):
						return codingagent.InputResult{Action: codingagent.InputTransform, Text: "/skill:inspect " + text[8:]}, nil
					case text == "/consume":
						return codingagent.InputResult{Action: codingagent.InputHandled}, nil
					default:
						return codingagent.InputResult{Action: codingagent.InputPass}, nil
					}
				},
			}
			expanded, handled := resolver.ResolvePrompt(fixtureCase.Text)
			var gotExpanded *string
			if !handled {
				normalized := f8NormalizePath(expanded, fixtureRoot)
				gotExpanded = &normalized
			}
			if handled != fixtureCase.Handled || !reflect.DeepEqual(gotExpanded, fixtureCase.Expanded) || !reflect.DeepEqual(trace, fixtureCase.Trace) {
				t.Fatalf("resolution mismatch\nwant handled=%v expanded=%v trace=%v\n got handled=%v expanded=%v trace=%v", fixtureCase.Handled, fixtureCase.Expanded, fixtureCase.Trace, handled, gotExpanded, trace)
			}
		})
	}
}

func loadF8Fixture(t testing.TB) f8Fixture {
	t.Helper()
	manifest := runner.LoadManifest(t, "F8")
	if manifest.Family != "F8" || manifest.Generator != "conformance/extract/f8-slash-templates.ts" {
		t.Fatalf("unexpected F8 manifest: %+v", manifest)
	}
	var fixture f8Fixture
	runner.LoadJSON(t, "F8", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.ArgumentCases) == 0 || len(fixture.ResolutionCases) == 0 {
		t.Fatalf("invalid F8 fixture header: %+v", fixture)
	}
	return fixture
}

func writeF8Tree(t testing.TB, root string, files []f8FixtureFile) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s parent: %v", file.Path, err)
		}
		if err := os.WriteFile(path, []byte(file.Content), 0o644); err != nil {
			t.Fatalf("write %s: %v", file.Path, err)
		}
	}
}

func f8FixtureSkill(skill codingagent.Skill, fixtureRoot string) f8Skill {
	return f8Skill{
		Name: skill.Name, Description: skill.Description,
		FilePath: f8NormalizePath(skill.FilePath, fixtureRoot), BaseDir: f8NormalizePath(skill.BaseDir, fixtureRoot),
		DisableModelInvocation: skill.DisableModelInvocation,
		SourceInfo:             f8FixtureSourceInfo(skill.SourceInfo, fixtureRoot),
	}
}

func f8FixtureTemplate(template codingagent.PromptTemplate, fixtureRoot string) f8PromptTemplate {
	var hint *string
	if template.ArgumentHint != "" {
		value := template.ArgumentHint
		hint = &value
	}
	source := f8FixtureSourceInfo(template.SourceInfo, fixtureRoot)
	return f8PromptTemplate{
		Name: template.Name, Description: template.Description, ArgumentHint: hint, Content: template.Content,
		FilePath: f8NormalizePath(template.FilePath, fixtureRoot), SourceInfo: &source,
	}
}

func f8FixtureSourceInfo(source codingagent.SourceInfo, fixtureRoot string) f8SourceInfo {
	return f8SourceInfo{
		Path: f8NormalizePath(source.Path, fixtureRoot), Source: source.Source, Scope: source.Scope,
		Origin: source.Origin, BaseDir: f8NormalizePath(source.BaseDir, fixtureRoot),
	}
}

func f8MaterializePath(value, fixtureRoot string) string {
	return strings.ReplaceAll(value, "<fixture>", filepath.ToSlash(fixtureRoot))
}

func f8NormalizePath(value, fixtureRoot string) string {
	return strings.ReplaceAll(filepath.ToSlash(value), filepath.ToSlash(fixtureRoot), "<fixture>")
}
