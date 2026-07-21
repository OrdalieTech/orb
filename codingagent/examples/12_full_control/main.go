// Full control: explicit model, settings, session, ResourceLoader, and tools.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

type fixedResourceLoader struct {
	registry     *extensions.Registry
	systemPrompt string
}

func (loader *fixedResourceLoader) GetExtensions() *extensions.Registry { return loader.registry }
func (*fixedResourceLoader) GetSkills() codingagent.ResourceSkillsResult {
	return codingagent.ResourceSkillsResult{Skills: []codingagent.Skill{}, Diagnostics: []codingagent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetPrompts() codingagent.ResourcePromptsResult {
	return codingagent.ResourcePromptsResult{Prompts: []codingagent.PromptTemplate{}, Diagnostics: []codingagent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetThemes() codingagent.ResourceThemesResult {
	return codingagent.ResourceThemesResult{Themes: []*modetheme.Theme{}, Diagnostics: []codingagent.ResourceDiagnostic{}}
}
func (*fixedResourceLoader) GetAgentsFiles() codingagent.ResourceAgentsFilesResult {
	return codingagent.ResourceAgentsFilesResult{AgentsFiles: []codingagent.ContextFile{}}
}
func (loader *fixedResourceLoader) GetSystemPrompt() *string                    { return &loader.systemPrompt }
func (*fixedResourceLoader) GetAppendSystemPrompt() []string                    { return []string{} }
func (*fixedResourceLoader) ExtendResources(codingagent.ResourceExtensionPaths) {}
func (*fixedResourceLoader) Reload(ctx context.Context, _ *codingagent.ResourceLoaderReloadOptions) error {
	return ctx.Err()
}

func main() {
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	agentDir := codingagent.DefaultAgentDir()
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
		Execute: func(_ context.Context, _ string, _ any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{
				Content: ai.ToolResultContent{&ai.TextContent{Text: "custom_info_result"}},
			}, nil
		},
	}

	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
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
		if end, ok := event.(codingagent.SessionAgentEndEvent); ok {
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

var _ codingagent.ResourceLoader = (*fixedResourceLoader)(nil)
