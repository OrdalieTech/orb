// Full control: explicit model, settings, session, ResourceLoader, and tools.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	modetheme "github.com/OrdalieTech/orb/agent/modes/theme"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

type fixedResourceLoader struct {
	registry     *extensions.Registry
	systemPrompt string
}

func (loader *fixedResourceLoader) GetExtensions() *extensions.Registry { return loader.registry }
func (*fixedResourceLoader) GetSkills() agent.ResourceSkillsResult {
	return agent.ResourceSkillsResult{Skills: []agent.Skill{}, Diagnostics: []agent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetPrompts() agent.ResourcePromptsResult {
	return agent.ResourcePromptsResult{Prompts: []agent.PromptTemplate{}, Diagnostics: []agent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetThemes() agent.ResourceThemesResult {
	return agent.ResourceThemesResult{Themes: []*modetheme.Theme{}, Diagnostics: []agent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetAgentsFiles() agent.ResourceAgentsFilesResult {
	return agent.ResourceAgentsFilesResult{AgentsFiles: []agent.ContextFile{}}
}
func (loader *fixedResourceLoader) GetSystemPrompt() *string { return &loader.systemPrompt }
func (*fixedResourceLoader) GetSystemPromptSource() *agent.PromptSource {
	return nil
}
func (*fixedResourceLoader) GetAppendSystemPrompt() []string { return []string{} }
func (*fixedResourceLoader) GetAppendSystemPromptSources() []agent.PromptSource {
	return []agent.PromptSource{}
}
func (*fixedResourceLoader) ExtendResources(agent.ResourceExtensionPaths) {}
func (*fixedResourceLoader) Reload(ctx context.Context, _ *agent.ResourceLoaderReloadOptions) error {
	return ctx.Err()
}

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	agentDir := agent.DefaultAgentDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		log.Fatal(err)
	}
	settings.SetCompactionEnabled(false)
	settings.SetRetryEnabled(true)

	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		log.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("main.go  go.mod  go.sum")})
	loader := &fixedResourceLoader{
		registry: extensions.NewRegistry(cwd),
		systemPrompt: "You are a minimal assistant.\n" +
			"Available: read, bash. Be concise.",
	}

	type customInfoInput struct {
		Topic string `json:"topic,omitempty" jsonschema:"description=Optional metadata topic"`
	}
	customInfoSchema, err := ai.JSONSchemaFrom[customInfoInput]()
	if err != nil {
		log.Fatal(err)
	}
	customInfo := extensions.ToolDefinition{
		Name: "custom_info", Label: "Custom Info", Description: "Returns custom metadata",
		Parameters: customInfoSchema,
		Execute: func(_ context.Context, _ string, _ any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			return engine.AgentToolResult{
				Content: ai.ToolResultContent{&ai.TextContent{Text: "custom_info_result"}},
			}, nil
		},
	}

	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Model: provider.GetModel(),
		ThinkingLevel: ai.ModelThinkingOff, StreamFn: provider.StreamSimple,
		Tools: []string{"read", "bash", "custom_info"}, CustomTools: []extensions.ToolDefinition{customInfo},
		ResourceLoader: loader, SessionManager: manager, Settings: settings,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer result.Session.Dispose()

	result.Session.Subscribe(func(event any) {
		if end, ok := event.(agent.SessionAgentEndEvent); ok {
			for _, message := range end.Messages {
				assistant, ok := message.(*ai.AssistantMessage)
				if !ok {
					continue
				}
				for _, block := range assistant.Content {
					if text, ok := block.(*ai.TextContent); ok {
						fmt.Print(text.Text)
					}
				}
			}
		}
	})
	if err := result.Session.Prompt(ctx, "List files in the current directory."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}

var _ agent.ResourceLoader = (*fixedResourceLoader)(nil)
