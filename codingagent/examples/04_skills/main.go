// Skills configuration through DefaultResourceLoader discovery and overrides.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	customSkill := codingagent.Skill{
		Name: "my-skill", Description: "Custom project instructions",
		FilePath: "/virtual/SKILL.md", BaseDir: "/virtual",
		SourceInfo: codingagent.SourceInfo{
			Path: "/virtual/SKILL.md", Source: "sdk", Scope: "temporary", Origin: "top-level",
		},
	}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: codingagent.DefaultAgentDir(),
		SkillsOverride: func(current codingagent.ResourceSkillsResult) codingagent.ResourceSkillsResult {
			filtered := make([]codingagent.Skill, 0, len(current.Skills)+1)
			for _, skill := range current.Skills {
				if strings.Contains(skill.Name, "browser") || strings.Contains(skill.Name, "search") {
					filtered = append(filtered, skill)
				}
			}
			current.Skills = append(filtered, customSkill)
			return current
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := loader.Reload(ctx, nil); err != nil {
		log.Fatal(err)
	}

	loaded := loader.GetSkills()
	names := make([]string, 0, len(loaded.Skills))
	for _, skill := range loaded.Skills {
		names = append(names, skill.Name)
	}
	fmt.Println("Discovered skills:", names)
	if len(loaded.Diagnostics) > 0 {
		fmt.Println("Warnings:", loaded.Diagnostics)
	}

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Session created with filtered skills")
	result.Session.Dispose()
}
