package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Bun has no equivalent of Node's resolve hook — its runtime `Bun.plugin`
// onResolve never sees a nested import — but it does consult NODE_PATH, and only
// after the node_modules walk, so a real install always wins and these links can
// merely fill gaps. Node ignores NODE_PATH for ESM, so this cannot reach it.
// ponytail: NODE_PATH links a whole package, so the root-to-"/compat" redirect
// loader.mjs performs has no Bun counterpart; upgrade path is a Bun plugin API
// that intercepts nested resolution.
func prepareRuntimeAliases(agentDir string, environment []string) ([]string, error) {
	root := environmentValue(environment, piSDKRootEnv)
	if root == "" {
		return environment, nil
	}
	aliasDir := filepath.Join(agentDir, "host", "aliases")
	if err := os.MkdirAll(aliasDir, 0o700); err != nil {
		return nil, fmt.Errorf("extension host: create alias directory: %w", err)
	}
	for exposed, canonical := range runtimeSDKPackages {
		target := resolveRuntimeSDK(filepath.Join(root, "node_modules"), canonical)
		if target == "" {
			target = resolveRuntimeSDK(enclosingNodeModules(root), canonical)
		}
		if target == "" {
			continue
		}
		if err := linkRuntimePackage(aliasDir, exposed, target); err != nil {
			return nil, fmt.Errorf("extension host: link SDK alias %s: %w", exposed, err)
		}
	}
	return setEnvironmentValue(environment, "NODE_PATH", prependPath(aliasDir, environmentValue(environment, "NODE_PATH"))), nil
}

var runtimeSDKPackages = map[string]string{
	"@earendil-works/pi-coding-agent": "@earendil-works/pi-coding-agent",
	"@earendil-works/pi-agent-core":   "@earendil-works/pi-agent-core",
	"@earendil-works/pi-ai":           "@earendil-works/pi-ai",
	"@earendil-works/pi-tui":          "@earendil-works/pi-tui",
	"@mariozechner/pi-coding-agent":   "@earendil-works/pi-coding-agent",
	"@mariozechner/pi-ai":             "@earendil-works/pi-ai",
	"@mariozechner/pi-tui":            "@earendil-works/pi-tui",
	"@sinclair/typebox":               "typebox",
	"pi":                              "@earendil-works/pi-coding-agent",
	"pi-coding-agent":                 "@earendil-works/pi-coding-agent",
	"pi-ai":                           "@earendil-works/pi-ai",
	"pi-tui":                          "@earendil-works/pi-tui",
}

func resolveRuntimeSDK(nodeModulesDir, name string) string {
	parts := strings.Split(name, "/")
	codingAgent := filepath.Join(nodeModulesDir, "@earendil-works", "pi-coding-agent")
	for _, candidate := range []string{
		filepath.Join(append([]string{codingAgent, "node_modules"}, parts...)...),
		filepath.Join(append([]string{nodeModulesDir}, parts...)...),
	} {
		if info, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func linkRuntimePackage(modulesDir, name, target string) error {
	parts := strings.Split(name, "/")
	if !validDependencyName(name, parts) {
		return fmt.Errorf("invalid runtime package name %q", name)
	}
	path := filepath.Join(append([]string{modulesDir}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return replaceExecutableLink(path, target)
}

func enclosingNodeModules(path string) string {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if filepath.Base(current) == "node_modules" {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}
