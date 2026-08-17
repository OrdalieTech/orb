package tools

// ToolsOptions carries per-tool options for SDK tool construction
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
