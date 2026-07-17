package main

var defaultBuiltInTools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}
var defaultActiveTools = []string{"read", "bash", "edit", "write"}

type ToolSelection struct {
	Active []string
}

// ResolveBuiltInToolSelection applies CLI allow/deny rules to built-in tools.
func ResolveBuiltInToolSelection(args CLIArgs) ToolSelection {
	return ResolveToolSelection(args, defaultBuiltInTools)
}

// ResolveToolSelection applies upstream allowlist precedence and returns the
// valid active tools in CLI order.
func ResolveToolSelection(args CLIArgs, registeredTools []string) ToolSelection {
	var requested []string
	switch {
	case args.Tools != nil:
		requested = args.Tools
	case args.NoTools || args.NoBuiltinTools:
		requested = []string{}
	default:
		requested = defaultActiveTools
	}
	registered := make(map[string]struct{}, len(registeredTools))
	for _, name := range registeredTools {
		registered[name] = struct{}{}
	}
	excluded := make(map[string]struct{}, len(args.ExcludeTools))
	for _, name := range args.ExcludeTools {
		excluded[name] = struct{}{}
	}
	selection := ToolSelection{Active: make([]string, 0, len(requested))}
	for _, name := range requested {
		if _, exists := registered[name]; !exists {
			continue
		}
		if _, denied := excluded[name]; denied {
			continue
		}
		selection.Active = append(selection.Active, name)
	}
	return selection
}
