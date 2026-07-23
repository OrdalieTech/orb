package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes"
)

type packageCLIEnv struct {
	tempDir    string
	agentDir   string
	projectDir string
	packageDir string
}

func setupPackageCLI(t *testing.T) packageCLIEnv {
	t.Helper()
	tempDir := t.TempDir()
	env := packageCLIEnv{
		tempDir:    tempDir,
		agentDir:   filepath.Join(tempDir, "agent"),
		projectDir: filepath.Join(tempDir, "project"),
		packageDir: filepath.Join(tempDir, "local-package"),
	}
	for _, dir := range []string{env.agentDir, env.projectDir, env.packageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(tempDir, "home"))
	t.Setenv(config.EnvAgentDir, env.agentDir)
	t.Chdir(env.projectDir)
	return env
}

func runPackageCLI(t *testing.T, argv []string) (code int, stdout, stderr string) {
	t.Helper()
	var outBuffer, errBuffer bytes.Buffer
	code = runCLIWithDependencies(context.Background(), argv, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &outBuffer, Stderr: &errBuffer,
	}, cliDependencies{
		refreshModels: func(context.Context, string) error { return nil },
	})
	return code, outBuffer.String(), errBuffer.String()
}

func writeProjectPiSettings(t *testing.T, env packageCLIEnv, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(env.projectDir, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.projectDir, ".pi", "settings.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginsCLIListsAndTogglesUserSettings(t *testing.T) {
	env := setupPackageCLI(t)
	code, stdout, stderr := runPackageCLI(t, []string{"plugins", "list"})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "tasks\toff") || !strings.Contains(stdout, "websearch\toff") || !strings.Contains(stdout, "subagents\toff") || !strings.Contains(stdout, "permissions\toff") || !strings.Contains(stdout, "memory\toff") {
		t.Fatalf("initial list: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runPackageCLI(t, []string{"plugins", "enable", "tasks"})
	if code != 0 || stderr != "" {
		t.Fatalf("enable: code=%d stderr=%q", code, stderr)
	}
	contents, err := os.ReadFile(filepath.Join(env.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Plugins map[string]bool `json:"plugins"`
	}
	if err := json.Unmarshal(contents, &stored); err != nil || !stored.Plugins["tasks"] {
		t.Fatalf("settings = %s, error = %v", contents, err)
	}
	code, stdout, stderr = runPackageCLI(t, []string{"plugins", "list"})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "tasks\ton") {
		t.Fatalf("enabled list: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runPackageCLI(t, []string{"plugins", "disable", "tasks"})
	if code != 0 || stderr != "" {
		t.Fatalf("disable: code=%d stderr=%q", code, stderr)
	}
}

func TestPackageCLIInstallPersistsRelativeLocalPath(t *testing.T) {
	env := setupPackageCLI(t)
	relativePkgDir := filepath.Join(env.projectDir, "packages", "local-package")
	if err := os.MkdirAll(relativePkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runPackageCLI(t, []string{"install", "./packages/local-package"})
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	contents, err := os.ReadFile(filepath.Join(env.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Packages) != 1 {
		t.Fatalf("packages = %v", settings.Packages)
	}
	resolved := filepath.Clean(filepath.Join(env.agentDir, settings.Packages[0]))
	if resolved != relativePkgDir {
		t.Fatalf("stored %q resolves to %q, want %q", settings.Packages[0], resolved, relativePkgDir)
	}
}

func TestPackageCLIRemoveWithTrailingSlash(t *testing.T) {
	env := setupPackageCLI(t)
	if code, _, stderr := runPackageCLI(t, []string{"install", env.packageDir + "/"}); code != 0 {
		t.Fatalf("install failed: %s", stderr)
	}
	if code, _, stderr := runPackageCLI(t, []string{"remove", env.packageDir + "/"}); code != 0 {
		t.Fatalf("remove failed: %s", stderr)
	}
	contents, err := os.ReadFile(filepath.Join(env.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Packages) != 0 {
		t.Fatalf("packages = %v", settings.Packages)
	}
}

func TestPackageCLIListTrustFlow(t *testing.T) {
	t.Run("untrusted skips project settings", func(t *testing.T) {
		env := setupPackageCLI(t)
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		code, stdout, _ := runPackageCLI(t, []string{"list"})
		if code != 0 || !strings.Contains(stdout, "No packages installed.") || strings.Contains(stdout, "Project packages:") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("remembered trust", func(t *testing.T) {
		env := setupPackageCLI(t)
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		trusted := true
		if err := config.NewProjectTrustStore(env.agentDir).Set(env.projectDir, &trusted); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := runPackageCLI(t, []string{"list"})
		if code != 0 || !strings.Contains(stdout, "Project packages:") || !strings.Contains(stdout, "npm:@project/pkg") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("--no-approve overrides remembered trust", func(t *testing.T) {
		env := setupPackageCLI(t)
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		trusted := true
		if err := config.NewProjectTrustStore(env.agentDir).Set(env.projectDir, &trusted); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := runPackageCLI(t, []string{"list", "--no-approve"})
		if code != 0 || !strings.Contains(stdout, "No packages installed.") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("--approve grants trust", func(t *testing.T) {
		env := setupPackageCLI(t)
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		code, stdout, _ := runPackageCLI(t, []string{"list", "--approve"})
		if code != 0 || !strings.Contains(stdout, "Project packages:") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("defaultProjectTrust always", func(t *testing.T) {
		env := setupPackageCLI(t)
		if err := os.WriteFile(filepath.Join(env.agentDir, "settings.json"), []byte(`{"defaultProjectTrust":"always"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		code, stdout, _ := runPackageCLI(t, []string{"list"})
		if code != 0 || !strings.Contains(stdout, "Project packages:") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("trust.json overrides defaultProjectTrust", func(t *testing.T) {
		env := setupPackageCLI(t)
		if err := os.WriteFile(filepath.Join(env.agentDir, "settings.json"), []byte(`{"defaultProjectTrust":"always"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeProjectPiSettings(t, env, `{"packages":["npm:@project/pkg"]}`)
		untrusted := false
		if err := config.NewProjectTrustStore(env.agentDir).Set(env.projectDir, &untrusted); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := runPackageCLI(t, []string{"list"})
		if code != 0 || !strings.Contains(stdout, "No packages installed.") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})
}

func TestPackageCLIBlocksUntrustedLocalChanges(t *testing.T) {
	env := setupPackageCLI(t)
	writeProjectPiSettings(t, env, "{}")
	code, _, stderr := runPackageCLI(t, []string{"install", "-l", "./local-package"})
	if code != 1 || !strings.Contains(stderr, "Project is not trusted. Use --approve to modify local package config.") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestPackageCLILocalInstallInitializesFreshProjectSettings(t *testing.T) {
	env := setupPackageCLI(t)
	code, _, stderr := runPackageCLI(t, []string{"install", "-l", env.packageDir})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	contents, err := os.ReadFile(filepath.Join(env.projectDir, ".pi", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Packages) != 1 {
		t.Fatalf("packages = %v", settings.Packages)
	}
	resolved := filepath.Clean(filepath.Join(env.projectDir, ".pi", settings.Packages[0]))
	if canonical, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = canonical
	}
	want := env.packageDir
	if canonical, err := filepath.EvalSymlinks(want); err == nil {
		want = canonical
	}
	if resolved != want {
		t.Fatalf("stored %q resolves to %q, want %q", settings.Packages[0], resolved, want)
	}
}

func TestPackageCLIHelpAndErrors(t *testing.T) {
	setupPackageCLI(t)

	code, stdout, stderr := runPackageCLI(t, []string{"install", "--help"})
	if code != 0 || !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "pigo install <source> [-l]") || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, _, stderr = runPackageCLI(t, []string{"install", "--unknown"})
	if code != 1 || !strings.Contains(stderr, `Unknown option --unknown for "install".`) ||
		!strings.Contains(stderr, `pigo install <source> [-l] [--approve|--no-approve]`) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runPackageCLI(t, []string{"install"})
	if code != 1 || !strings.Contains(stderr, "Missing install source.") || !strings.Contains(stderr, "Usage: pigo install <source> [-l]") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runPackageCLI(t, []string{"remove", "npm:not-configured"})
	if code != 1 || !strings.Contains(stderr, "No matching package found for npm:not-configured") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestPackageCLIUpdateTargets(t *testing.T) {
	setupPackageCLI(t)

	// --models refreshes catalogs through the injected dependency.
	code, stdout, _ := runPackageCLI(t, []string{"update", "--models"})
	if code != 0 || stdout != "Model catalogs refreshed\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}

	// --models conflicts with other targets.
	code, _, stderr := runPackageCLI(t, []string{"update", "--models", "--self"})
	if code != 1 || !strings.Contains(stderr, "--models cannot be combined with --self, --extensions, --all, or --extension") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	// --extensions with nothing configured succeeds.
	code, stdout, _ = runPackageCLI(t, []string{"update", "--extensions"})
	if code != 0 || stdout != "All packages up to date.\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}

	// The unstamped test binary takes the dev-build route without a network call.
	code, stdout, stderr = runPackageCLI(t, []string{"update"})
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "pigo dev build — update check skipped.\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "Packages:") || strings.Contains(stdout, "extensions") {
		t.Fatalf("zero-package update output = %q", stdout)
	}
	for _, want := range []string{
		"pigo does not replace its running binary",
		"curl -fsSL https://raw.githubusercontent.com/OrdalieTech/pigo/main/scripts/install.sh | sh",
		"go install github.com/OrdalieTech/pigo/cmd/pigo@latest",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("update output missing %q: %q", want, stdout)
		}
	}

	// --offline is handled by the bare self-update route and never fetches.
	previousVersion := version
	version = "0.2.1"
	code, stdout, stderr = runPackageCLI(t, []string{"update", "--offline"})
	version = previousVersion
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "pigo v0.2.1 — offline mode — update check skipped.\n") {
		t.Fatalf("offline: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// The explicit self aliases use the same instruction-only route.
	for _, args := range [][]string{{"update", "--self"}, {"update", "self"}, {"update", "pigo"}} {
		code, stdout, stderr = runPackageCLI(t, args)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "Re-run the method used to install it:") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}

	// --force belonged to in-place self-update and must not imply support.
	code, _, stderr = runPackageCLI(t, []string{"update", "--force"})
	if code != 1 || !strings.Contains(stderr, `Unknown option --force for "update".`) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	// A missing positional source suggests the configured entry.
	if err := os.WriteFile(filepath.Join(os.Getenv(config.EnvAgentDir), "settings.json"), []byte(`{"packages":["npm:pi-formatter"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runPackageCLI(t, []string{"update", "pi-formatter"})
	if code != 1 || !strings.Contains(stderr, "Did you mean npm:pi-formatter?") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestSelfUpdateMessageSelection(t *testing.T) {
	instructions := `pigo does not replace its running binary.
Re-run the method used to install it:

  Installer: curl -fsSL https://raw.githubusercontent.com/OrdalieTech/pigo/main/scripts/install.sh | sh
  Go:        go install github.com/OrdalieTech/pigo/cmd/pigo@latest
`
	tests := []struct {
		name           string
		currentVersion string
		latestVersion  string
		checkErr       error
		offline        bool
		want           string
	}{
		{
			name:           "up to date",
			currentVersion: "0.3.0",
			latestVersion:  "v0.3.0",
			want:           "pigo v0.3.0 — already the latest version.\n",
		},
		{
			name:           "update available",
			currentVersion: "0.2.1",
			latestVersion:  "v0.3.0",
			want: "Update available: v0.2.1 -> v0.3.0\n" +
				"Release: https://github.com/OrdalieTech/pigo/releases/tag/v0.3.0\n" + instructions,
		},
		{
			name:           "check failed",
			currentVersion: "0.2.1",
			checkErr:       errors.New("network error"),
			want:           "pigo v0.2.1 — could not check for updates (network error).\n" + instructions,
		},
		{
			name:           "dev build",
			currentVersion: "0.1.0-dev",
			latestVersion:  "v0.3.0",
			want:           "pigo dev build — update check skipped.\n" + instructions,
		},
		{
			name:           "offline",
			currentVersion: "0.2.1",
			latestVersion:  "v0.3.0",
			offline:        true,
			want:           "pigo v0.2.1 — offline mode — update check skipped.\n" + instructions,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			printSelfUpdateStatus(&output, test.currentVersion, test.latestVersion, test.checkErr, test.offline)
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPackageUpdateMessageSelection(t *testing.T) {
	updates := []codingagent.PackageVersionUpdate{
		{PackageUpdate: codingagent.PackageUpdate{DisplayName: "alpha", Type: "npm"}, CurrentVersion: "1.0.0", LatestVersion: "1.1.0"},
		{PackageUpdate: codingagent.PackageUpdate{DisplayName: "beta", Type: "npm"}, CurrentVersion: "2.0.0", LatestVersion: "2.2.0"},
	}
	tests := []struct {
		name    string
		check   codingagent.PackageUpdateCheck
		skipped bool
		want    string
	}{
		{name: "none installed", check: codingagent.PackageUpdateCheck{}, want: ""},
		{name: "all current", check: codingagent.PackageUpdateCheck{Installed: 2}, want: "Packages: all 2 up to date.\n"},
		{name: "updates available", check: codingagent.PackageUpdateCheck{Installed: 2, Updates: updates}, want: "Package updates available: alpha v1.0.0 -> v1.1.0, beta v2.0.0 -> v2.2.0 — run pigo update --extensions\n"},
		{name: "check skipped", check: codingagent.PackageUpdateCheck{Installed: 2}, skipped: true, want: "Packages: 2 installed (update check skipped) — run pigo update --extensions\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			printPackageUpdateStatus(&output, test.check, test.skipped)
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpdatedPackagesMessageSelection(t *testing.T) {
	tests := []struct {
		name    string
		updates []codingagent.PackageVersionUpdate
		want    string
	}{
		{name: "unchanged", want: "All packages up to date.\n"},
		{name: "updated", updates: []codingagent.PackageVersionUpdate{{PackageUpdate: codingagent.PackageUpdate{DisplayName: "pi-hypa", Type: "npm"}, CurrentVersion: "0.4.0", LatestVersion: "0.5.0"}}, want: "pi-hypa v0.4.0 -> v0.5.0\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			printUpdatedPackages(&output, test.updates)
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPackageCLIUpdateUsesSavedTrustOnly(t *testing.T) {
	env := setupPackageCLI(t)
	// An untrusted project with project packages: update must not touch
	// project scope and must not prompt.
	writeProjectPiSettings(t, env, `{"packages":["npm:fake-package"]}`)
	code, _, stderr := runPackageCLI(t, []string{"update", "--extensions"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestConfigCLICommand(t *testing.T) {
	env := setupPackageCLI(t)

	code, stdout, _ := runPackageCLI(t, []string{"config", "--help"})
	if code != 0 || !strings.Contains(stdout, "pigo config [-l]") {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}

	writeProjectPiSettings(t, env, "{}")
	code, _, stderr := runPackageCLI(t, []string{"config", "-l"})
	if code != 1 || !strings.Contains(stderr, "Project is not trusted. Use --approve to modify local resource config.") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runPackageCLI(t, []string{"config", "--bogus"})
	if code != 1 || !strings.Contains(stderr, `Unknown option --bogus for "config".`) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestConfigCLIResolvesScopedResourcesAndRunsSelector(t *testing.T) {
	env := setupPackageCLI(t)
	globalExtension := filepath.Join(env.agentDir, "extensions", "global.ts")
	projectExtension := filepath.Join(env.projectDir, config.ConfigDirName, "extensions", "project.ts")
	for _, path := range []string{globalExtension, projectExtension} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("export default function () {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var captured modes.ConfigSelectorOptions
	runs := 0
	run := func(argv []string) (int, string) {
		var stderr bytes.Buffer
		code := runCLIWithDependencies(context.Background(), argv, cliStreams{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{
			refreshModels: func(context.Context, string) error { return nil },
			runConfig: func(_ context.Context, options modes.ConfigSelectorOptions) error {
				runs++
				captured = options
				return nil
			},
		})
		return code, stderr.String()
	}

	code, stderr := run([]string{"config", "--approve"})
	if code != 0 || stderr != "" || runs != 1 {
		t.Fatalf("approved config code=%d runs=%d stderr=%q", code, runs, stderr)
	}
	if captured.WriteScope != modes.ConfigWriteGlobal || !captured.ProjectModeAvailable || !captured.SettingsManager.IsProjectTrusted() {
		t.Fatalf("approved selector options = %#v", captured)
	}
	if hasResolvedPath(captured.ResolvedPaths.Global.Extensions, projectExtension) ||
		!hasResolvedPath(captured.ResolvedPaths.Global.Extensions, globalExtension) {
		t.Fatalf("global resolved paths = %#v", captured.ResolvedPaths.Global.Extensions)
	}
	if !hasResolvedPath(captured.ResolvedPaths.Project.Extensions, projectExtension) ||
		!hasResolvedPath(captured.ResolvedPaths.Project.Extensions, globalExtension) {
		t.Fatalf("project resolved paths = %#v", captured.ResolvedPaths.Project.Extensions)
	}

	code, stderr = run([]string{"config", "--local", "--approve"})
	if code != 0 || stderr != "" || captured.WriteScope != modes.ConfigWriteProject || runs != 2 {
		t.Fatalf("local config code=%d scope=%q runs=%d stderr=%q", code, captured.WriteScope, runs, stderr)
	}

	code, stderr = run([]string{"config", "--no-approve"})
	if code != 0 || stderr != "" || captured.ProjectModeAvailable || captured.SettingsManager.IsProjectTrusted() || runs != 3 {
		t.Fatalf("untrusted global config code=%d available=%v runs=%d stderr=%q", code, captured.ProjectModeAvailable, runs, stderr)
	}
	if captured.ResolvedPaths.Project != captured.ResolvedPaths.Global || hasResolvedPath(captured.ResolvedPaths.Project.Extensions, projectExtension) {
		t.Fatalf("untrusted project resolution was not the global resolution")
	}
}

func hasResolvedPath(resources []codingagent.ResolvedResource, path string) bool {
	for _, resource := range resources {
		if resource.Path == path {
			return true
		}
	}
	return false
}

func TestConfigCLITTYAndRunnerErrors(t *testing.T) {
	setupPackageCLI(t)
	runs := 0
	runner := func(context.Context, modes.ConfigSelectorOptions) error {
		runs++
		return nil
	}
	var stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"config"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr,
	}, cliDependencies{runConfig: runner})
	if code != 1 || runs != 0 || !strings.Contains(stderr.String(), "pigo config requires an interactive terminal") {
		t.Fatalf("non-TTY code=%d runs=%d stderr=%q", code, runs, stderr.String())
	}

	stderr.Reset()
	wantErr := errors.New("selector failed")
	code = runCLIWithDependencies(context.Background(), []string{"config"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{runConfig: func(context.Context, modes.ConfigSelectorOptions) error { return wantErr }})
	if code != 1 || !strings.Contains(stderr.String(), "Error: selector failed") {
		t.Fatalf("runner error code=%d stderr=%q", code, stderr.String())
	}
}
