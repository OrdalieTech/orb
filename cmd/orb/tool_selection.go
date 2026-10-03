package main

import "slices"

var defaultBuiltInTools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}
var defaultActiveTools = []string{"read", "bash", "edit", "write"}

// ResolveToolSelection applies upstream allowlist precedence and returns the
// valid active tools in CLI order.
func ResolveToolSelection(args CLIArgs, registeredTools []string) []string {
	var requested []string
	switch {
	case args.Tools != nil:
		requested = args.Tools
	case args.NoTools || args.NoBuiltinTools:
		requested = []string{}
	default:
		requested = defaultActiveTools
	}
	return slices.DeleteFunc(slices.Clone(requested), func(name string) bool {
		return !slices.Contains(registeredTools, name) || slices.Contains(args.ExcludeTools, name)
	})
}
