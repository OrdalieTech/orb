package codingagent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm legacy shasum verification
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OrdalieTech/pigo/internal/semver"
)

// Native npm registry client: metadata fetch, version selection, tarball
// download with integrity verification, and extraction — replacing upstream's
// npm/bun/pnpm subprocess calls (pigo ships without a Node toolchain).

const defaultNpmRegistry = "https://registry.npmjs.org"

type npmRegistryConfig struct {
	baseURL   string
	authToken string
}

// npmRegistry resolves the registry once per manager: the registryBaseURL
// test seam wins, then npm_config_registry, the project .npmrc, and ~/.npmrc
// (registry= lines), defaulting to registry.npmjs.org. A //host/:_authToken=
// line matching the registry is passed through as a bearer token.
func (manager *PackageManager) npmRegistry() npmRegistryConfig {
	manager.registryOnce.Do(func() {
		if manager.registryBaseURL != "" {
			manager.registryConfig = npmRegistryConfig{baseURL: strings.TrimSuffix(manager.registryBaseURL, "/")}
			return
		}
		manager.registryConfig = resolveNpmRegistry(manager.cwd)
	})
	return manager.registryConfig
}

func resolveNpmRegistry(cwd string) npmRegistryConfig {
	registry := ""
	for _, key := range [...]string{"npm_config_registry", "NPM_CONFIG_REGISTRY"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			registry = value
			break
		}
	}
	projectRC := parseNpmrc(filepath.Join(cwd, ".npmrc"))
	userRC := parseNpmrc(filepath.Join(pmHomeDir(), ".npmrc"))
	if registry == "" {
		registry = projectRC["registry"]
	}
	if registry == "" {
		registry = userRC["registry"]
	}
	if registry == "" {
		registry = defaultNpmRegistry
	}
	registry = strings.TrimSuffix(registry, "/")
	tokenKey := npmNerfDart(registry) + ":_authToken"
	token := projectRC[tokenKey]
	if token == "" {
		token = userRC[tokenKey]
	}
	return npmRegistryConfig{baseURL: registry, authToken: token}
}

// parseNpmrc is a minimal key=value parse: comments (#, ;) and blank lines
// skipped, values unquoted; no ini sections, no ${VAR} expansion.
func parseNpmrc(path string) map[string]string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	values := map[string]string{}
	for line := range strings.SplitSeq(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return values
}

// npmNerfDart approximates npm's nerf-dart credential key for a registry URL:
// protocol stripped, trailing slash ensured (e.g. //registry.npmjs.org/).
func npmNerfDart(registry string) string {
	parsed, err := url.Parse(registry)
	if err != nil || parsed.Host == "" {
		return ""
	}
	path := parsed.Path
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return "//" + parsed.Host + path
}

func isDarwin() bool { return runtime.GOOS == "darwin" }
func isLinux() bool  { return runtime.GOOS == "linux" }

