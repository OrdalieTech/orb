package runner_test

import (
	"strings"

	"github.com/OrdalieTech/orb/conformance/runner"
)

// P6: resource formats and precedence remain upstream-compatible, but Orb owns
// .orb instead of .pi. Translate only complete config-directory path components
// while replaying the frozen fixtures; never rewrite names, contents or goldens.
func orbConfigFixturePath(path string) string {
	return replaceConfigPathComponent(path, ".pi", ".orb")
}

func upstreamConfigFixturePath(path string) string {
	return replaceConfigPathComponent(path, ".orb", ".pi")
}

func replaceConfigPathComponent(path, from, to string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == from {
			parts[i] = to
		}
	}
	return strings.Join(parts, "/")
}

func normalizeOrbConfigFixturePath(path, root string) string {
	return upstreamConfigFixturePath(runner.NormalizeFixturePath(path, root))
}
