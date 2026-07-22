package host

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

const runtimeUnavailableMessage = "JS extensions require Node.js ≥22.6 or Bun; skills, prompt templates, MCP servers and built-in tools work without it"

type Runtime struct {
	Name    string
	Version string
	Path    string
	Args    []string
}

type RuntimeUnavailableError struct {
	NodeVersion string
}

func (*RuntimeUnavailableError) Error() string { return runtimeUnavailableMessage }

func (err *RuntimeUnavailableError) Diagnostic() extensions.Diagnostic {
	return extensions.Diagnostic{Type: "error", Message: err.Error(), Path: "<extension-host>"}
}

func DiscoverRuntime(ctx context.Context) (Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var nodeVersion string
	if path, err := exec.LookPath("node"); err == nil {
		version, versionErr := commandVersion(ctx, path)
		if versionErr == nil {
			nodeVersion = strings.TrimPrefix(version, "v")
			if nodeAtLeast226(nodeVersion) {
				return Runtime{Name: "node", Version: nodeVersion, Path: path, Args: nodeRuntimeArgs(nodeVersion)}, nil
			}
		}
	}
	if path, err := exec.LookPath("bun"); err == nil {
		version, versionErr := commandVersion(ctx, path)
		if versionErr == nil {
			return Runtime{Name: "bun", Version: strings.TrimPrefix(version, "v"), Path: path}, nil
		}
	}
	return Runtime{}, &RuntimeUnavailableError{NodeVersion: nodeVersion}
}

func commandVersion(ctx context.Context, path string) (string, error) {
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", errors.New("empty runtime version")
	}
	return version, nil
}

func nodeRuntimeArgs(version string) []string {
	args := []string{"--experimental-strip-types", "--disable-warning=ExperimentalWarning"}
	if nodeAtLeast(version, 22, 7) {
		args = append(args, "--experimental-transform-types")
	}
	return append(args, "--preserve-symlinks")
}

func nodeAtLeast226(version string) bool {
	return nodeAtLeast(version, 22, 6)
}

func nodeAtLeast(version string, requiredMajor, requiredMinor int) bool {
	core := version
	if index := strings.IndexAny(core, "-+"); index >= 0 {
		core = core[:index]
	}
	parts := strings.Split(core, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > requiredMajor || major == requiredMajor && minor >= requiredMinor
}
