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
	// The wire list tracks DECISIONS.md "The compat kernel": every upstream
	// file that defines a format an external artifact observes — on-disk
	// ~/.pi files, session/event/RPC shapes, the extension host protocol,
	// skills/prompt-template/pi-package formats, print output, HTML export.
	wireFiles := []string{
		"packages/ai/src/types.ts",
		"packages/agent/src/types.ts",
		"packages/agent/src/harness/events.ts",
		"packages/agent/src/harness/skills.ts",
		"packages/agent/src/harness/prompt-templates.ts",
		"packages/coding-agent/src/core/messages.ts",
		"packages/coding-agent/src/core/session-manager.ts",
		"packages/coding-agent/src/core/settings-manager.ts",
		"packages/coding-agent/src/core/auth-storage.ts",
		"packages/coding-agent/src/core/models-store.ts",
		"packages/coding-agent/src/core/trust-manager.ts",
		"packages/coding-agent/src/core/project-trust.ts",
		"packages/coding-agent/src/core/keybindings.ts",
		"packages/coding-agent/src/core/pi-manifest.ts",
		"packages/coding-agent/src/core/skills.ts",
		"packages/coding-agent/src/core/prompt-templates.ts",
		"packages/coding-agent/src/modes/json-event.ts",
		"packages/coding-agent/src/modes/print-mode.ts",
	}
	if slices.Contains(wireFiles, filename) {
		return ClassWire
	}
	if strings.HasPrefix(filename, "packages/ai/src/api/") ||
		strings.HasPrefix(filename, "packages/coding-agent/src/core/extensions/") ||
		strings.HasPrefix(filename, "packages/coding-agent/src/core/export-html/") ||
		strings.Contains(filename, "packages/agent/src/harness/session/") ||
		filename == "packages/agent/src/harness/types.ts" ||
		strings.Contains(filename, "/modes/rpc") ||
		strings.Contains(filename, "rpc-protocol") ||
		strings.Contains(filename, "session-format") {
		return ClassWire
	}
	// API surfaces of the packages orb ports (ai, agent, coding-agent);
	// server/client/telemetry/tui are orb-owned or removed and carry nothing.
	apiFiles := []string{
		"packages/agent/src/agent-loop.ts",
		"packages/agent/src/agent.ts",
		"packages/agent/src/index.ts",
		"packages/agent/src/harness/agent-harness.ts",
		"packages/ai/src/models.ts",
		"packages/ai/src/index.ts",
		"packages/coding-agent/src/core/agent-session.ts",
		"packages/coding-agent/src/index.ts",
	}
	if slices.Contains(apiFiles, filename) {
		return ClassAPI
	}
	if strings.Contains(filename, "/src/auth/") ||
		strings.Contains(filename, "/src/providers/") ||
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
