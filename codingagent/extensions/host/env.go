package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	piSubagentBinaryEnv = "PI_SUBAGENT_PI_BINARY"
	piAgentDirEnv       = "PI_CODING_AGENT_DIR"
	piAgentMarkerEnv    = "PI_CODING_AGENT"
)

func prepareHostEnvironment(options Options, base []string, runtimePath string) ([]string, error) {
	agentDir := options.AgentDir
	if agentDir == "" {
		return nil, errors.New("extension host: agent directory is empty")
	}
	executable := options.OrbExecutable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("extension host: resolve orb executable: %w", err)
		}
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("extension host: resolve orb executable: %w", err)
	}
	shimDir := filepath.Join(agentDir, "host", "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		return nil, fmt.Errorf("extension host: create binary shim directory: %w", err)
	}
	shimPath := filepath.Join(shimDir, "pi")
	if err := replaceExecutableLink(shimPath, executable); err != nil {
		return nil, fmt.Errorf("extension host: materialize pi binary shim: %w", err)
	}

	environment := append([]string(nil), base...)
	pathValue := environmentValue(environment, "PATH")
	if pathValue == "" {
		pathValue = os.Getenv("PATH")
	}
	// The chosen runtime can sit outside PATH entirely — a version manager whose
	// shims a spawned process never inherits — while extensions spawn `node`,
	// `npx` and `npm` expecting the runtime they are already running on.
	if runtimePath != "" {
		pathValue = prependPath(filepath.Dir(runtimePath), pathValue)
	}
	environment = setEnvironmentValue(environment, "PATH", prependPath(shimDir, pathValue))
	environment = setEnvironmentValue(environment, piSubagentBinaryEnv, shimPath)
	environment = setEnvironmentValue(environment, piAgentDirEnv, agentDir)
	environment = setEnvironmentValue(environment, piAgentMarkerEnv, "true")
	// The pi SDK surface is served exclusively by the embedded orb-extension-sdk
	// (materialized in startLocked, named by ORB_EXTENSION_SDK_ROOT). orb never
	// looks for an installed pi and never borrows its bundled SDK: reading pi's
	// config files is the D4 compatibility promise; executing its code is not,
	// and the line stays clean.
	return environment, nil
}

func replaceExecutableLink(path, target string) error {
	if current, err := os.Readlink(path); err == nil {
		if current == target {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Lstat(path); statErr != nil || info.IsDir() {
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("refusing to replace directory %s", path)
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".pi-link-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := os.Symlink(target, temporaryPath); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func prependPath(directory, value string) string {
	if value == "" {
		return directory
	}
	for _, entry := range filepath.SplitList(value) {
		if entry == directory {
			return value
		}
	}
	return directory + string(os.PathListSeparator) + value
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}

func setEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}
