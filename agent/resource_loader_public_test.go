package agent_test

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
)

type publicResourceLoader struct{}

func (*publicResourceLoader) GetExtensions() *extensions.Registry { return nil }
func (*publicResourceLoader) GetSkills() agent.ResourceSkillsResult {
	return agent.ResourceSkillsResult{}
}
func (*publicResourceLoader) GetPrompts() agent.ResourcePromptsResult {
	return agent.ResourcePromptsResult{}
}
func (*publicResourceLoader) GetThemes() agent.ResourceThemesResult {
	return agent.ResourceThemesResult{}
}
func (*publicResourceLoader) GetAgentsFiles() agent.ResourceAgentsFilesResult {
	return agent.ResourceAgentsFilesResult{}
}
func (*publicResourceLoader) GetSystemPrompt() *string { return nil }
func (*publicResourceLoader) GetSystemPromptSource() *agent.PromptSource {
	return nil
}
func (*publicResourceLoader) GetAppendSystemPrompt() []string { return nil }
func (*publicResourceLoader) GetAppendSystemPromptSources() []agent.PromptSource {
	return nil
}
func (*publicResourceLoader) ExtendResources(agent.ResourceExtensionPaths) {
}
func (*publicResourceLoader) Reload(context.Context, *agent.ResourceLoaderReloadOptions) error {
	return nil
}

func TestResourceLoaderPublicSurface(t *testing.T) {
	t.Helper()
	loader := &publicResourceLoader{}
	var resourceLoader agent.ResourceLoader = loader
	_ = resourceLoader.GetExtensions()
	_ = resourceLoader.GetSkills()
	_ = resourceLoader.GetPrompts()
	_ = resourceLoader.GetThemes()
	_ = resourceLoader.GetAgentsFiles()
	_ = resourceLoader.GetSystemPrompt()
	_ = resourceLoader.GetSystemPromptSource()
	_ = resourceLoader.GetAppendSystemPrompt()
	_ = resourceLoader.GetAppendSystemPromptSources()
	resourceLoader.ExtendResources(agent.ResourceExtensionPaths{})
	_ = resourceLoader.Reload(context.Background(), nil)
	_ = agent.AgentSessionOptions{ResourceLoader: loader}
	_ = agent.AgentSessionServices{ResourceLoader: loader}
	_, _ = agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{CWD: "."})
}
