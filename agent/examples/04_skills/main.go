// Skills configuration through DefaultResourceLoader discovery and overrides.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	customSkill := agent.Skill{
		Name: "my-skill", Description: "Custom project instructions",
		FilePath: "/virtual/SKILL.md", BaseDir: "/virtual",
		SourceInfo: agent.SourceInfo{
			Path: "/virtual/SKILL.md", Source: "sdk", Scope: "temporary", Origin: "top-level",
		},
	}
	loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agent.DefaultAgentDir(),
		SkillsOverride: func(current agent.ResourceSkillsResult) agent.ResourceSkillsResult {
			filtered := make([]agent.Skill, 0, len(current.Skills)+1)
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
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: cwd, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
		ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Session created with filtered skills")
	result.Session.Dispose()
}
