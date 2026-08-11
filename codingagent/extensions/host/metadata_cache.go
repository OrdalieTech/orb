package host

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
)

// The metadata snapshot cache lets metadata-only commands (--help,
// --list-models) reuse the registration-static metadata of the last full
// extension host load — CLI flags, provider registrations, load errors —
// instead of spawning a JS runtime. It is write-through only: every successful
// full load rewrites it, real sessions always spawn, and any read problem
// (missing, corrupt, fingerprint mismatch, partial data) means the caller
// spawns exactly as before, so staleness self-heals on the next real run.

const (
	metadataCacheName    = "metadata-cache.json"
	metadataCacheVersion = 1
	// metadataFingerprintMaxFiles bounds the package-tree listings: a package
	// dir bigger than this (a repo root, a home dir with a stray package.json)
	// disables the cache for that extension set rather than pay an unbounded
	// walk on every metadata command.
	metadataFingerprintMaxFiles = 4096
)

type MetadataCache struct {
	Version        int                      `json:"version"`
	Fingerprint    string                   `json:"fingerprint"`
	RuntimeName    string                   `json:"runtimeName"`
	RuntimeVersion string                   `json:"runtimeVersion"`
	RuntimePath    string                   `json:"runtimePath"`
	Extensions     []MetadataCacheExtension `json:"extensions"`
	Errors         []LoadError              `json:"errors"`
	Diagnostics    []extensions.Diagnostic  `json:"diagnostics"`
}

type MetadataCacheExtension struct {
	Path      string                     `json:"path"`
	Flags     []wireFlag                 `json:"flags"`
	Providers []wireProviderRegistration `json:"providers"`
}

// MetadataCacheParams identifies a discovered extension set: the same inputs
// fingerprint the cache at write (from Manager options) and at read (from the
// metadata command's own discovery), so equality proves the snapshot still
// describes what a full load would register.
type MetadataCacheParams struct {
	AgentDir       string
	CWD            string
	ProjectTrusted bool
	Paths          []string
}

// LoadMetadataCache returns the cached snapshot when it provably matches the
// current extension set, and nil whenever spawning is required. The cached
// runtime identity feeds the fingerprint as recorded: re-probing a runtime
// would itself spawn a process, and a runtime change goes stale only until the
// next real session rewrites the cache.
func LoadMetadataCache(params MetadataCacheParams) *MetadataCache {
	encoded, err := os.ReadFile(filepath.Join(params.AgentDir, "host", metadataCacheName))
	if err != nil {
		return nil
	}
	var cache MetadataCache
	if json.Unmarshal(encoded, &cache) != nil || cache.Version != metadataCacheVersion || cache.Fingerprint == "" {
		return nil
	}
	for _, extension := range cache.Extensions {
		for _, provider := range extension.Providers {
			// Native provider auth and model callbacks run inside the JS
			// process; a snapshot cannot answer their availability, so their
			// presence makes the cache partial data.
			if provider.Kind != wireProviderConfig {
				return nil
			}
		}
	}
	fingerprint, err := metadataFingerprint(params, cache.RuntimeName, cache.RuntimeVersion, cache.RuntimePath)
	if err != nil || fingerprint != cache.Fingerprint {
		return nil
	}
	return &cache
}

// Register replays the cached registrations into the registry the way a live
// load would: one factory per successfully loaded entry, flags in the same
// per-extension sorted order the host binds them, config-kind providers with
// their registration data (no Stream — metadata commands never stream).
func (cache *MetadataCache) Register(registry *extensions.Registry) []LoadError {
	var result []LoadError
	for _, entry := range cache.Extensions {
		err := registry.Register(entry.Path, func(api extensions.API) error {
			for _, flag := range entry.Flags {
				var defaultValue any
				if len(flag.Default) != 0 {
					if err := json.Unmarshal(flag.Default, &defaultValue); err != nil {
						return fmt.Errorf("decode flag %s default: %w", flag.Name, err)
					}
				}
				api.RegisterFlag(flag.Name, extensions.Flag{Description: flag.Description, Type: flag.Type, Default: defaultValue})
			}
			for _, provider := range entry.Providers {
				api.RegisterProviderConfig(provider.ID, providerConfigFromWire(provider.Config))
			}
			return nil
		})
		if err != nil {
			result = append(result, LoadError{Path: entry.Path, Error: stripRegistryPrefix(entry.Path, err)})
		}
	}
	return result
}

