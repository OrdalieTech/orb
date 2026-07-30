package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/codingagent/extensions"
)

const runtimeUnavailableMessage = "JS extensions require Node.js ≥22.6 or Bun; set ORB_NODE to a Node executable if one is installed where orb cannot find it. Skills, prompt templates, MCP servers and built-in tools work without it"

// nodeOverrideEnv names the Node executable outright, for the setups no search
// can reach: a build outside every conventional prefix, or two installs where
// PATH picks the one the user does not want. It is authoritative — an override
// that cannot run is reported rather than silently replaced, because a silently
// ignored escape hatch is the failure it was meant to prevent. Set it to "none"
// to keep orb from running JavaScript extensions at all.
const nodeOverrideEnv = "ORB_NODE"

const nodeOverrideDisabled = "none"

// Node gained module.stripTypeScriptTypes in 22.13, which is what loader.mjs
// substitutes for Node's refusal to compile TypeScript under node_modules.
// Below it a dependency published as TypeScript cannot be compiled at all, so
// such a Node is used only when nothing better is installed.
const nodeTranspileMajor, nodeTranspileMinor = 22, 13

// A version-manager shim can block on a lock or on a TTY it will never be given,
// and DiscoverRuntime is often called on a context with no deadline, so every
// probe is bounded on its own.
const runtimeProbeTimeout = 5 * time.Second

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
	if override := strings.TrimSpace(os.Getenv(nodeOverrideEnv)); override != "" {
		if override == nodeOverrideDisabled {
			return Runtime{}, &RuntimeUnavailableError{}
		}
		version, err := commandVersion(ctx, override)
		if err == nil && !nodeAtLeast226(version) {
			err = fmt.Errorf("node %s is older than the required 22.6", version)
		}
		if err != nil {
			return Runtime{}, fmt.Errorf("%s=%s is not a usable node: %w", nodeOverrideEnv, override, err)
		}
		return nodeRuntime(ctx, override, version), nil
	}
	var best Runtime
	var rejected string
	for _, candidate := range nodeCandidates() {
		version, err := commandVersion(ctx, candidate)
		if err != nil {
			continue
		}
		if !nodeAtLeast226(version) {
			if versionNewer(version, rejected) {
				rejected = version
			}
			continue
		}
		// A Node that can compile every extension ends the search, so whatever the
		// user put on PATH wins whenever it is capable; the rest of the list exists
		// only to rescue a PATH that has no Node or carries one that cannot.
		if nodeAtLeast(version, nodeTranspileMajor, nodeTranspileMinor) {
			return nodeRuntime(ctx, candidate, version), nil
		}
		if versionNewer(version, best.Version) {
			best = Runtime{Name: "node", Version: version, Path: candidate}
		}
	}
	if best.Path != "" {
		best.Args = nodeRuntimeArgs(ctx, best.Path, best.Version)
		return best, nil
	}
	if path, err := exec.LookPath("bun"); err == nil {
		if version, versionErr := commandVersion(ctx, path); versionErr == nil {
			// Dependencies are materialized by an explicit, audited install step, so
			// Bun's implicit auto-install would fetch unresolved specifiers from npm
			// mid-session; Node has no such behaviour.
			return Runtime{Name: "bun", Version: version, Path: path, Args: []string{"--no-install"}}, nil
		}
	}
	return Runtime{}, &RuntimeUnavailableError{NodeVersion: rejected}
}

func nodeRuntime(ctx context.Context, path, version string) Runtime {
	return Runtime{Name: "node", Version: version, Path: path, Args: nodeRuntimeArgs(ctx, path, version)}
}

// nodeCandidates lists every Node worth probing, PATH first. exec.LookPath
// already skips a dangling symlink and a non-executable entry, so PATH yielding
// nothing is the ordinary state under a version manager that installs a shell
// function (nvm) or whose shims only exist in an interactive login shell (fnm,
// volta, asdf, mise): none of them put a Node on the PATH a spawned process
// inherits, but all of them keep the install itself at a stable location.
func nodeCandidates() []string {
	candidates := make([]string, 0, 8)
	seen := make(map[string]struct{})
	add := func(paths ...string) {
		for _, path := range paths {
			// Filters a dangling symlink and a directory named node as well, so a
			// broken PATH entry costs a stat rather than a failed spawn.
			if path == "" || !executableFile(path) {
				continue
			}
			key := path
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				key = resolved
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, path)
		}
	}
	// LookPath first, because it is the platform-correct answer, then every other
	// PATH entry: LookPath stops at the first match, so a shim that exits non-zero
	// because its manager is half-configured would otherwise shadow the working
	// Node sitting behind it. An empty entry means the working directory and is
	// skipped rather than executed.
	if path, err := exec.LookPath("node"); err == nil {
		add(path)
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory != "" {
			add(filepath.Join(directory, "node"))
		}
	}
	for _, pattern := range nodeSearchPatterns() {
		matches, err := filepath.Glob(pattern)
		if err == nil {
			add(matches...)
		}
	}
	return candidates
}

