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
	HarnessSubstitution []f8SubstitutionCase `json:"harnessSubstitutionCases"`
	HarnessPrompts      f8HarnessPrompts     `json:"harnessPrompts"`
	Discovery           f8Discovery          `json:"discovery"`
	ResourceLoader      f8ResourceLoader     `json:"resourceLoader"`
	ResourceExtension   f8ResourceExtension  `json:"resourceLoaderExtension"`
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
	DirectPrompt    f8HarnessResult   `json:"directPrompt"`
	Invocation      string            `json:"invocation"`
}

type f8HarnessResult struct {
	PromptTemplates []f8HarnessPrompt `json:"promptTemplates"`
	Diagnostics     []any             `json:"diagnostics"`
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

type f8BuiltinCommand struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	ArgumentHint *string `json:"argumentHint"`
}

type f8ResourceCollision struct {
	ResourceType string `json:"resourceType"`
	Name         string `json:"name"`
	WinnerPath   string `json:"winnerPath"`
	LoserPath    string `json:"loserPath"`
}

type f8ResourceDiagnostic struct {
	Type      string               `json:"type"`
	Message   string               `json:"message"`
	Path      string               `json:"path"`
	Collision *f8ResourceCollision `json:"collision"`
}

type f8Discovery struct {
	Files                                []f8FixtureFile        `json:"files"`
	Skills                               []f8Skill              `json:"skills"`
	Diagnostics                          []f8ResourceDiagnostic `json:"diagnostics"`
	Templates                            []f8PromptTemplate     `json:"templates"`
	Commands                             []f8Command            `json:"commands"`
	RPCCommandsWhenSkillCommandsDisabled []f8Command            `json:"rpcCommandsWhenSkillCommandsDisabled"`
	BuiltinCommands                      []f8BuiltinCommand     `json:"builtinCommands"`
}

type f8ResourceLoader struct {
	Files                      []f8FixtureFile        `json:"files"`
	CWD                        string                 `json:"cwd"`
	AgentDir                   string                 `json:"agentDir"`
	SkillPaths                 []string               `json:"skillPaths"`
	PromptPaths                []string               `json:"promptPaths"`
	PackageSkillPaths          []string               `json:"packageSkillPaths"`
	PackagePromptTemplatePaths []string               `json:"packagePromptPaths"`
	Skills                     []f8Skill              `json:"skills"`
	Templates                  []f8PromptTemplate     `json:"templates"`
	Diagnostics                []f8ResourceDiagnostic `json:"diagnostics"`
}

type f8PathMetadata struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	BaseDir string `json:"baseDir,omitempty"`
}

type f8ResourcePath struct {
	Path     string         `json:"path"`
	Metadata f8PathMetadata `json:"metadata"`
}

type f8ResourceExtension struct {
	Files             []f8FixtureFile        `json:"files"`
	CWD               string                 `json:"cwd"`
	AgentDir          string                 `json:"agentDir"`
	SkillPaths        []f8ResourcePath       `json:"skillPaths"`
	PromptPaths       []f8ResourcePath       `json:"promptPaths"`
	Skills            []f8Skill              `json:"skills"`
	SkillDiagnostics  []f8ResourceDiagnostic `json:"skillDiagnostics"`
	Templates         []f8PromptTemplate     `json:"templates"`
	PromptDiagnostics []f8ResourceDiagnostic `json:"promptDiagnostics"`
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
	gotDiagnostics := f8FixtureDiagnostics(skillResult.Diagnostics, fixtureRoot)
	if !reflect.DeepEqual(gotDiagnostics, fixture.Discovery.Diagnostics) {
		t.Fatalf("skill diagnostics\nwant: %+v\n got: %+v", fixture.Discovery.Diagnostics, gotDiagnostics)
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

func TestF8CommandSurfacesMatchUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.Discovery.Files)
	skills := codingagent.LoadSkills(codingagent.LoadSkillsOptions{
		CWD: fixtureRoot, AgentDir: filepath.Join(fixtureRoot, "agent"),
		SkillPaths: []string{filepath.Join(fixtureRoot, "skills")},
	}).Skills
	templates := codingagent.LoadPromptTemplates(codingagent.LoadPromptTemplatesOptions{
		CWD: fixtureRoot, AgentDir: filepath.Join(fixtureRoot, "agent"),
		PromptPaths: []string{filepath.Join(fixtureRoot, "prompts")},
	})
	resolver := codingagent.SlashResolver{Skills: skills, PromptTemplates: templates}
	t.Run("rpc-ignores-interactive-skill-command-setting", func(t *testing.T) {
		gotRPC := f8FixtureCommands(resolver.Commands(false), fixtureRoot)
		if !reflect.DeepEqual(gotRPC, fixture.Discovery.RPCCommandsWhenSkillCommandsDisabled) {
			t.Fatalf("RPC commands with enableSkillCommands=false\nwant: %+v\n got: %+v", fixture.Discovery.RPCCommandsWhenSkillCommandsDisabled, gotRPC)
		}
	})
	t.Run("interactive-builtins", func(t *testing.T) {
		gotBuiltins := make([]f8BuiltinCommand, len(codingagent.BuiltinSlashCommands))
		for index, command := range codingagent.BuiltinSlashCommands {
			var hint *string
			if command.ArgumentHint != "" {
				value := command.ArgumentHint
				hint = &value
			}
			gotBuiltins[index] = f8BuiltinCommand{Name: command.Name, Description: command.Description, ArgumentHint: hint}
		}
		if !reflect.DeepEqual(gotBuiltins, fixture.Discovery.BuiltinCommands) {
			t.Fatalf("built-in commands\nwant: %+v\n got: %+v", fixture.Discovery.BuiltinCommands, gotBuiltins)
		}
	})
}

func TestF8ResourceLoaderPrecedenceMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.ResourceLoader.Files)
	t.Setenv("HOME", filepath.Join(fixtureRoot, "home"))
	trusted := true
	resources := codingagent.LoadResources(codingagent.ResourceOptions{
		CWD:                        f8MaterializePath(fixture.ResourceLoader.CWD, fixtureRoot),
		AgentDir:                   f8MaterializePath(fixture.ResourceLoader.AgentDir, fixtureRoot),
		ProjectTrusted:             &trusted,
		NoContextFiles:             true,
		SkillPaths:                 f8MaterializePaths(fixture.ResourceLoader.SkillPaths, fixtureRoot),
		PromptTemplatePaths:        f8MaterializePaths(fixture.ResourceLoader.PromptPaths, fixtureRoot),
		PackageSkillPaths:          f8MaterializePaths(fixture.ResourceLoader.PackageSkillPaths, fixtureRoot),
		PackagePromptTemplatePaths: f8MaterializePaths(fixture.ResourceLoader.PackagePromptTemplatePaths, fixtureRoot),
	})
	gotSkills := make([]f8Skill, len(resources.Skills))
	for index, skill := range resources.Skills {
		gotSkills[index] = f8FixtureSkill(skill, fixtureRoot)
	}
	t.Run("skills-precedence-and-source-info", func(t *testing.T) {
		if !reflect.DeepEqual(gotSkills, fixture.ResourceLoader.Skills) {
			t.Fatalf("resource-loader skills\nwant: %+v\n got: %+v", fixture.ResourceLoader.Skills, gotSkills)
		}
	})
	gotTemplates := make([]f8PromptTemplate, len(resources.PromptTemplates))
	for index, template := range resources.PromptTemplates {
		gotTemplates[index] = f8FixtureTemplate(template, fixtureRoot)
	}
	t.Run("prompt-precedence-dedupe-empty-and-source-info", func(t *testing.T) {
		if !reflect.DeepEqual(gotTemplates, fixture.ResourceLoader.Templates) {
			t.Fatalf("resource-loader prompts\nwant: %+v\n got: %+v", fixture.ResourceLoader.Templates, gotTemplates)
		}
	})
	gotDiagnostics := f8FixtureDiagnostics(resources.Diagnostics, fixtureRoot)
	t.Run("diagnostics-and-collisions", func(t *testing.T) {
		if !reflect.DeepEqual(gotDiagnostics, fixture.ResourceLoader.Diagnostics) {
			t.Fatalf("resource-loader diagnostics\nwant: %+v\n got: %+v", fixture.ResourceLoader.Diagnostics, gotDiagnostics)
		}
	})
}

func TestF8ResourceLoaderExtensionsMatchUpstreamImmediately(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.ResourceExtension.Files)
	t.Setenv("HOME", filepath.Join(fixtureRoot, "loader-extension", "home"))
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD:          f8MaterializePath(fixture.ResourceExtension.CWD, fixtureRoot),
		AgentDir:     f8MaterializePath(fixture.ResourceExtension.AgentDir, fixtureRoot),
		NoExtensions: true, NoSkills: true, NoPromptTemplates: true, NoThemes: true, NoContextFiles: true,
		AppendSystemPrompt: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	loader.ExtendResources(codingagent.ResourceExtensionPaths{
		SkillPaths:  f8MaterializeResourcePaths(fixture.ResourceExtension.SkillPaths, fixtureRoot),
		PromptPaths: f8MaterializeResourcePaths(fixture.ResourceExtension.PromptPaths, fixtureRoot),
	})

	skillResult := loader.GetSkills()
	gotSkills := make([]f8Skill, len(skillResult.Skills))
	for index, skill := range skillResult.Skills {
		gotSkills[index] = f8FixtureSkill(skill, fixtureRoot)
	}
	if !reflect.DeepEqual(gotSkills, fixture.ResourceExtension.Skills) {
		t.Fatalf("immediate extension skills\nwant: %+v\n got: %+v", fixture.ResourceExtension.Skills, gotSkills)
	}
	gotSkillDiagnostics := f8FixtureDiagnostics(skillResult.Diagnostics, fixtureRoot)
	if !reflect.DeepEqual(gotSkillDiagnostics, fixture.ResourceExtension.SkillDiagnostics) {
		t.Fatalf("extension skill diagnostics\nwant: %+v\n got: %+v", fixture.ResourceExtension.SkillDiagnostics, gotSkillDiagnostics)
	}

	promptResult := loader.GetPrompts()
	gotTemplates := make([]f8PromptTemplate, len(promptResult.Prompts))
	for index, template := range promptResult.Prompts {
		gotTemplates[index] = f8FixtureTemplate(template, fixtureRoot)
	}
	if !reflect.DeepEqual(gotTemplates, fixture.ResourceExtension.Templates) {
		t.Fatalf("immediate extension prompts\nwant: %+v\n got: %+v", fixture.ResourceExtension.Templates, gotTemplates)
	}
	gotPromptDiagnostics := f8FixtureDiagnostics(promptResult.Diagnostics, fixtureRoot)
	if !reflect.DeepEqual(gotPromptDiagnostics, fixture.ResourceExtension.PromptDiagnostics) {
		t.Fatalf("extension prompt diagnostics\nwant: %+v\n got: %+v", fixture.ResourceExtension.PromptDiagnostics, gotPromptDiagnostics)
	}
}

