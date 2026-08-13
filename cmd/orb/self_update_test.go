package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/orbalogo"
)

var newOrb = []byte("#!/bin/sh\necho orb 0.5.0\n")

type tarEntry struct {
	name     string
	body     []byte
	typeflag byte
	link     string
}

func buildArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gzipWriter := gzip.NewWriter(&raw)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.body)), Typeflag: tar.TypeReg, Linkname: entry.link}
		if entry.typeflag != 0 {
			header.Typeflag, header.Size = entry.typeflag, 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := errors.Join(tarWriter.Close(), gzipWriter.Close()); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// release is the server side of an upgrade: the two stable URLs scripts/install.sh builds, served
// over TLS because orb refuses plain HTTP.
type release struct {
	tag                                     string // "" serves v0.5.0
	archive                                 []byte // nil serves a LICENSE + orb archive
	checksums                               string // "" serves the correct line for the archive
	metadataHits, checksumHits, archiveHits int
}

// installedOrb lays out a realistic install: a canonical regular file plus the symlink on PATH.
func installedOrb(t *testing.T, mode os.FileMode) (dir, canonical, link string) {
	t.Helper()
	dir = t.TempDir()
	canonical = filepath.Join(dir, "orb")
	if err := os.WriteFile(canonical, []byte("original"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(canonical, mode); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(t.TempDir(), "orb")
	if err := os.Symlink(canonical, link); err != nil {
		t.Fatal(err)
	}
	return dir, canonical, link
}

func updaterFor(t *testing.T, currentVersion string, state *release) selfUpdater {
	t.Helper()
	if state.tag == "" {
		state.tag = "v0.5.0"
	}
	if state.archive == nil {
		state.archive = buildArchive(t, tarEntry{name: "LICENSE", body: []byte("MIT")}, tarEntry{name: "orb", body: newOrb})
	}
	asset := fmt.Sprintf("orb_%s_%s_%s.tar.gz", strings.TrimPrefix(state.tag, "v"), runtime.GOOS, runtime.GOARCH)
	if state.checksums == "" {
		sum := sha256.Sum256(state.archive)
		state.checksums = hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases/latest":
			state.metadataHits++
			_, _ = io.WriteString(writer, `{"tag_name":"`+state.tag+`"}`)
		case "/download/" + state.tag + "/checksums.txt":
			state.checksumHits++
			_, _ = io.WriteString(writer, state.checksums)
		case "/download/" + state.tag + "/" + asset:
			state.archiveHits++
			_, _ = writer.Write(state.archive)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return selfUpdater{
		currentVersion: currentVersion,
		releaseURL:     server.URL + "/releases/latest",
		releaseBase:    server.URL + "/download",
		client:         server.Client(),
		// Resolving the running binary is a filesystem effect, so only a test that installs one
		// overrides this; every other route proves it never got that far.
		executable: func() (string, error) {
			t.Error("the updater resolved the running binary")
			return "", errors.New("must not be called")
		},
		resolveLinks: filepath.EvalSymlinks,
	}
}

func assertOnlyOrb(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "orb" {
		t.Fatalf("install directory %s holds %v (%v), want only orb", dir, entries, err)
	}
}

// A skipped route never resolves the binary (updaterFor's executable fails the test) and asks
// GitHub for nothing beyond the version it needs to compare.
func TestSelfUpdateSkipsWithoutTouchingDisk(t *testing.T) {
	tests := []struct {
		version      string
		offline      bool
		metadataHits int
		want         string
	}{
		{version: "dev", want: "development build"},
		{version: "0.4.15", offline: true, want: "offline"},
		{version: "0.5.0", metadataHits: 1, want: "already current ✓"},
		{version: "0.6.0", metadataHits: 1, want: "already current ✓"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			state := &release{}
			updater := updaterFor(t, test.version, state)
			updater.offline = test.offline
			var output bytes.Buffer
			if code := updater.run(context.Background(), &output); code != 0 {
				t.Fatalf("code = %d, output = %q", code, output.String())
			}
			want := fmt.Sprintf("Orb update\n\n  %s\n     │\n     └─ %s\n", plainVersion(test.version), test.want)
			if output.String() != want {
				t.Fatalf("output = %q, want %q", output.String(), want)
			}
			if state.metadataHits != test.metadataHits || state.checksumHits != 0 || state.archiveHits != 0 {
				t.Fatalf("requests: metadata=%d checksums=%d archive=%d, want metadata=%d and nothing else",
					state.metadataHits, state.checksumHits, state.archiveHits, test.metadataHits)
			}
		})
	}
}

func TestUpdateAnimationIsTTYOnly(t *testing.T) {
	var output bytes.Buffer
	stop := startUpdateAnimation(&output, false, "checking release")
	stop()
	if output.Len() != 0 {
		t.Fatalf("non-TTY animation = %q", output.String())
	}

	stop = startUpdateAnimation(&output, true, "checking release")
	stop()
	if want := "     ·   checking release\r\x1b[2K"; output.String() != want {
		t.Fatalf("TTY animation = %q, want %q", output.String(), want)
	}
}

// TERM=dumb is a TTY that cannot address the cursor, so an animated route has to stay plain.
func TestSelfUpdateWritesNoEscapesOnADumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	var output bytes.Buffer
	if code := runSelfUpdate(context.Background(), &output, true, true); code != 0 {
		t.Fatalf("code = %d, output = %q", code, output.String())
	}
	if strings.Contains(output.String(), "\x1b") {
		t.Fatalf("output = %q, want no escape sequences", output.String())
	}
}

func TestLogoRevealDrawsSampledStagesInPlace(t *testing.T) {
	var output bytes.Buffer
	revealLogo(&output, true, 0)
	got := output.String()
	if rows := strings.Count(got, "\r\x1b[2K"); rows != len(updateRevealStages)*orbalogo.Height {
		t.Fatalf("cleared rows = %d, want %d", rows, len(updateRevealStages)*orbalogo.Height)
	}
	if up := strings.Count(got, fmt.Sprintf("\x1b[%dA", orbalogo.Height)); up != len(updateRevealStages)-1 {
		t.Fatalf("canvas-height cursor-up redraws = %d, want %d", up, len(updateRevealStages)-1)
	}
	last := orbalogo.Frame(orbalogo.FrameCount - 1)[orbalogo.Height-1]
	if want := "\r\x1b[2K" + last + "\r\n\r\n"; !strings.HasSuffix(got, want) {
		t.Fatalf("reveal ends with %q, want the completed mark and a blank line", got)
	}
}

func TestSelfUpdateReplacesCanonicalBinary(t *testing.T) {
	dir, canonical, link := installedOrb(t, 0o700)
	state := &release{}
	updater := updaterFor(t, "0.4.15", state)
	updater.executable = func() (string, error) { return link, nil }
	var output bytes.Buffer
	if code := updater.run(context.Background(), &output); code != 0 {
		t.Fatalf("code = %d, output = %q", code, output.String())
	}
	want := "Orb update\n\n  0.4.15\n     │\n     ● archive verified\n     │\n     ● binary replaced\n     │\n  0.5.0 ✓\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if state.archiveHits != 1 {
		t.Fatalf("archive downloads = %d", state.archiveHits)
	}
	if contents, err := os.ReadFile(canonical); err != nil || !bytes.Equal(contents, newOrb) {
		t.Fatalf("binary = %q, %v", contents, err)
	}
	// The mode is the target's, not the archive's, and the PATH symlink still resolves to the file
	// that was replaced in place.
	info, err := os.Stat(canonical)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	if resolved, err := os.Stat(link); err != nil || !os.SameFile(info, resolved) {
		t.Fatalf("link resolves to %v, %v", resolved, err)
	}
	assertOnlyOrb(t, dir)
}

func TestSelfUpdateRejectsBadReleasesAndRollsBack(t *testing.T) {
	asset := fmt.Sprintf("orb_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	good := sha256.Sum256(buildArchive(t, tarEntry{name: "orb", body: newOrb}))
	line := func(digest, name string) string { return digest + "  " + name + "\n" }
	tests := []struct {
		name    string
		state   release
		managed bool
		wantErr string
	}{
		{name: "checksum mismatch", state: release{checksums: line(strings.Repeat("ab", 32), asset)}, wantErr: "failed its sha256 checksum"},
		{name: "duplicate checksum line", state: release{checksums: line(hex.EncodeToString(good[:]), asset) + line(strings.Repeat("ab", 32), asset)}, wantErr: "more than once"},
		{name: "malformed checksum line", state: release{checksums: line("not-a-digest", asset)}, wantErr: "malformed sha256"},
		{name: "no checksum line for the asset", state: release{checksums: line(strings.Repeat("ab", 32), "orb_0.5.0_source.tar.gz")}, wantErr: "no sha256"},
		{name: "archive is not gzip", state: release{archive: []byte("orb_0.5.0.tar.gz but not really")}, wantErr: "not gzip"},
		{name: "missing orb entry", state: release{archive: buildArchive(t, tarEntry{name: "README.md", body: []byte("hi")})}, wantErr: "no orb entry"},
		{name: "nested orb entry", state: release{archive: buildArchive(t, tarEntry{name: "dist/orb", body: newOrb})}, wantErr: "no orb entry"},
		{name: "duplicate orb entry", state: release{archive: buildArchive(t, tarEntry{name: "orb", body: newOrb}, tarEntry{name: "orb", body: newOrb})}, wantErr: "more than one orb entry"},
		{name: "orb entry is a link", state: release{archive: buildArchive(t, tarEntry{name: "orb", typeflag: tar.TypeSymlink, link: "/etc/passwd"})}, wantErr: "not a regular file"},
		{name: "tag is not a plain version", state: release{tag: "v0.5.0+/../evil"}, wantErr: "not a plain version"},
		{name: "install belongs to a package manager", managed: true, wantErr: "managed by its package manager"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, canonical, link := installedOrb(t, 0o755)
			updater := updaterFor(t, "0.4.15", &test.state)
			updater.executable = func() (string, error) { return link, nil }
			if test.managed {
				updater.executable = func() (string, error) { return "/nix/store/abc-orb/bin/orb", nil }
				updater.resolveLinks = func(value string) (string, error) { return value, nil }
			}
			var output bytes.Buffer
			if code := updater.run(context.Background(), &output); code != 1 {
				t.Fatalf("code = %d, output = %q", code, output.String())
			}
			if !strings.Contains(output.String(), "     × ") || !strings.Contains(output.String(), test.wantErr) {
				t.Fatalf("output = %q, want a failure mentioning %q", output.String(), test.wantErr)
			}
			if !strings.HasSuffix(output.String(), "     │\n  unchanged\n") {
				t.Fatalf("output = %q, want an unchanged terminal state", output.String())
			}
			contents, err := os.ReadFile(canonical)
			if err != nil || string(contents) != "original" {
				t.Fatalf("binary = %q, %v", contents, err)
			}
			if info, err := os.Stat(canonical); err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("mode = %v, %v", info.Mode(), err)
			}
			assertOnlyOrb(t, dir)
		})
	}
}

