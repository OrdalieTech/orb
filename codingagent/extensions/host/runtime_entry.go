package host

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Bun has no equivalent of Node's resolve hook — its runtime `Bun.plugin`
// onResolve never sees a nested import — but it does consult NODE_PATH, and
// only after the node_modules walk. The legacy pi SDK names are therefore
// exposed to Bun as thin wrapper packages re-exporting the materialized
// orb-extension-sdk modules. Because NODE_PATH only fills gaps, a real pi SDK
// installed in an extension's own tree still outranks these links under Bun:
// the loader.mjs no-real-SDK guarantee is Node-only, and the upgrade path is a
// Bun plugin that intercepts nested resolution.
func prepareRuntimeAliases(agentDir string, environment []string) ([]string, error) {
	root := environmentValue(environment, extensionSDKRootEnv)
	if root == "" {
		return environment, nil
	}
	aliasDir := filepath.Join(agentDir, "host", "aliases")
	for exposed, subpaths := range runtimeSDKPackages {
		if err := writeRuntimeSDKWrapper(aliasDir, root, exposed, subpaths); err != nil {
			return nil, fmt.Errorf("extension host: link SDK alias %s: %w", exposed, err)
		}
	}
	return setEnvironmentValue(environment, "NODE_PATH", prependPath(aliasDir, environmentValue(environment, "NODE_PATH"))), nil
}

// runtimeSDKAIExports mirrors the pi-ai subpaths loader.mjs serves: the root
// is upstream's index surface, "/compat" its superset with the legacy global
// API, matching the upstream exports map.
var runtimeSDKAIExports = map[string]string{
	".":               "ai.mjs",
	"./compat":        "ai-compat.mjs",
	"./oauth":         "ai-oauth.mjs",
	"./providers/all": "ai-providers-all.mjs",
}

var runtimeSDKPackages = map[string]map[string]string{
	"@earendil-works/pi-coding-agent": {".": "coding-agent.mjs"},
	"@earendil-works/pi-agent-core":   {".": "agent-core.mjs"},
	"@earendil-works/pi-ai":           runtimeSDKAIExports,
	"@earendil-works/pi-tui":          {".": "tui.mjs"},
	"@mariozechner/pi-coding-agent":   {".": "coding-agent.mjs"},
	"@mariozechner/pi-agent-core":     {".": "agent-core.mjs"},
	"@mariozechner/pi-ai":             runtimeSDKAIExports,
	"@mariozechner/pi-tui":            {".": "tui.mjs"},
	"pi":                              {".": "coding-agent.mjs"},
	"pi-coding-agent":                 {".": "coding-agent.mjs"},
	"pi-ai":                           runtimeSDKAIExports,
	"pi-tui":                          {".": "tui.mjs"},
}

// writeRuntimeSDKWrapper lays down one wrapper package: a package.json exports
// map plus a re-export module per subpath, each pointing into the materialized
// SDK root by absolute file URL. The wrappers are tiny and content-derived from
// the (content-addressed) root, so they are simply rewritten on every start.
func writeRuntimeSDKWrapper(aliasDir, sdkRoot, exposed string, subpaths map[string]string) error {
	parts := strings.Split(exposed, "/")
	if !validDependencyName(exposed, parts) {
		return fmt.Errorf("invalid runtime package name %q", exposed)
	}
	packageDir := filepath.Join(append([]string{aliasDir}, parts...)...)
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		return err
	}
	exports := make([]string, 0, len(subpaths))
	for subpath, module := range subpaths {
		wrapper := runtimeSDKWrapperFile(subpath)
		target := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(sdkRoot, module))}
		source := fmt.Sprintf("export * from %q;\n", target.String())
		if err := os.WriteFile(filepath.Join(packageDir, wrapper), []byte(source), 0o600); err != nil {
			return err
		}
		exports = append(exports, fmt.Sprintf("%q:%q", subpath, "./"+wrapper))
	}
	// Deterministic manifest bytes keep repeated starts byte-identical.
	sort.Strings(exports)
	manifest := fmt.Sprintf(`{"name":%q,"type":"module","exports":{%s}}`+"\n", exposed, strings.Join(exports, ","))
	return os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(manifest), 0o600)
}

// runtimeSDKWrapperFile flattens a subpath into a wrapper filename: "." becomes
// index.mjs, "./providers/all" becomes providers-all.mjs.
func runtimeSDKWrapperFile(subpath string) string {
	if subpath == "." {
		return "index.mjs"
	}
	return strings.ReplaceAll(strings.TrimPrefix(subpath, "./"), "/", "-") + ".mjs"
}
