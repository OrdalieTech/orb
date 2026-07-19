package codingagent_test

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

type publicResourceLoader struct{}

func (*publicResourceLoader) GetExtensions() *extensions.Registry { return nil }
func (*publicResourceLoader) GetSkills() codingagent.ResourceSkillsResult {
	return codingagent.ResourceSkillsResult{}
}
func (*publicResourceLoader) GetPrompts() codingagent.ResourcePromptsResult {
	return codingagent.ResourcePromptsResult{}
}
func (*publicResourceLoader) GetThemes() codingagent.ResourceThemesResult {
	return codingagent.ResourceThemesResult{}
}
func (*publicResourceLoader) GetAgentsFiles() codingagent.ResourceAgentsFilesResult {
	return codingagent.ResourceAgentsFilesResult{}
}
func (*publicResourceLoader) GetSystemPrompt() *string        { return nil }
func (*publicResourceLoader) GetAppendSystemPrompt() []string { return nil }
func (*publicResourceLoader) ExtendResources(codingagent.ResourceExtensionPaths) {
}
func (*publicResourceLoader) Reload(context.Context, *codingagent.ResourceLoaderReloadOptions) error {
	return nil
}

func TestResourceLoaderPublicSurface(t *testing.T) {
	t.Helper()
	loader := &publicResourceLoader{}
	var resourceLoader codingagent.ResourceLoader = loader
	_ = resourceLoader.GetExtensions()
	_ = resourceLoader.GetSkills()
	_ = resourceLoader.GetPrompts()
	_ = resourceLoader.GetThemes()
	_ = resourceLoader.GetAgentsFiles()
	_ = resourceLoader.GetSystemPrompt()
	_ = resourceLoader.GetAppendSystemPrompt()
	resourceLoader.ExtendResources(codingagent.ResourceExtensionPaths{})
	_ = resourceLoader.Reload(context.Background(), nil)
	_ = codingagent.AgentSessionOptions{ResourceLoader: loader}
	_ = codingagent.AgentSessionServices{ResourceLoader: loader}
	_, _ = codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{CWD: "."})
}
