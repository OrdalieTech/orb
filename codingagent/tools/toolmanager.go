package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	toolNetworkTimeout  = 10 * time.Second
	toolDownloadTimeout = 120 * time.Second
	maxChecksumBytes    = 1 << 20
)

type managedTool string

const (
	managedFD managedTool = "fd"
	managedRG managedTool = "rg"
)

type toolConfig struct {
	name              string
	repository        string
	binaryName        string
	systemBinaryNames []string
}

var managedToolConfigs = map[managedTool]toolConfig{
	managedFD: {
		name:              "fd",
		repository:        "sharkdp/fd",
		binaryName:        "fd",
		systemBinaryNames: []string{"fd", "fdfind"},
	},
	managedRG: {
		name:       "ripgrep",
		repository: "BurntSushi/ripgrep",
		binaryName: "rg",
	},
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type githubRelease struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type toolManager struct {
	binDir     string
	goos       string
	goarch     string
	apiBaseURL string
	client     *http.Client
}

func defaultToolManager() (*toolManager, error) {
	binDir, err := managedBinDir()
	if err != nil {
		return nil, err
	}
	return &toolManager{
		binDir:     binDir,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		apiBaseURL: "https://api.github.com",
		client:     http.DefaultClient,
	}, nil
}

func managedBinDir() (string, error) {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		agentDir = filepath.Join(home, ".pi", "agent")
	} else {
		normalized, err := expandPath(agentDir, false, false)
		if err != nil {
			return "", err
		}
		agentDir = normalized
	}
	return filepath.Join(agentDir, "bin"), nil
}

func (manager *toolManager) getToolPath(ctx context.Context, tool managedTool) string {
	config, ok := managedToolConfigs[tool]
	if !ok {
		return ""
	}
	binaryName := config.binaryName
	if manager.goos == "windows" {
		binaryName += ".exe"
	}
	localPath := filepath.Join(manager.binDir, binaryName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath
	}
	for _, name := range config.systemBinaryNames {
		if commandExists(ctx, name) {
			return name
		}
	}
	if len(config.systemBinaryNames) == 0 && commandExists(ctx, config.binaryName) {
		return config.binaryName
	}
	return ""
}

func commandExists(ctx context.Context, name string) bool {
	command := exec.CommandContext(ctx, name, "--version")
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	err := command.Run()
	var exitError *exec.ExitError
	return err == nil || errors.As(err, &exitError)
}

func ensureManagedTool(ctx context.Context, tool managedTool) string {
	manager, err := defaultToolManager()
	if err != nil {
		return ""
	}
	return manager.ensureTool(ctx, tool)
}

func (manager *toolManager) ensureTool(ctx context.Context, tool managedTool) string {
	if path := manager.getToolPath(ctx, tool); path != "" {
		return path
	}
	if offlineModeEnabled() || manager.goos == "android" {
		return ""
	}
	path, err := manager.downloadTool(ctx, tool)
	if err != nil {
		return ""
	}
	return path
}

