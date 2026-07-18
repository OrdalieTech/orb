package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestToolAssetNames(t *testing.T) {
	tests := []struct {
		tool                        managedTool
		version, goos, goarch, want string
	}{
		{managedFD, "10.3.0", "darwin", "amd64", "fd-v10.3.0-x86_64-apple-darwin.tar.gz"},
		{managedFD, "10.3.0", "darwin", "arm64", "fd-v10.3.0-aarch64-apple-darwin.tar.gz"},
		{managedFD, "10.3.0", "linux", "amd64", "fd-v10.3.0-x86_64-unknown-linux-gnu.tar.gz"},
		{managedFD, "10.3.0", "linux", "arm64", "fd-v10.3.0-aarch64-unknown-linux-gnu.tar.gz"},
		{managedFD, "10.3.0", "windows", "amd64", "fd-v10.3.0-x86_64-pc-windows-msvc.zip"},
		{managedRG, "14.1.1", "darwin", "amd64", "ripgrep-14.1.1-x86_64-apple-darwin.tar.gz"},
		{managedRG, "14.1.1", "darwin", "arm64", "ripgrep-14.1.1-aarch64-apple-darwin.tar.gz"},
		{managedRG, "14.1.1", "linux", "amd64", "ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz"},
		{managedRG, "14.1.1", "linux", "arm64", "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz"},
		{managedRG, "14.1.1", "windows", "arm64", "ripgrep-14.1.1-aarch64-pc-windows-msvc.zip"},
	}
	for _, test := range tests {
		t.Run(string(test.tool)+"/"+test.goos+"/"+test.goarch, func(t *testing.T) {
			if got := toolAssetName(test.tool, test.version, test.goos, test.goarch); got != test.want {
				t.Fatalf("asset = %q, want %q", got, test.want)
			}
		})
	}
	if got := toolAssetName(managedRG, "14.1.1", "linux", "386"); got != "" {
		t.Fatalf("unsupported asset = %q", got)
	}
}

func TestToolManagerResolutionAndOfflineMode(t *testing.T) {
	managedDir := t.TempDir()
	managed := filepath.Join(managedDir, "fd")
	if err := os.WriteFile(managed, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	systemDir := t.TempDir()
	writeToolExecutable(t, filepath.Join(systemDir, "fdfind"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", systemDir)
	manager := &toolManager{binDir: managedDir, goos: "linux", goarch: "amd64"}
	if got := manager.getToolPath(context.Background(), managedFD); got != managed {
		t.Fatalf("managed path = %q, want %q", got, managed)
	}
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}
	if got := manager.getToolPath(context.Background(), managedFD); got != "fdfind" {
		t.Fatalf("system path = %q, want fdfind", got)
	}

	var requests atomic.Int32
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PI_OFFLINE", "YeS")
	manager = testManagedToolManager(t, "linux", func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return testHTTPResponse(http.StatusInternalServerError, nil), nil
	})
	if got := manager.ensureTool(context.Background(), managedFD); got != "" {
		t.Fatalf("offline path = %q", got)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("offline network requests = %d", got)
	}
}

func TestToolManagerDownloadsReleaseAssetAndExtractsTarGz(t *testing.T) {
	archive := toolTarGz(t, "ripgrep-14.1.1-x86_64-unknown-linux-musl/rg", "rg-binary")
	assetName := toolAssetName(managedRG, "14.1.1", "linux", "amd64")
	asset := toolReleaseAsset(assetName, "https://example.test/archive", archive)
	var requested []string
	manager := testManagedToolManager(t, "linux", func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.URL.Path)
		switch request.URL.Path {
		case "/repos/BurntSushi/ripgrep/releases/latest":
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "14.1.1", asset)), nil
		case "/archive":
			return testHTTPResponse(http.StatusOK, archive), nil
		default:
			return testHTTPResponse(http.StatusNotFound, nil), nil
		}
	})
	path, err := manager.downloadTool(context.Background(), managedRG)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "rg-binary" {
		t.Fatalf("binary = %q, err = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, err = %v", info.Mode(), err)
	}
	if len(requested) != 2 {
		t.Fatalf("requests = %v, want latest plus archive", requested)
	}
}

func TestToolManagerExtractsZipAndPinsIntelMacVersion(t *testing.T) {
	zipData := toolZip(t, "package/rg.exe", "windows-rg")
	windowsAsset := toolAssetName(managedRG, "14.1.1", "windows", "amd64")
	windows := testManagedToolManager(t, "windows", archiveTransport(t, "14.1.1", windowsAsset, zipData))
	path, err := windows.downloadTool(context.Background(), managedRG)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "windows-rg" || filepath.Base(path) != "rg.exe" {
		t.Fatalf("zip binary path=%q data=%q err=%v", path, data, err)
	}

	tarData := toolTarGz(t, "package/fd", "fd-binary")
	var requests []string
	mac := testManagedToolManager(t, "darwin", func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/repos/sharkdp/fd/releases/latest":
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "v11.0.0")), nil
		case "/repos/sharkdp/fd/releases/tags/v10.3.0":
			assetName := toolAssetName(managedFD, "10.3.0", "darwin", "amd64")
			asset := toolReleaseAsset(assetName, "https://example.test/archive", tarData)
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "v10.3.0", asset)), nil
		case "/archive":
			return testHTTPResponse(http.StatusOK, tarData), nil
		default:
			return testHTTPResponse(http.StatusNotFound, nil), nil
		}
	})
	if _, err := mac.downloadTool(context.Background(), managedFD); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[1] != "/repos/sharkdp/fd/releases/tags/v10.3.0" || requests[2] != "/archive" {
		t.Fatalf("requests = %v, want latest lookup, pinned release lookup, then archive", requests)
	}
}