// Every pattern is POSIX and simply matches nothing on Windows, which lands with
// D8; none of them is wrong there, only inert.
// ponytail: the newest install wins within a version manager rather than the
// version the user selected, which orb cannot read without running the manager;
// upgrade path is honouring `.nvmrc`/`.tool-versions` when one is present.
func nodeSearchPatterns() []string {
	home, _ := os.UserHomeDir()
	patterns := make([]string, 0, 16)
	add := func(root string, parts ...string) {
		if root != "" {
			patterns = append(patterns, filepath.Join(append([]string{root}, parts...)...))
		}
	}
	add(versionManagerRoot(home, []string{"NVM_DIR"}, ".nvm"), "versions", "node", "*", "bin", "node")
	add(versionManagerRoot(home, []string{"FNM_DIR"}, ".local", "share", "fnm"), "node-versions", "*", "installation", "bin", "node")
	add(versionManagerRoot(home, nil, "Library", "Application Support", "fnm"), "node-versions", "*", "installation", "bin", "node")
	add(versionManagerRoot(home, []string{"VOLTA_HOME"}, ".volta"), "tools", "image", "node", "*", "bin", "node")
	add(versionManagerRoot(home, []string{"ASDF_DATA_DIR", "ASDF_DIR"}, ".asdf"), "installs", "nodejs", "*", "bin", "node")
	add(versionManagerRoot(home, []string{"MISE_DATA_DIR"}, ".local", "share", "mise"), "installs", "node", "*", "bin", "node")
	add(versionManagerRoot(home, nil, ".nodenv", "versions"), "*", "bin", "node")
	add(versionManagerRoot(home, []string{"N_PREFIX"}), "bin", "node")
	return append(patterns, nodeSystemSearchPatterns...)
}

// Overridden in tests, where a Node installed at a system prefix would otherwise
// decide the outcome.
var nodeSystemSearchPatterns = []string{
	"/opt/homebrew/opt/node@*/bin/node",
	"/usr/local/opt/node@*/bin/node",
	"/usr/local/n/versions/node/*/bin/node",
	"/opt/homebrew/bin/node",
	"/usr/local/bin/node",
	"/usr/bin/node",
	"/snap/bin/node",
}

func versionManagerRoot(home string, names []string, fallback ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	if home == "" || len(fallback) == 0 {
		return ""
	}
	return filepath.Join(append([]string{home}, fallback...)...)
}

func commandVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return parseRuntimeVersion(string(output))
}

// A shim prints its own chatter around the version it forwards ("Now using node
// v22.13.0"), so the first version-shaped token wins rather than all of stdout.
func parseRuntimeVersion(output string) (string, error) {
	for _, field := range strings.Fields(output) {
		candidate := strings.TrimPrefix(field, "v")
		if _, ok := versionOrder(candidate); ok {
			return candidate, nil
		}
	}
	return "", errors.New("empty runtime version")
}

// Node 22.7 added --experimental-transform-types and Node 26 removed it together
// with TypeScript transformation itself, so the flag is bounded on both sides.
// An unknown flag aborts Node with "bad option" instead of warning, which would
// kill the host outright, so the build in hand is asked rather than its version
// guessed — a vendor or nightly build need not follow the release timeline.
func nodeRuntimeArgs(ctx context.Context, path, version string) []string {
	args := []string{"--experimental-strip-types", "--disable-warning=ExperimentalWarning", "--disable-warning=MODULE_TYPELESS_PACKAGE_JSON"}
	if nodeAtLeast(version, 22, 7) && nodeAcceptsFlag(ctx, path, "--experimental-transform-types") {
		args = append(args, "--experimental-transform-types")
	}
	return args
}

func nodeAcceptsFlag(ctx context.Context, path, flag string) bool {
	ctx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, path, flag, "-e", "").Run() == nil
}

func nodeAtLeast226(version string) bool {
	return nodeAtLeast(version, 22, 6)
}

func nodeAtLeast(version string, requiredMajor, requiredMinor int) bool {
	order, ok := versionOrder(version)
	return ok && (order[0] > requiredMajor || order[0] == requiredMajor && order[1] >= requiredMinor)
}

func versionNewer(candidate, current string) bool {
	left, _ := versionOrder(candidate)
	right, _ := versionOrder(current)
	for index := range left {
		if left[index] != right[index] {
			return left[index] > right[index]
		}
	}
	return false
}

// versionOrder reports a dotted version as comparable numbers; ok is false
// unless a numeric major and minor are both present, which is what separates a
// version from the surrounding chatter of a shim.
func versionOrder(version string) ([3]int, bool) {
	if index := strings.IndexAny(version, "-+"); index >= 0 {
		version = version[:index]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return [3]int{}, false
	}
	var order [3]int
	for index := 0; index < 2; index++ {
		value, err := strconv.Atoi(parts[index])
		if err != nil {
			return [3]int{}, false
		}
		order[index] = value
	}
	if len(parts) > 2 {
		order[2], _ = strconv.Atoi(parts[2])
	}
	return order, true
}