func offlineModeEnabled() bool {
	switch strings.ToLower(os.Getenv("PI_OFFLINE")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (manager *toolManager) downloadTool(ctx context.Context, tool managedTool) (string, error) {
	config, ok := managedToolConfigs[tool]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", tool)
	}
	release, err := manager.fetchRelease(ctx, config)
	if err != nil {
		return "", err
	}
	version := strings.TrimPrefix(release.TagName, "v")
	if tool == managedFD && manager.goos == "darwin" && manager.goarch == "amd64" {
		const pinnedVersion = "10.3.0"
		if version != pinnedVersion {
			release, err = manager.fetchTaggedRelease(ctx, config, "v"+pinnedVersion)
			if err != nil {
				return "", err
			}
		}
		version = pinnedVersion
	}
	assetName := toolAssetName(tool, version, manager.goos, manager.goarch)
	if assetName == "" {
		return "", fmt.Errorf("unsupported platform: %s/%s", manager.goos, manager.goarch)
	}
	if err := os.MkdirAll(manager.binDir, 0o755); err != nil {
		return "", err
	}
	tempDir, err := os.MkdirTemp(manager.binDir, "extract_tmp_"+config.binaryName+"_")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	asset, ok := assetByName(release.Assets, assetName)
	if !ok {
		return "", fmt.Errorf("release asset not found: %s", assetName)
	}
	expectedChecksum, err := manager.releaseAssetChecksum(ctx, release, asset)
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(tempDir, assetName)
	if err := manager.downloadFile(ctx, asset.BrowserDownloadURL, archivePath); err != nil {
		return "", err
	}
	if err := verifyFileChecksum(archivePath, expectedChecksum); err != nil {
		return "", err
	}

	binaryFileName := config.binaryName
	if manager.goos == "windows" {
		binaryFileName += ".exe"
	}
	extractedPath := filepath.Join(tempDir, "extracted-"+binaryFileName)
	switch {
	case strings.HasSuffix(assetName, ".tar.gz"):
		err = extractBinaryFromTarGz(archivePath, extractedPath, binaryFileName)
	case strings.HasSuffix(assetName, ".zip"):
		err = extractBinaryFromZip(archivePath, extractedPath, binaryFileName)
	default:
		err = fmt.Errorf("unsupported archive format: %s", assetName)
	}
	if err != nil {
		return "", err
	}
	if manager.goos != "windows" {
		if err := os.Chmod(extractedPath, 0o755); err != nil {
			return "", err
		}
	}
	binaryPath := filepath.Join(manager.binDir, binaryFileName)
	if err := os.Rename(extractedPath, binaryPath); err != nil {
		return "", err
	}
	return binaryPath, nil
}

func (manager *toolManager) fetchRelease(ctx context.Context, config toolConfig) (githubRelease, error) {
	endpoint := strings.TrimRight(manager.apiBaseURL, "/") + "/repos/" + config.repository + "/releases/latest"
	return manager.fetchReleaseEndpoint(ctx, endpoint)
}

func (manager *toolManager) fetchTaggedRelease(ctx context.Context, config toolConfig, tag string) (githubRelease, error) {
	endpoint := strings.TrimRight(manager.apiBaseURL, "/") + "/repos/" + config.repository + "/releases/tags/" + tag
	return manager.fetchReleaseEndpoint(ctx, endpoint)
}

func (manager *toolManager) fetchReleaseEndpoint(ctx context.Context, endpoint string) (githubRelease, error) {
	requestContext, cancel := context.WithTimeout(ctx, toolNetworkTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubRelease{}, err
	}
	request.Header.Set("User-Agent", "pi-coding-agent")
	response, err := manager.client.Do(request)
	if err != nil {
		return githubRelease{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return githubRelease{}, fmt.Errorf("GitHub API error: %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	return release, nil
}

func assetByName(assets []releaseAsset, name string) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func (manager *toolManager) releaseAssetChecksum(ctx context.Context, release githubRelease, asset releaseAsset) (string, error) {
	if checksum, ok := checksumFromDigest(asset.Digest); ok {
		return checksum, nil
	}
	checksumAsset, ok := checksumAssetFor(release.Assets, asset.Name)
	if !ok {
		return "", fmt.Errorf("checksum unavailable for release asset: %s", asset.Name)
	}
	contents, err := manager.downloadChecksum(ctx, checksumAsset.BrowserDownloadURL)
	if err != nil {
		return "", err
	}
	checksum, ok := parseChecksum(contents, asset.Name)
	if !ok {
		return "", fmt.Errorf("invalid checksum for release asset: %s", asset.Name)
	}
	return checksum, nil
}

func checksumFromDigest(digest string) (string, bool) {
	algorithm, checksum, ok := strings.Cut(strings.TrimSpace(digest), ":")
	if !ok || !strings.EqualFold(algorithm, "sha256") || !validSHA256(checksum) {
		return "", false
	}
	return strings.ToLower(checksum), true
}

func checksumAssetFor(assets []releaseAsset, assetName string) (releaseAsset, bool) {
	if asset, ok := assetByName(assets, assetName+".sha256"); ok {
		return asset, true
	}
	for _, asset := range assets {
		switch strings.ToLower(asset.Name) {
		case "sha256sums", "sha256sums.txt", "checksums", "checksums.txt":
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func (manager *toolManager) downloadChecksum(ctx context.Context, url string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, toolNetworkTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := manager.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("failed to download checksum: %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxChecksumBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxChecksumBytes {
		return nil, errors.New("checksum file too large")
	}
	return contents, nil
}

func parseChecksum(contents []byte, assetName string) (string, bool) {
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || !validSHA256(fields[0]) {
			continue
		}
		if len(fields) == 1 || strings.TrimPrefix(fields[1], "*") == assetName {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func verifyFileChecksum(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func toolAssetName(tool managedTool, version, goos, goarch string) string {
	architecture := ""
	switch goarch {
	case "arm64":
		architecture = "aarch64"
	case "amd64":
		architecture = "x86_64"
	default:
		return ""
	}
	switch tool {
	case managedFD:
		switch goos {
		case "darwin":
			return fmt.Sprintf("fd-v%s-%s-apple-darwin.tar.gz", version, architecture)
		case "linux":
			return fmt.Sprintf("fd-v%s-%s-unknown-linux-gnu.tar.gz", version, architecture)
		case "windows":
			return fmt.Sprintf("fd-v%s-%s-pc-windows-msvc.zip", version, architecture)
		}
	case managedRG:
		switch goos {
		case "darwin":
			return fmt.Sprintf("ripgrep-%s-%s-apple-darwin.tar.gz", version, architecture)
		case "linux":
			if goarch == "arm64" {
				return fmt.Sprintf("ripgrep-%s-aarch64-unknown-linux-gnu.tar.gz", version)
			}
			return fmt.Sprintf("ripgrep-%s-x86_64-unknown-linux-musl.tar.gz", version)
		case "windows":
			return fmt.Sprintf("ripgrep-%s-%s-pc-windows-msvc.zip", version, architecture)
		}
	}
	return ""
}

func (manager *toolManager) downloadFile(ctx context.Context, url, destination string) error {
	requestContext, cancel := context.WithTimeout(ctx, toolDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := manager.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("failed to download: %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func extractBinaryFromTarGz(archivePath, destination, binaryName string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return err
	}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = gzipReader.Close()
			_ = file.Close()
			return nextErr
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(filepath.Clean(header.Name)) != binaryName {
			continue
		}
		err = writeExtractedBinary(destination, tarReader)
		break
	}
	gzipCloseErr := gzipReader.Close()
	fileCloseErr := file.Close()
	if err != nil {
		return err
	}
	if gzipCloseErr != nil {
		return gzipCloseErr
	}
	if fileCloseErr != nil {
		return fileCloseErr
	}
	if _, statErr := os.Stat(destination); statErr != nil {
		return fmt.Errorf("binary not found in archive: expected %s", binaryName)
	}
	return nil
}

func extractBinaryFromZip(archivePath, destination, binaryName string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || filepath.Base(filepath.Clean(file.Name)) != binaryName {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			return err
		}
		writeErr := writeExtractedBinary(destination, entry)
		closeErr := entry.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
	return fmt.Errorf("binary not found in archive: expected %s", binaryName)
}

func writeExtractedBinary(destination string, source io.Reader) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