func TestToolManagerArchiveWithoutBinaryFailsBeforeInstall(t *testing.T) {
	archive := toolTarGz(t, "package/README.md", "none")
	assetName := toolAssetName(managedRG, "14.1.1", "linux", "amd64")
	manager := testManagedToolManager(t, "linux", archiveTransport(t, "14.1.1", assetName, archive))
	_, err := manager.downloadTool(context.Background(), managedRG)
	if err == nil || !strings.Contains(err.Error(), "binary not found in archive") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(manager.binDir, "rg")); !os.IsNotExist(statErr) {
		t.Fatalf("partial binary stat = %v", statErr)
	}
}

func TestToolManagerRejectsChecksumMismatchBeforeExtract(t *testing.T) {
	archive := toolTarGz(t, "package/rg", "rg-binary")
	assetName := toolAssetName(managedRG, "14.1.1", "linux", "amd64")
	asset := releaseAsset{
		Name: assetName, BrowserDownloadURL: "https://example.test/archive",
		Digest: "sha256:" + strings.Repeat("0", sha256.Size*2),
	}
	manager := testManagedToolManager(t, "linux", func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repos/BurntSushi/ripgrep/releases/latest":
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "14.1.1", asset)), nil
		case "/archive":
			return testHTTPResponse(http.StatusOK, archive), nil
		default:
			return testHTTPResponse(http.StatusNotFound, nil), nil
		}
	})
	_, err := manager.downloadTool(context.Background(), managedRG)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(manager.binDir, "rg")); !os.IsNotExist(statErr) {
		t.Fatalf("partial binary stat = %v", statErr)
	}
}

func TestToolManagerUsesPublishedChecksumAssetWhenDigestMissing(t *testing.T) {
	archive := toolTarGz(t, "package/rg", "rg-binary")
	assetName := toolAssetName(managedRG, "14.1.1", "linux", "amd64")
	asset := releaseAsset{Name: assetName, BrowserDownloadURL: "https://example.test/archive"}
	checksumAsset := releaseAsset{Name: assetName + ".sha256", BrowserDownloadURL: "https://example.test/checksum"}
	checksum := sha256.Sum256(archive)
	checksumFile := []byte(hex.EncodeToString(checksum[:]) + "  " + assetName + "\n")
	manager := testManagedToolManager(t, "linux", func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repos/BurntSushi/ripgrep/releases/latest":
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "14.1.1", asset, checksumAsset)), nil
		case "/checksum":
			return testHTTPResponse(http.StatusOK, checksumFile), nil
		case "/archive":
			return testHTTPResponse(http.StatusOK, archive), nil
		default:
			return testHTTPResponse(http.StatusNotFound, nil), nil
		}
	})
	path, err := manager.downloadTool(context.Background(), managedRG)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "rg-binary" {
		t.Fatalf("binary = %q, err = %v", data, err)
	}
}

func TestToolManagerFailsClosedWithoutChecksum(t *testing.T) {
	assetName := toolAssetName(managedRG, "14.1.1", "linux", "amd64")
	asset := releaseAsset{Name: assetName, BrowserDownloadURL: "https://example.test/archive"}
	var requests atomic.Int32
	manager := testManagedToolManager(t, "linux", func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, "14.1.1", asset)), nil
	})
	_, err := manager.downloadTool(context.Background(), managedRG)
	if err == nil || err.Error() != "checksum unavailable for release asset: "+assetName {
		t.Fatalf("error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("network requests = %d, want release lookup only", got)
	}
}

func TestToolManagerLiveDownload(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to download a real managed tool")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("managed downloads target linux and darwin")
	}
	for _, tool := range []managedTool{managedRG, managedFD} {
		t.Run(string(tool), func(t *testing.T) {
			manager := &toolManager{
				binDir: t.TempDir(), goos: runtime.GOOS, goarch: runtime.GOARCH,
				apiBaseURL: "https://api.github.com", client: http.DefaultClient,
			}
			path, err := manager.downloadTool(context.Background(), tool)
			if err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(path, "--version").CombinedOutput(); err != nil {
				t.Fatalf("%s --version: %v: %s", path, err, output)
			}
		})
	}
}

func testManagedToolManager(t *testing.T, goos string, transport roundTripFunc) *toolManager {
	t.Helper()
	return &toolManager{
		binDir: t.TempDir(), goos: goos, goarch: "amd64",
		apiBaseURL: "https://example.test",
		client:     &http.Client{Transport: transport},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func toolReleaseJSON(t *testing.T, tag string, assets ...releaseAsset) []byte {
	t.Helper()
	data, err := json.Marshal(githubRelease{TagName: tag, Assets: assets})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func archiveTransport(t *testing.T, tag, assetName string, archive []byte) roundTripFunc {
	t.Helper()
	asset := toolReleaseAsset(assetName, "https://example.test/archive", archive)
	return func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/releases/latest") {
			return testHTTPResponse(http.StatusOK, toolReleaseJSON(t, tag, asset)), nil
		}
		if request.URL.Path == "/archive" {
			return testHTTPResponse(http.StatusOK, archive), nil
		}
		return testHTTPResponse(http.StatusNotFound, nil), nil
	}
}

func toolReleaseAsset(name, url string, contents []byte) releaseAsset {
	checksum := sha256.Sum256(contents)
	return releaseAsset{Name: name, BrowserDownloadURL: url, Digest: "sha256:" + hex.EncodeToString(checksum[:])}
}

func toolTarGz(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tarWriter, contents); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func toolZip(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeToolExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
