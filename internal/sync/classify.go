package upstreamsync

import (
	"path"
	"slices"
	"strings"
)

// Classification is by upstream path pattern only. The compat kernel (DECISIONS.md
// "The compat kernel") is what carries port obligation: wire-format and API-surface
// paths. Feature-only and docs paths are optional cherry-picks.

func classifyChange(current, old string) string {
	currentClass := classifyPath(current)
	oldClass := classifyPath(old)
	if classPriority(oldClass) > classPriority(currentClass) {
		return oldClass
	}
	return currentClass
}

func classifyPath(filename string) string {
	filename = strings.ToLower(path.Clean(filename))
	if filename == "." {
		return ClassFeature
	}
	if strings.HasSuffix(filename, ".md") || strings.Contains(filename, "/docs/") ||
		strings.HasSuffix(filename, "/readme") || strings.Contains(filename, "changelog") {
		return ClassDocs
	}
	wireFiles := []string{
		"packages/ai/src/types.ts",
		"packages/agent/src/types.ts",
		"packages/coding-agent/src/core/messages.ts",
		"packages/coding-agent/src/core/session-manager.ts",
		"packages/coding-agent/src/core/settings-manager.ts",
		"packages/coding-agent/src/core/auth-storage.ts",
		"packages/coding-agent/src/core/models-store.ts",
	}
	if slices.Contains(wireFiles, filename) {
		return ClassWire
	}
	if strings.HasPrefix(filename, "packages/ai/src/api/") ||
		strings.Contains(filename, "packages/agent/src/harness/session/") ||
		filename == "packages/agent/src/harness/types.ts" ||
		strings.Contains(filename, "/modes/rpc") ||
		strings.Contains(filename, "/modes/json") ||
		strings.Contains(filename, "rpc-protocol") ||
		strings.Contains(filename, "session-format") {
		return ClassWire
	}
	apiFiles := []string{
		"packages/agent/src/agent-loop.ts",
		"packages/agent/src/agent.ts",
		"packages/agent/src/harness/agent-harness.ts",
		"packages/ai/src/models.ts",
		"packages/coding-agent/src/core/agent-session.ts",
	}
	if slices.Contains(apiFiles, filename) {
		return ClassAPI
	}
	if strings.Contains(filename, "/src/auth/") ||
		strings.Contains(filename, "/src/providers/") ||
		strings.Contains(filename, "/core/extensions/types.ts") ||
		strings.HasSuffix(filename, "/src/index.ts") ||
		strings.Contains(filename, "/cli/args.ts") {
		return ClassAPI
	}
	return ClassFeature
}

func classPriority(classification string) int {
	switch classification {
	case ClassWire:
		return 4
	case ClassAPI:
		return 3
	case ClassDocs:
		return 2
	default:
		return 1
	}
}