func TestF8HarnessSubstitutionMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	for _, fixtureCase := range fixture.HarnessSubstitution {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			template := agentharness.PromptTemplate{Name: fixtureCase.Name, Content: fixtureCase.Content}
			got := agentharness.FormatPromptTemplateInvocation(template, fixtureCase.Args)
			if got != fixtureCase.Expected {
				t.Fatalf("harness substitution mismatch:\n%s", runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
			}
		})
	}
}

func TestF8HarnessDirectPromptMatchesUpstream(t *testing.T) {
	fixture := loadF8Fixture(t)
	fixtureRoot := t.TempDir()
	writeF8Tree(t, fixtureRoot, fixture.Discovery.Files)
	result := agentharness.LoadPromptTemplates(
		agentharness.LocalExecutionEnv{CWD: fixtureRoot},
		filepath.Join(fixtureRoot, "prompts", "empty.md"),
	)
	got := f8HarnessResult{Diagnostics: []any{}}
	got.PromptTemplates = make([]f8HarnessPrompt, len(result.PromptTemplates))
	for index, template := range result.PromptTemplates {
		got.PromptTemplates[index] = f8HarnessPrompt{Name: template.Name, Description: template.Description, Content: template.Content}
	}
	if len(result.Diagnostics) != 0 || !reflect.DeepEqual(got, fixture.HarnessPrompts.DirectPrompt) {
		t.Fatalf("direct harness prompt\nwant: %+v\n got: %+v diagnostics=%+v", fixture.HarnessPrompts.DirectPrompt, got, result.Diagnostics)
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
	if fixture.SchemaVersion != 3 || len(fixture.ArgumentCases) == 0 || len(fixture.ResolutionCases) == 0 || len(fixture.ResourceLoader.Files) == 0 || len(fixture.ResourceExtension.Files) == 0 {
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

func f8FixtureCommands(commands []codingagent.SlashCommandInfo, fixtureRoot string) []f8Command {
	result := make([]f8Command, len(commands))
	for index, command := range commands {
		result[index] = f8Command{
			Name: command.Name, Description: command.Description, Source: string(command.Source),
			SourceInfo: f8FixtureSourceInfo(command.SourceInfo, fixtureRoot),
		}
	}
	return result
}

func f8FixtureDiagnostics(diagnostics []codingagent.ResourceDiagnostic, fixtureRoot string) []f8ResourceDiagnostic {
	result := make([]f8ResourceDiagnostic, len(diagnostics))
	for index, diagnostic := range diagnostics {
		result[index] = f8ResourceDiagnostic{
			Type: diagnostic.Type, Message: f8NormalizePath(diagnostic.Message, fixtureRoot),
			Path: f8NormalizePath(diagnostic.Path, fixtureRoot),
		}
		if diagnostic.Collision != nil {
			result[index].Collision = &f8ResourceCollision{
				ResourceType: diagnostic.Collision.ResourceType,
				Name:         diagnostic.Collision.Name,
				WinnerPath:   f8NormalizePath(diagnostic.Collision.WinnerPath, fixtureRoot),
				LoserPath:    f8NormalizePath(diagnostic.Collision.LoserPath, fixtureRoot),
			}
		}
	}
	return result
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

func f8MaterializePaths(values []string, fixtureRoot string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = f8MaterializePath(value, fixtureRoot)
	}
	return result
}

func f8MaterializeResourcePaths(values []f8ResourcePath, fixtureRoot string) []codingagent.ResourcePath {
	result := make([]codingagent.ResourcePath, len(values))
	for index, value := range values {
		result[index] = codingagent.ResourcePath{
			Path: f8MaterializePath(value.Path, fixtureRoot),
			Metadata: codingagent.PathMetadata{
				Source: value.Metadata.Source, Scope: value.Metadata.Scope, Origin: value.Metadata.Origin,
				BaseDir: f8MaterializePath(value.Metadata.BaseDir, fixtureRoot),
			},
		}
	}
	return result
}

func f8NormalizePath(value, fixtureRoot string) string {
	return strings.ReplaceAll(filepath.ToSlash(value), filepath.ToSlash(fixtureRoot), "<fixture>")
}