func readPackageJSON(path string) (map[string]any, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(contents, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

type npmPackument struct {
	DistTags map[string]string         `json:"dist-tags"`
	Versions map[string]npmVersionInfo `json:"versions"`
}

type npmVersionInfo struct {
	Version string  `json:"version"`
	Dist    npmDist `json:"dist"`
}

type npmDist struct {
	Tarball   string `json:"tarball"`
	Shasum    string `json:"shasum"`
	Integrity string `json:"integrity"`
}

func (manager *PackageManager) httpClient() *http.Client {
	return &http.Client{Timeout: packageNetworkTimeout}
}

func (manager *PackageManager) fetchPackument(name string) (*npmPackument, error) {
	return manager.fetchPackumentContext(context.Background(), name)
}

func (manager *PackageManager) fetchPackumentContext(ctx context.Context, name string) (*npmPackument, error) {
	registry := manager.npmRegistry()
	endpoint := registry.baseURL + "/" + url.PathEscape(name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	if registry.authToken != "" {
		request.Header.Set("Authorization", "Bearer "+registry.authToken)
	}
	response, err := manager.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("npm package not found: %s", name)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm registry request for %s failed with status %d", name, response.StatusCode)
	}
	var packument npmPackument
	if err := json.NewDecoder(response.Body).Decode(&packument); err != nil {
		return nil, fmt.Errorf("npm registry response for %s is invalid: %s", name, err)
	}
	return &packument, nil
}

// selectNpmVersion mirrors upstream `npm view` semantics: exact version,
// highest matching a range, else the latest dist-tag.
func selectNpmVersion(packument *npmPackument, source *npmSource) (npmVersionInfo, error) {
	if source.pinned {
		if info, exists := packument.Versions[source.version]; exists {
			return info, nil
		}
		return npmVersionInfo{}, fmt.Errorf("npm version %s@%s not found in registry", source.name, source.version)
	}
	if source.rng != "" {
		versions := make([]string, 0, len(packument.Versions))
		for version := range packument.Versions {
			versions = append(versions, version)
		}
		best := semver.MaxSatisfying(versions, source.rng)
		if best == "" {
			return npmVersionInfo{}, fmt.Errorf("no npm version of %s satisfies %s", source.name, source.rng)
		}
		return packument.Versions[best], nil
	}
	latest := packument.DistTags["latest"]
	if latest == "" {
		return npmVersionInfo{}, fmt.Errorf("npm package %s has no latest dist-tag", source.name)
	}
	if info, exists := packument.Versions[latest]; exists {
		return info, nil
	}
	return npmVersionInfo{}, fmt.Errorf("npm version %s@%s not found in registry", source.name, latest)
}

func (manager *PackageManager) getLatestNpmVersion(source *npmSource) (string, error) {
	return manager.getLatestNpmVersionContext(context.Background(), source)
}

func (manager *PackageManager) getLatestNpmVersionContext(ctx context.Context, source *npmSource) (string, error) {
	packument, err := manager.fetchPackumentContext(ctx, source.name)
	if err != nil {
		return "", err
	}
	info, err := selectNpmVersion(packument, source)
	if err != nil {
		return "", err
	}
	return info.Version, nil
}

func (manager *PackageManager) installNpm(source *npmSource, scope string, temporary bool) error {
	installRoot, err := manager.getNpmInstallRoot(scope, temporary)
	if err != nil {
		return err
	}
	if err := manager.ensureNpmProject(installRoot); err != nil {
		return err
	}
	packument, err := manager.fetchPackument(source.name)
	if err != nil {
		return err
	}
	info, err := selectNpmVersion(packument, source)
	if err != nil {
		return err
	}
	if info.Dist.Tarball == "" {
		return fmt.Errorf("npm version %s@%s has no tarball", source.name, info.Version)
	}
	tarball, err := manager.downloadNpmTarball(info)
	if err != nil {
		return fmt.Errorf("failed to download %s@%s: %s", source.name, info.Version, err)
	}
	destination := filepath.Join(installRoot, "node_modules", source.name)
	if err := extractNpmTarball(tarball, destination); err != nil {
		return err
	}
	return manager.installPackageDependencies(destination)
}

func (manager *PackageManager) uninstallNpm(source *npmSource, scope string) error {
	installRoot, err := manager.getNpmInstallRoot(scope, false)
	if err != nil {
		return err
	}
	if !pathExists(installRoot) {
		return nil
	}
	installed, err := resolveManagedPath(installRoot, "node_modules", source.name)
	if err != nil {
		return err
	}
	return os.RemoveAll(installed)
}

func (manager *PackageManager) downloadNpmTarball(info npmVersionInfo) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, info.Dist.Tarball, nil)
	if err != nil {
		return nil, err
	}
	registry := manager.npmRegistry()
	if registry.authToken != "" {
		// Send the token only to the registry's own host.
		if base, baseErr := url.Parse(registry.baseURL); baseErr == nil {
			if target, targetErr := url.Parse(info.Dist.Tarball); targetErr == nil && target.Host == base.Host {
				request.Header.Set("Authorization", "Bearer "+registry.authToken)
			}
		}
	}
	response, err := manager.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tarball request failed with status %d", response.StatusCode)
	}
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if err := verifyNpmIntegrity(contents, info.Dist.Integrity, info.Dist.Shasum); err != nil {
		return nil, err
	}
	return contents, nil
}

// verifyNpmIntegrity checks the SRI integrity string when present (sha512 or
// sha1), falling back to the legacy hex shasum. Unverifiable data is rejected.
func verifyNpmIntegrity(contents []byte, integrity, shasum string) error {
	if integrity != "" {
		for entry := range strings.SplitSeq(integrity, " ") {
			algorithm, expected, found := strings.Cut(entry, "-")
			if !found {
				continue
			}
			var digest []byte
			switch algorithm {
			case "sha512":
				sum := sha512.Sum512(contents)
				digest = sum[:]
			case "sha1":
				sum := sha1.Sum(contents) //nolint:gosec // registry-provided legacy digest
				digest = sum[:]
			default:
				continue
			}
			if base64.StdEncoding.EncodeToString(digest) == expected {
				return nil
			}
			return errors.New("tarball integrity check failed")
		}
	}
	if shasum != "" {
		sum := sha1.Sum(contents) //nolint:gosec // registry-provided legacy digest
		if hex.EncodeToString(sum[:]) == strings.ToLower(shasum) {
			return nil
		}
		return errors.New("tarball integrity check failed")
	}
	return errors.New("tarball has no integrity metadata")
}

// extractNpmTarball unpacks an npm .tgz into destination, stripping the
// leading path component ("package/") as npm does. Only regular files and
// directories are materialized; entries escaping the destination are rejected.
func extractNpmTarball(tarball []byte, destination string) error {
	staging := destination + ".tmp-install"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()

	gzipReader, err := gzip.NewReader(strings.NewReader(string(tarball)))
	if err != nil {
		return err
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		_, stripped, found := strings.Cut(name, "/")
		if !found || stripped == "" {
			continue
		}
		target, err := resolveManagedPath(staging, filepath.FromSlash(stripped))
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tarReader) //nolint:gosec // size bounded by verified tarball
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
		default:
			// Symlinks and special files are not part of npm publish output.
			continue
		}
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	cleanup = false
	return nil
}
