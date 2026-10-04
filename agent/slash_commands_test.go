package agent

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSlashResolutionOrderAndSkillTemplateCollision(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "inspect", "SKILL.md")
	mustWriteResource(t, skillPath, "---\nname: inspect\ndescription: Inspect\n---\nSkill body")
	stages := make([]string, 0)
	resolver := &SlashResolver{
		Skills: []Skill{{Name: "inspect", Description: "Inspect", FilePath: skillPath, BaseDir: filepath.Dir(skillPath)}},
		PromptTemplates: []PromptTemplate{
			{Name: "review", Content: "Template $1"},
			{Name: "skill:inspect", Content: "Wrong $1"},
		},
		ExecuteExtension: func(name, args string) (bool, error) {
			stages = append(stages, "extension:"+name+":"+args)
			return name == "handled", nil
		},
		InterceptInput: func(text string) (InputResult, error) {
			stages = append(stages, "input:"+text)
			if strings.HasPrefix(text, "/alias ") {
				return InputResult{Action: InputTransform, Text: "/review " + strings.TrimPrefix(text, "/alias ")}, nil
			}
			return InputResult{Action: InputPass}, nil
		},
	}

	if got, handled := resolver.ResolvePrompt("/handled now"); !handled || got != "/handled now" || !reflect.DeepEqual(stages, []string{"extension:handled:now"}) {
		t.Fatalf("extension resolution = %q, %v, %#v", got, handled, stages)
	}
	stages = stages[:0]
	if got, handled := resolver.ResolvePrompt("/alias file.go"); handled || got != "Template file.go" || !reflect.DeepEqual(stages, []string{"extension:alias:file.go", "input:/alias file.go"}) {
		t.Fatalf("input/template resolution = %q, %v, %#v", got, handled, stages)
	}
	stages = stages[:0]
	got, handled := resolver.ResolvePrompt("/skill:inspect extra")
	if handled || !strings.Contains(got, "Skill body") || strings.Contains(got, "Wrong") {
		t.Fatalf("skill/template collision = %q, %v", got, handled)
	}
	if !reflect.DeepEqual(stages, []string{"extension:skill:inspect:extra", "input:/skill:inspect extra"}) {
		t.Fatalf("stage order = %#v", stages)
	}
}

func TestQueuedExpansionRejectsExtensionCommands(t *testing.T) {
	resolver := SlashResolver{
		ExtensionCommands: []SlashCommandInfo{{Name: "deploy", Source: SlashCommandExtension}},
		PromptTemplates:   []PromptTemplate{{Name: "review", Content: "Review $1"}},
	}
	if _, err := resolver.ExpandQueued("/deploy now"); err == nil || !strings.Contains(err.Error(), `"/deploy"`) {
		t.Fatalf("extension queue error = %v", err)
	}
	if got, err := resolver.ExpandQueued("/review file.go"); err != nil || got != "Review file.go" {
		t.Fatalf("template queue expansion = %q, %v", got, err)
	}
}
