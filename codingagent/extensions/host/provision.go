package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/internal/filelock"
)

// SDK auto-provisioning. Upstream pi bundles the SDK with itself, so a loose
// extension file may import "@earendil-works/pi-ai" without declaring it
// anywhere. orb never borrows an installed pi's copy; instead, the first
// launch that needs the SDK installs the pinned version into orb's own user
// npm root, the same root managedSDKRoot resolves. Package-installed
// extensions declare their dependencies and never trigger this.
//
// ponytail: provisioning targets the user root only — a project-trusted
// install into the project root stays a manual step, because writing into a
// project's tree unprompted is not orb's call to make.

const sdkInstallTimeout = 3 * time.Minute

// sdkImportPattern matches an import of any package the SDK alias table
// serves, under either its current or historical scope.
var sdkImportPattern = regexp.MustCompile(`['"]@(?:earendil-works|mariozechner)/pi-`)

// runSDKInstall is swappable so tests provision without npm or network.
var runSDKInstall = func(ctx context.Context, npmPath, installRoot, spec string) error {
	ctx, cancel := context.WithTimeout(ctx, sdkInstallTimeout)
	defer cancel()
	// --legacy-peer-deps: installed extension packages carry conflicting SDK
	// peer ranges; this install pins one exact version regardless.
	command := exec.CommandContext(ctx, npmPath,
		"install", "--ignore-scripts", "--no-fund", "--no-audit", "--legacy-peer-deps", "--prefix", installRoot, spec)
	command.Dir = installRoot
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

// ensureSDKProvisioned runs between runtime resolution and host launch, so a
// first-ever JS extension with a bare SDK import loads on the very launch that
// discovered it. Every early return means "nothing to do": the install runs
// only when an entry provably needs the SDK and no copy is resolvable.
func (manager *Manager) ensureSDKProvisioned(ctx context.Context, runtime Runtime) {
	options := manager.options
	if options.SDKVersion == "" || os.Getenv(piSDKRootEnv) != "" || isSDKProvisionOffline() {
		return
	}
	if managedSDKRoot(options) != "" {
		return
	}
	needed := false
	for _, entry := range manager.entries {
		if entryNeedsSDK(entry.Path) {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	spec := piSDKPackage + "@" + options.SDKVersion
	installRoot := config.UserNpmInstallRoot(options.AgentDir)
	npmPath := npmNear(runtime)
	if npmPath == "" {
		manager.report(warningDiagnostic(fmt.Sprintf(
			"an extension imports the pi SDK, which is not installed and npm was not found to install it; run 'npm i --prefix %s %s' with any npm", installRoot, spec)))
		return
	}
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		manager.report(warningDiagnostic("pi SDK install: " + err.Error()))
		return
	}
	release, err := filelock.Acquire(filepath.Join(installRoot, "sdk-install"))
	if err != nil {
		manager.report(warningDiagnostic("pi SDK install: " + err.Error()))
		return
	}
	defer func() { _ = release() }()
	// Another orb may have provisioned while this one waited on the lock.
	if managedSDKRoot(options) != "" {
		return
	}
	if manager.options.Stderr != nil {
		_, _ = fmt.Fprintf(manager.options.Stderr, "Installing the pi SDK (%s) into %s — first extension importing it\n", spec, installRoot)
	}
	if err := runSDKInstall(ctx, npmPath, installRoot, spec); err != nil {
		manager.report(warningDiagnostic(fmt.Sprintf(
			"pi SDK install failed (%s); run 'npm i --prefix %s %s' manually: %s", npmPath, installRoot, spec, err)))
	}
}

// entryNeedsSDK reports whether an extension entry imports the SDK with no
// resolvable copy anywhere up its tree. Package-installed extensions commonly
// declare the SDK as a peerDependency - satisfied upstream by pi bundling it -
// which npm does not materialize, so resolvability is the only criterion.
func entryNeedsSDK(path string) bool {
	source, err := os.ReadFile(path)
	if err != nil || !sdkImportPattern.Match(source) {
		return false
	}
	for directory := filepath.Dir(path); ; {
		modules := filepath.Join(directory, "node_modules", "@earendil-works")
		for _, name := range [...]string{"pi-coding-agent", "pi-ai"} {
			if installedPackageName(filepath.Join(modules, name)) == "@earendil-works/"+name {
				return false
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return true
		}
		directory = parent
	}
}

// npmNear prefers the npm sitting beside the resolved runtime, so the install
// uses the same Node the extensions will run on; PATH is the fallback.
func npmNear(runtime Runtime) string {
	if runtime.Path != "" {
		sibling := filepath.Join(filepath.Dir(runtime.Path), "npm")
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
			return sibling
		}
	}
	found, err := exec.LookPath("npm")
	if err != nil {
		return ""
	}
	return found
}

func warningDiagnostic(message string) extensions.Diagnostic {
	return extensions.Diagnostic{Type: "warning", Message: message, Path: "<extension-host>"}
}

func isSDKProvisionOffline() bool {
	value := os.Getenv("PI_OFFLINE")
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}