// The metadata request is redirected too, and a hop off HTTPS has to die there — before anything
// resolves the running binary.
func TestSelfUpdateRefusesAMetadataRedirectOffHTTPS(t *testing.T) {
	// A reachable plain-HTTP endpoint serving a newer tag: following the redirect would resolve the
	// binary, so updaterFor's executable turns that into a failure.
	plain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"tag_name":"v9.9.9"}`)
	}))
	t.Cleanup(plain.Close)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, plain.URL+"/releases/latest", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	updater := updaterFor(t, "0.4.15", &release{})
	updater.releaseURL = server.URL + "/releases/latest"
	var output bytes.Buffer
	if code := updater.run(context.Background(), &output); code != 1 {
		t.Fatalf("code = %d, output = %q", code, output.String())
	}
	if !strings.Contains(output.String(), "     × ") || !strings.HasSuffix(output.String(), "     │\n  unchanged\n") {
		t.Fatalf("output = %q, want a failure", output.String())
	}
}

func TestManagedInstallCoversTheStoresThatOwnTheirFiles(t *testing.T) {
	for canonical, want := range map[string]bool{
		"/nix/store/9k1abc-orb-0.5.0/bin/orb": true,
		"/snap/orb/current/bin/orb":           true,
		"/opt/homebrew/bin/orb":               true,
		"/home/linuxbrew/.linuxbrew/bin/orb":  true,
		"/usr/local/Cellar/orb/0.5.0/bin/orb": true, // Intel Homebrew
		"/usr/local/Caskroom/orb/0.5.0/orb":   true,
		"/usr/local/bin/orb":                  false,
		"/home/lea/.local/share/orb/bin/orb":  false,
		"/home/lea/snap-experiments/bin/orb":  false,
	} {
		if got := managedInstall(canonical); got != want {
			t.Errorf("managedInstall(%q) = %v, want %v", canonical, got, want)
		}
	}
}

// swapBinary owns the last-moment guard; drive it directly because it fires between staging and
// rename, after another installer has already written the target.
func TestSwapBinaryRefusesATargetThatChangedWhileStaged(t *testing.T) {
	dir, canonical, _ := installedOrb(t, 0o755)
	before, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("someone else got here first"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapErr := swapBinary(canonical, newOrb, before)
	if swapErr == nil || !strings.Contains(swapErr.Error(), "changed while the update was staged") {
		t.Fatalf("err = %v", swapErr)
	}
	if contents, _ := os.ReadFile(canonical); string(contents) != "someone else got here first" {
		t.Fatalf("binary = %q", contents)
	}
	assertOnlyOrb(t, dir)
}

// A `go install ...@latest` binary carries no ldflags, only build info.
func TestBuildVersionRecoversTheModuleVersion(t *testing.T) {
	orb := func(moduleVersion string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: orbModulePath, Version: moduleVersion}}
	}
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{name: "go install stamps the tag", stamped: "dev", info: orb("v0.5.0"), ok: true, want: "v0.5.0"},
		{name: "local build has no version", stamped: "dev", info: orb("(devel)"), ok: true, want: "dev"},
		{name: "no build info at all", stamped: "dev", ok: false, want: "dev"},
		{name: "another module embeds orb", stamped: "dev", info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/fork", Version: "v9.9.9"}}, ok: true, want: "dev"},
		{name: "release ldflags win", stamped: "0.4.15", info: orb("v0.5.0"), ok: true, want: "0.4.15"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := buildVersion(test.stamped, test.info, test.ok); got != test.want {
				t.Fatalf("buildVersion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpdateCommandRouting(t *testing.T) {
	setupPackageCLI(t)
	t.Setenv("PI_OFFLINE", "")
	run := func(argv ...string) (code int, stdout string, selfCalls int, offline bool) {
		var output bytes.Buffer
		code = runCLIWithDependencies(context.Background(), argv, cliStreams{Stdin: strings.NewReader(""), Stdout: &output, Stderr: &output}, cliDependencies{
			refreshModels: func(context.Context, string) error { return nil },
			selfUpdate: func(_ context.Context, out io.Writer, wantOffline, _ bool) int {
				selfCalls, offline = selfCalls+1, wantOffline
				_, _ = fmt.Fprintln(out, "<upgrade block>")
				return 0
			},
		})
		return code, output.String(), selfCalls, offline
	}

	tests := []struct {
		argv     []string
		wantCode int
		wantOut  string // a substring for failures, the exact output otherwise
		wantSelf int
		offline  bool
	}{
		{argv: []string{"update"}, wantOut: "<upgrade block>\n", wantSelf: 1},
		{argv: []string{"update", "--self"}, wantOut: "<upgrade block>\n", wantSelf: 1},
		{argv: []string{"update", "orb"}, wantOut: "<upgrade block>\n", wantSelf: 1},
		{argv: []string{"update", "--offline"}, wantOut: "<upgrade block>\n", wantSelf: 1, offline: true},
		{argv: []string{"update", "--all"}, wantOut: "All packages up to date.\n<upgrade block>\n", wantSelf: 1},
		// The package and catalog routes never touch the binary.
		{argv: []string{"update", "--extensions"}, wantOut: "All packages up to date.\n"},
		{argv: []string{"update", "--models"}, wantOut: "Model catalogs refreshed\n"},
		{argv: []string{"update", "npm:pi-formatter"}, wantCode: 1, wantOut: "No matching package found"},
		{argv: []string{"update", "--extensions", "--offline"}, wantCode: 1, wantOut: `Unknown option --offline for "update".`},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.argv, " "), func(t *testing.T) {
			code, stdout, selfCalls, offline := run(test.argv...)
			if code != test.wantCode || selfCalls != test.wantSelf || offline != test.offline {
				t.Fatalf("code=%d self=%d offline=%v stdout=%q", code, selfCalls, offline, stdout)
			}
			if (test.wantCode == 0 && stdout != test.wantOut) || !strings.Contains(stdout, test.wantOut) {
				t.Fatalf("stdout = %q, want %q", stdout, test.wantOut)
			}
		})
	}

	// PI_OFFLINE reaches the updater without the flag.
	t.Setenv("PI_OFFLINE", "1")
	if _, _, _, offline := run("update"); !offline {
		t.Fatal("PI_OFFLINE did not reach the updater")
	}
}

func TestUpdateHelpDescribesTheDirectUpgrade(t *testing.T) {
	setupPackageCLI(t)
	_, stdout, _ := runPackageCLI(t, []string{"update", "--help"})
	if !strings.Contains(stdout, "Update orb itself") || !strings.Contains(stdout, "--offline") ||
		!strings.Contains(stdout, "orb update --all        Update packages, then the orb binary") {
		t.Fatalf("update help = %q", stdout)
	}
}
