package tools

import "github.com/OrdalieTech/pigo/agent"

// ToolsOptions carries per-tool options for the bundle constructors
// (upstream core/tools ToolsOptions).
type ToolsOptions struct {
	Read  *ReadToolOptions
	Bash  *BashToolOptions
	Edit  *EditToolOptions
	Write *WriteToolOptions
	Grep  *GrepToolOptions
	Find  *FindToolOptions
	Ls    *LsToolOptions
}

// NewCodingTools mirrors upstream createCodingTools: read, bash, edit, write.
func NewCodingTools(cwd string, options *ToolsOptions) []agent.AgentTool {
	var resolved ToolsOptions
	if options != nil {
		resolved = *options
	}
	return []agent.AgentTool{
		NewReadTool(cwd, resolved.Read),
		NewBashTool(cwd, resolved.Bash),
		NewEditTool(cwd, resolved.Edit),
		NewWriteTool(cwd, resolved.Write),
	}
}

// NewReadOnlyTools mirrors upstream createReadOnlyTools: read, grep, find, ls.
func NewReadOnlyTools(cwd string, options *ToolsOptions) []agent.AgentTool {
	var resolved ToolsOptions
	if options != nil {
		resolved = *options
	}
	return []agent.AgentTool{
		NewReadTool(cwd, resolved.Read),
		NewGrepTool(cwd, resolved.Grep),
		NewFindTool(cwd, resolved.Find),
		NewLsTool(cwd, resolved.Ls),
	}
}
