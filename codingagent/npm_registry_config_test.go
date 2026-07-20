package codingagent

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for npm registry configuration: npm_config_registry and
// .npmrc registry= lines are honored instead of hard-coding
// registry.npmjs.org, and //host/:_authToken= lines pass through as bearer
// tokens.

func clearNpmRegistryEnv(t *testing.T) {
	t.Helper()
	t.Setenv("npm_config_registry", "")
	t.Setenv("NPM_CONFIG_REGISTRY", "")
}

func addFakeRegistryPackage(registry *fakeNpmRegistry, name string) {
	registry.add(fakeNpmPackage{name: name, version: "1.0.0", files: map[string]string{
		"package.json": packageJSONContent(name, "1.0.0"),
	}})
}

func assertInstalledFromRegistry(t *testing.T, manager *PackageManager, agentDir, name string) {
	t.Helper()
	if err := manager.Install("npm:"+name, false); err != nil {
		t.Fatal(err)
	}
	if !pathExists(filepath.Join(agentDir, "npm", "node_modules", name, "package.json")) {
		t.Fatalf("package %s not installed", name)
	}
}

func TestNpmRegistryHonorsEnvVar(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	addFakeRegistryPackage(registry, "pi-env-registry-pkg")
	manager, _, agentDir, _ := newTestPackageManager(t)
	clearNpmRegistryEnv(t)
	t.Setenv("npm_config_registry", registry.server.URL)

	// The default registry is never consulted: this package only exists on
	// the local httptest registry, so success proves the env var was honored.
	assertInstalledFromRegistry(t, manager, agentDir, "pi-env-registry-pkg")
}

func TestNpmRegistryHonorsUserNpmrc(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	addFakeRegistryPackage(registry, "pi-user-npmrc-pkg")
	manager, _, agentDir, _ := newTestPackageManager(t)
	clearNpmRegistryEnv(t)
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".npmrc"),
		"# comment\nregistry="+registry.server.URL+"/\n")

	assertInstalledFromRegistry(t, manager, agentDir, "pi-user-npmrc-pkg")
}

func TestNpmRegistryProjectNpmrcBeatsUserNpmrc(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	addFakeRegistryPackage(registry, "pi-project-npmrc-pkg")
	manager, cwd, agentDir, _ := newTestPackageManager(t)
	clearNpmRegistryEnv(t)
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".npmrc"), "registry=http://127.0.0.1:9/\n")
	writeTestFile(t, filepath.Join(cwd, ".npmrc"), "registry="+registry.server.URL+"\n")

	assertInstalledFromRegistry(t, manager, agentDir, "pi-project-npmrc-pkg")
}

func TestNpmRegistryPassesNpmrcAuthToken(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	registry.token = "sekret-token"
	addFakeRegistryPackage(registry, "pi-auth-pkg")
	manager, cwd, agentDir, _ := newTestPackageManager(t)
	clearNpmRegistryEnv(t)
	registryURL, err := url.Parse(registry.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(cwd, ".npmrc"),
		"registry="+registry.server.URL+"\n//"+registryURL.Host+"/:_authToken=sekret-token\n")

	// Both the packument fetch and the tarball download require the token.
	assertInstalledFromRegistry(t, manager, agentDir, "pi-auth-pkg")
}

func TestNpmRegistryDefaultsWithoutConfiguration(t *testing.T) {
	manager, _, _, _ := newTestPackageManager(t)
	clearNpmRegistryEnv(t)
	registry := manager.npmRegistry()
	if registry.baseURL != defaultNpmRegistry || registry.authToken != "" {
		t.Fatalf("registry = %+v", registry)
	}
}