// writeMetadataCache persists the registration-static metadata of a successful
// full load (temp file + rename, 0600). Failures are silent: the cache is an
// optimization for metadata commands, never a load-bearing store.
func (manager *Manager) writeMetadataCache(success map[string]bool, result LoadResult) {
	runtime := manager.Runtime()
	if runtime == nil {
		return
	}
	fingerprint, err := metadataFingerprint(MetadataCacheParams{
		AgentDir:       manager.options.AgentDir,
		CWD:            manager.options.CWD,
		ProjectTrusted: manager.options.ProjectTrusted,
		Paths:          result.Paths,
	}, runtime.Name, runtime.Version, runtime.Path)
	if err != nil {
		return
	}
	cache := MetadataCache{
		Version:        metadataCacheVersion,
		Fingerprint:    fingerprint,
		RuntimeName:    runtime.Name,
		RuntimeVersion: runtime.Version,
		RuntimePath:    runtime.Path,
		Extensions:     make([]MetadataCacheExtension, 0, len(manager.entries)),
		Errors:         append([]LoadError(nil), result.Errors...),
		Diagnostics:    append([]extensions.Diagnostic(nil), result.Diagnostics...),
	}
	for _, entry := range manager.entries {
		if !success[entry.ID] {
			continue
		}
		snapshot := MetadataCacheExtension{Path: entry.Path, Flags: manager.stateHost.flagsSnapshot(entry.ID)}
		if state := manager.state(entry.ID); state != nil {
			snapshot.Providers = state.Providers
		}
		cache.Extensions = append(cache.Extensions, snapshot)
	}
	encoded, err := json.Marshal(&cache)
	if err != nil {
		return
	}
	directory := filepath.Join(manager.options.AgentDir, "host")
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	temporary, err := os.CreateTemp(directory, "."+metadataCacheName+"-*")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		return
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return
	}
	if temporary.Close() != nil {
		return
	}
	_ = os.Rename(temporaryPath, filepath.Join(directory, metadataCacheName))
}

func (host *stateHost) flagsSnapshot(extensionID string) []wireFlag {
	host.mu.RLock()
	registration := cloneStateRegistrations(host.registrations[extensionID])
	host.mu.RUnlock()
	if registration == nil {
		return nil
	}
	return sortedFlags(registration.flags)
}

var embeddedSourceHashes = sync.OnceValue(func() string {
	_, sdkHash, err := collectSDKEntries()
	if err != nil {
		sdkHash = "error:" + err.Error()
	}
	return fmt.Sprintf("host:%x\nloader:%x\nsdk:%s\n", sha256.Sum256(hostSource), sha256.Sum256(loaderSource), sdkHash)
})

// metadataFingerprint hashes everything that can change what a full load would
// register: the embedded host/loader/SDK sources, the runtime identity, the
// sorted entry set with per-entry content hash and containing-package listing,
// the npm install root lockfile, the trust decision, and the cwd whenever a
// project-scoped entry participates. Any I/O error disables the cache rather
// than fingerprinting an unknown state.
func metadataFingerprint(params MetadataCacheParams, runtimeName, runtimeVersion, runtimePath string) (string, error) {
	digest := sha256.New()
	_, _ = io.WriteString(digest, embeddedSourceHashes())
	_, _ = fmt.Fprintf(digest, "runtime:%s|%s|%s\n", runtimeName, runtimeVersion, runtimePath)
	_, _ = fmt.Fprintf(digest, "trusted:%t\n", params.ProjectTrusted)
	paths := append([]string(nil), params.Paths...)
	sort.Strings(paths)
	cwd := filepath.Clean(params.CWD)
	projectScoped := false
	remaining := metadataFingerprintMaxFiles
	for _, path := range paths {
		if strings.HasPrefix(path, cwd+string(filepath.Separator)) {
			projectScoped = true
		}
		entryHash, err := fileSHA256(path)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(digest, "entry:%s|%s\n", path, entryHash)
		if err := writePackageTreeListing(digest, metadataListingRoot(path, cwd, filepath.Clean(params.AgentDir)), &remaining); err != nil {
			return "", err
		}
	}
	if projectScoped {
		_, _ = fmt.Fprintf(digest, "cwd:%s\n", cwd)
	}
	_, _ = fmt.Fprintf(digest, "lock:%s\n", optionalFileSHA256(filepath.Join(config.UserNpmInstallRoot(params.AgentDir), "package-lock.json")))
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(content)), nil
}

func optionalFileSHA256(path string) string {
	hash, err := fileSHA256(path)
	if err != nil {
		return "absent"
	}
	return hash
}

// metadataListingRoot picks the directory whose listing fingerprints an entry:
// the owning package dir (what dependency materialization keys on), but only
// when it stays inside the project or the agent dir — the upward package.json
// search can otherwise land on a stray manifest in /tmp or $HOME, whose tree
// is neither ours to list nor boundable. Outside those scopes the entry's own
// directory stands in; the entry content hash still covers the entry itself.
func metadataListingRoot(entryPath, cwd, agentDir string) string {
	root := filepath.Dir(entryPath)
	manifest, err := owningPackageJSON(entryPath)
	if err != nil || manifest == "" {
		return root
	}
	manifestDir := filepath.Dir(manifest)
	for _, scope := range [...]string{cwd, agentDir} {
		if manifestDir == scope || strings.HasPrefix(manifestDir, scope+string(filepath.Separator)) {
			return manifestDir
		}
	}
	return root
}

// writePackageTreeListing appends a (relpath,size,mtimeNS) row for every
// regular file under root, in WalkDir's deterministic lexical order, so a
// dependency edit or install invalidates the fingerprint without hashing whole
// node_modules trees. remaining caps the total rows across every entry.
func writePackageTreeListing(digest io.Writer, root string, remaining *int) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if *remaining <= 0 {
			return fmt.Errorf("extension package tree %s exceeds %d files", root, metadataFingerprintMaxFiles)
		}
		*remaining--
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(digest, "file:%s|%d|%d\n", filepath.ToSlash(relative), info.Size(), info.ModTime().UnixNano())
		return nil
	})
}
