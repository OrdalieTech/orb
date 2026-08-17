package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type packageManifest struct {
	Dependencies map[string]string `json:"dependencies"`
}

func materializeDependencies(ctx context.Context, runtime Runtime, entryPath string, environment []string) error {
	manifestPath, err := owningPackageJSON(entryPath)
	if err != nil || manifestPath == "" {
		return err
	}
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var manifest packageManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if len(manifest.Dependencies) == 0 {
		return nil
	}
	packageDir := filepath.Dir(manifestPath)
	if dependenciesSatisfied(packageDir, manifest.Dependencies) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandPath, arguments, err := dependencyInstallCommand(runtime, environment)
	if err != nil {
		return fmt.Errorf("install dependencies for %s: %w", entryPath, err)
	}
	command := exec.CommandContext(ctx, commandPath, arguments...)
	command.Dir = packageDir
	command.Env = append([]string(nil), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("install dependencies for %s: %s", entryPath, detail)
	}
	if !dependenciesSatisfied(packageDir, manifest.Dependencies) {
		return fmt.Errorf("install dependencies for %s: installer completed but node_modules is incomplete", entryPath)
	}
	return nil
}

func owningPackageJSON(entryPath string) (string, error) {
	if entryPath == "" {
		return "", errors.New("extension dependency entry path is empty")
	}
	resolved, err := filepath.Abs(entryPath)
	if err != nil {
		return "", err
	}
	directory := resolved
	if info, statErr := os.Stat(resolved); statErr == nil && !info.IsDir() {
		directory = filepath.Dir(resolved)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	} else if errors.Is(statErr, os.ErrNotExist) {
		directory = filepath.Dir(resolved)
	}
	for {
		candidate := filepath.Join(directory, "package.json")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", nil
		}
		directory = parent
	}
}

func dependenciesSatisfied(packageDir string, dependencies map[string]string) bool {
	for name := range dependencies {
		parts := strings.Split(name, "/")
		if !validDependencyName(name, parts) {
			return false
		}
		if resolveDependencyDirectory(packageDir, parts) == "" {
			return false
		}
	}
	return true
}

func resolveDependencyDirectory(packageDir string, parts []string) string {
	for directory := filepath.Clean(packageDir); ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(append([]string{directory, "node_modules"}, parts...)...)
		if info, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
	}
}

func validDependencyName(name string, parts []string) bool {
	if strings.Contains(name, `\`) {
		return false
	}
	if len(parts) == 1 {
		return parts[0] != "" && parts[0] != "." && parts[0] != ".." && !strings.HasPrefix(parts[0], "@")
	}
	return len(parts) == 2 && strings.HasPrefix(parts[0], "@") && len(parts[0]) > 1 && parts[1] != "" && parts[1] != "." && parts[1] != ".."
}

func dependencyInstallCommand(runtime Runtime, environment []string) (string, []string, error) {
	if runtime.Name == "bun" {
		if runtime.Path == "" {
			return "", nil, errors.New("bun runtime path is empty")
		}
		return runtime.Path, []string{"install", "--production"}, nil
	}
	if sibling := filepath.Join(filepath.Dir(runtime.Path), "npm"); executableFile(sibling) {
		return sibling, []string{"install", "--omit=dev", "--no-audit", "--no-fund"}, nil
	}
	npm, err := lookPathInEnvironment("npm", environment)
	if err != nil {
		return "", nil, errors.New("npm is required because package.json dependencies are not materialized")
	}
	return npm, []string{"install", "--omit=dev", "--no-audit", "--no-fund"}, nil
}

func lookPathInEnvironment(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		if executableFile(name) {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	for _, directory := range filepath.SplitList(environmentValue(environment, "PATH")) {
		if candidate := filepath.Join(directory, name); executableFile(candidate) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
