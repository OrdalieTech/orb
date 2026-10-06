package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
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
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
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
	forged                                  bool   // sign checksums.txt with a key that is not Orb's
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
	public, private, _ := ed25519.GenerateKey(nil)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/download/" + state.tag + "/checksums.txt.sig":
			signer := private
			if state.forged {
				_, signer, _ = ed25519.GenerateKey(nil)
			}
			_, _ = writer.Write(ed25519.Sign(signer, []byte(state.checksums)))
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
	return selfUpdater{Updater: selfupdate.Updater{
		Key:            public,
		CurrentVersion: currentVersion,
		ReleaseURL:     server.URL + "/releases/latest",
		ReleaseBase:    server.URL + "/download",
		Client:         server.Client(),
		// Resolving the running binary is a filesystem effect, so only a test that installs one
		// overrides this; every other route proves it never got that far.
		Executable: func() (string, error) {
			t.Error("the updater resolved the running binary")
			return "", errors.New("must not be called")
		},
		ResolveLinks: filepath.EvalSymlinks,
	}}
}

func assertOnlyOrb(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "orb" {
		t.Fatalf("install directory %s holds %v (%v), want only orb", dir, entries, err)
	}
}

func wantUpdateOutput(cells ...string) string {
	var body strings.Builder
	for _, row := range lockupRows(orbalogo.FrameCount-1, 2, false) {
		body.WriteString(row)
		body.WriteByte('\n')
	}
	body.WriteString(joinBarStyled(updateBarCap, cells, false))
	body.WriteString("\n\n\n")
	return body.String()
}

// windowsRefusal is what orb update reports on Windows: releases publish no
// Windows archive yet (DECISIONS.md, Windows CI decision), so it fails before
// downloading anything and leaves the binary alone.
var windowsRefusal = "unsupported Orb platform: windows/" + runtime.GOARCH

// wantPerm is what os.Stat reports for a file created or chmodded with perm:
// Windows keeps only the read-only attribute, so Go reports 0444 or 0666.
func wantPerm(perm os.FileMode) os.FileMode {
	if runtime.GOOS != "windows" {
		return perm
	}
	if perm&0o200 == 0 {
		return 0o444
	}
	return 0o666
}

func TestSelfUpdateReplacesCanonicalBinary(t *testing.T) {
	dir, canonical, link := installedOrb(t, 0o700)
	state := &release{}
	updater := updaterFor(t, "0.4.15", state)
	updater.Executable = func() (string, error) { return link, nil }
	var output bytes.Buffer
	if runtime.GOOS == "windows" {
		if code := updater.run(context.Background(), &output); code != 1 || !strings.Contains(output.String(), windowsRefusal) {
			t.Fatalf("code = %d, output = %q, want %q", code, output.String(), windowsRefusal)
		}
		if contents, err := os.ReadFile(canonical); err != nil || string(contents) != "original" || state.archiveHits != 0 {
			t.Fatalf("binary = %q, %v; archive downloads = %d", contents, err, state.archiveHits)
		}
		return
	}
	if code := updater.run(context.Background(), &output); code != 0 {
		t.Fatalf("code = %d, output = %q", code, output.String())
	}
	want := wantUpdateOutput("0.4.15", "archive verified", "binary replaced", "0.5.0 ✓")
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
		{name: "signed by another key", state: release{forged: true}, wantErr: "not signed by Orb's release key"},
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
		if runtime.GOOS == "windows" && !test.managed {
			test.wantErr = windowsRefusal
		}
		t.Run(test.name, func(t *testing.T) {
			dir, canonical, link := installedOrb(t, 0o755)
			updater := updaterFor(t, "0.4.15", &test.state)
			updater.Executable = func() (string, error) { return link, nil }
			if test.managed {
				updater.Executable = func() (string, error) { return "/nix/store/abc-orb/bin/orb", nil }
				updater.ResolveLinks = func(value string) (string, error) { return value, nil }
			}
			var output bytes.Buffer
			if code := updater.run(context.Background(), &output); code != 1 {
				t.Fatalf("code = %d, output = %q", code, output.String())
			}
			if !strings.Contains(output.String(), test.wantErr) {
				t.Fatalf("output = %q, want a failure mentioning %q", output.String(), test.wantErr)
			}
			if !strings.HasSuffix(output.String(), "unchanged\n\n\n") {
				t.Fatalf("output = %q, want an unchanged terminal state", output.String())
			}
			contents, err := os.ReadFile(canonical)
			if err != nil || string(contents) != "original" {
				t.Fatalf("binary = %q, %v", contents, err)
			}
			if info, err := os.Stat(canonical); err != nil || info.Mode().Perm() != wantPerm(0o755) {
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
	updater.ReleaseURL = server.URL + "/releases/latest"
	var output bytes.Buffer
	if code := updater.run(context.Background(), &output); code != 1 {
		t.Fatalf("code = %d, output = %q", code, output.String())
	}
	if !strings.HasSuffix(output.String(), "unchanged\n\n\n") {
		t.Fatalf("output = %q, want a failure", output.String())
	}
}

// selfupdate.Swap owns the last-moment guard; drive it directly because it fires between staging and
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
	swapErr := selfupdate.Swap(canonical, newOrb, before)
	if swapErr == nil || !strings.Contains(swapErr.Error(), "changed while the update was staged") {
		t.Fatalf("err = %v", swapErr)
	}
	if contents, _ := os.ReadFile(canonical); string(contents) != "someone else got here first" {
		t.Fatalf("binary = %q", contents)
	}
	assertOnlyOrb(t, dir)
}
