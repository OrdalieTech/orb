package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func prepareRuntimeEntry(agentDir string, runtime Runtime, entry extensionEntry) (extensionEntry, error) {
	entry.RuntimePath = ""
	entry.SourceRoot = ""
	entry.RuntimeRoot = ""
	if runtime.Name != "node" {
		return entry, nil
	}
	manifestPath, err := owningPackageJSON(entry.Path)
	if err != nil || manifestPath == "" {
		return entry, err
	}
	packageDir := filepath.Dir(manifestPath)
	nodeModulesDir := enclosingNodeModules(packageDir)
	if nodeModulesDir == "" {
		return entry, nil
	}
	relative, err := filepath.Rel(packageDir, entry.Path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return entry, fmt.Errorf("stage Node TypeScript entry %s: path escapes package", entry.Path)
	}
	digest := sha256.Sum256([]byte(packageDir))
	stageDir := filepath.Join(agentDir, "host", "entries", hex.EncodeToString(digest[:8]))
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return entry, fmt.Errorf("stage Node TypeScript entry %s: %w", entry.Path, err)
	}
	sourceLink := filepath.Join(stageDir, "source")
	if err := replaceExecutableLink(sourceLink, packageDir); err != nil {
		return entry, fmt.Errorf("stage Node TypeScript entry %s: %w", entry.Path, err)
	}
	if err := stageRuntimeDependencies(stageDir, packageDir, nodeModulesDir, manifestPath); err != nil {
		return entry, fmt.Errorf("stage Node TypeScript dependencies for %s: %w", entry.Path, err)
	}
	entry.RuntimePath = filepath.Join(sourceLink, relative)
	entry.SourceRoot = packageDir
	entry.RuntimeRoot = sourceLink
	return entry, nil
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

func stageRuntimeDependencies(stageDir, packageDir, nodeModulesDir, manifestPath string) error {
	modulesDir := filepath.Join(stageDir, "node_modules")
	packagesDir := filepath.Join(stageDir, "packages")
	if info, err := os.Lstat(modulesDir); err == nil && !info.IsDir() {
		if err := os.Remove(modulesDir); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(modulesDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(packagesDir, 0o700); err != nil {
		return err
	}
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest packageManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return err
	}
	for name := range manifest.Dependencies {
		parts := strings.Split(name, "/")
		target := resolveDependencyDirectory(packageDir, parts)
		if target != "" {
			if err := linkStagedRuntimePackage(modulesDir, packagesDir, name, target); err != nil {
				return err
			}
		}
	}
	for exposed, canonical := range runtimeSDKPackages {
		if _, declared := manifest.Dependencies[exposed]; declared && exposed != "@sinclair/typebox" {
			continue
		}
		target := resolveRuntimeSDK(nodeModulesDir, canonical)
		if target != "" {
			if err := linkStagedRuntimePackage(modulesDir, packagesDir, exposed, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func linkStagedRuntimePackage(modulesDir, packagesDir, name, target string) error {
	if err := linkRuntimePackage(modulesDir, name, target); err != nil {
		return err
	}
	return linkRuntimePackage(packagesDir, name, target)
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
