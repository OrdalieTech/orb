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
	metadataCacheName = "metadata-cache.json"
	// Version 2: entry files and the npm lockfile are stamped (size, mtime)
	// like every other fingerprint input instead of content-hashed, and the
	// package-tree listings are emitted once per distinct root — both change
	// the fingerprint, so version-1 caches must not be read.
	metadataCacheVersion = 2
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

// MetadataCacheParams is the whole fingerprint input set: the same values
// fingerprint the cache at write (from Manager options and the resolved
// runtime) and at read (from the metadata command's own discovery, with the
// runtime identity taken from the snapshot), so equality proves the snapshot
// still describes what a full load would register.
type MetadataCacheParams struct {
	AgentDir       string
	CWD            string
	ProjectTrusted bool
	Paths          []string
	RuntimeName    string
	RuntimeVersion string
	RuntimePath    string
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
	params.RuntimeName, params.RuntimeVersion, params.RuntimePath = cache.RuntimeName, cache.RuntimeVersion, cache.RuntimePath
	fingerprint, err := metadataFingerprint(params)
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

// writeMetadataCache snapshots the registration-static metadata of a successful
// full load and persists it in the background (temp file + rename, 0600):
// fingerprinting walks the extension package trees, which no interactive
// startup should wait on for a file only --help/--list-models ever read. The
// snapshot is taken synchronously so the goroutine shares nothing with the live
// manager; the write itself is best-effort, and a process that exits first
// simply leaves the cache as the previous run wrote it. Manager.Close waits for
// the write, so a closed manager has published whatever it was going to.
func (manager *Manager) writeMetadataCache(success map[string]bool, result LoadResult) {
	runtime := manager.Runtime()
	if runtime == nil || manager.options.SkipMetadataCacheWrite {
		return
	}
	params := MetadataCacheParams{
		AgentDir:       manager.options.AgentDir,
		CWD:            manager.options.CWD,
		ProjectTrusted: manager.options.ProjectTrusted,
		Paths:          append([]string(nil), result.Paths...),
		RuntimeName:    runtime.Name,
		RuntimeVersion: runtime.Version,
		RuntimePath:    runtime.Path,
	}
	cache := MetadataCache{
		Version:        metadataCacheVersion,
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
		// state returns a deep clone, so the goroutine never reads registrations
		// the live host may replace.
		if state := manager.state(entry.ID); state != nil {
			snapshot.Providers = state.Providers
		}
		cache.Extensions = append(cache.Extensions, snapshot)
	}
	manager.cacheWrites.Add(1)
	go func() {
		defer manager.cacheWrites.Done()
		fingerprint, err := metadataFingerprint(params)
		if err != nil {
			return
		}
		cache.Fingerprint = fingerprint
		encoded, err := json.Marshal(&cache)
		if err != nil {
			return
		}
		_ = writeHostFile(filepath.Join(params.AgentDir, "host"), metadataCacheName, encoded)
	}()
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
// sorted entry set with a stamp per entry, one listing per distinct
// containing-package tree, the npm install root lockfile, the trust decision,
// and the cwd whenever a project-scoped entry participates. Any I/O error
// disables the cache rather than fingerprinting an unknown state.
func metadataFingerprint(params MetadataCacheParams) (string, error) {
	digest := sha256.New()
	_, _ = io.WriteString(digest, embeddedSourceHashes())
	_, _ = fmt.Fprintf(digest, "runtime:%s|%s|%s\n", params.RuntimeName, params.RuntimeVersion, params.RuntimePath)
	_, _ = fmt.Fprintf(digest, "trusted:%t\n", params.ProjectTrusted)
	paths := append([]string(nil), params.Paths...)
	sort.Strings(paths)
	cwd := filepath.Clean(params.CWD)
	projectScoped := false
	for _, path := range paths {
		if strings.HasPrefix(path, cwd+string(filepath.Separator)) {
			projectScoped = true
		}
		stamp, err := fileStamp(path)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(digest, "entry:%s|%s\n", path, stamp)
	}
	remaining := metadataFingerprintMaxFiles
	for _, root := range metadataListingRoots(paths, cwd, filepath.Clean(params.AgentDir)) {
		_, _ = fmt.Fprintf(digest, "root:%s\n", root)
		if err := writePackageTreeListing(digest, root, &remaining); err != nil {
			return "", err
		}
	}
	if projectScoped {
		_, _ = fmt.Fprintf(digest, "cwd:%s\n", cwd)
	}
	_, _ = fmt.Fprintf(digest, "lock:%s\n", optionalFileStamp(filepath.Join(config.UserNpmInstallRoot(params.AgentDir), "package-lock.json")))
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

// fileStamp identifies a file the way the package-tree listing identifies every
// other fingerprint input — by size and mtime, not content. Content-hashing
// entry files and lockfiles bought no extra safety (the whole dependency tree
// is already trusted on mtime) and made metadata commands read every byte of
// bundled multi-MB extensions.
func fileStamp(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano()), nil
}

func optionalFileStamp(path string) string {
	stamp, err := fileStamp(path)
	if err != nil {
		return "absent"
	}
	return stamp
}

// metadataListingRoots collects the listing roots of an entry set as a sorted,
// deduplicated slice: two extensions in one npm package, or several
// project-scoped entries under one repo, share a root, and listing it once
// keeps every pass out of the shared file budget it would otherwise multiply.
// Sorted roots keep the digest deterministic.
func metadataListingRoots(paths []string, cwd, agentDir string) []string {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if root := metadataListingRoot(path, cwd, agentDir); root != "" {
			unique[root] = struct{}{}
		}
	}
	roots := make([]string, 0, len(unique))
	for root := range unique {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

// metadataListingRoot picks the directory whose listing fingerprints an entry:
// the owning package dir (what dependency materialization keys on), but only
// when it stays inside the project or the agent dir — the upward package.json
// search can otherwise land on a stray manifest in /tmp or $HOME, whose tree
// is neither ours to list nor boundable. Outside those scopes the entry's own
// directory stands in; the entry stamp still covers the entry itself. The
// project cwd is never a listing root: in a JS repo it owns the manifest every
// project extension resolves to, and walking the whole checkout on every
// metadata command would blow the file budget and disable the cache outright,
// so such an entry falls back to its own directory (and contributes no listing
// at all when that directory is the cwd).
func metadataListingRoot(entryPath, cwd, agentDir string) string {
	entryDir := filepath.Dir(entryPath)
	root := entryDir
	if manifest, err := owningPackageJSON(entryPath); err == nil && manifest != "" {
		manifestDir := filepath.Dir(manifest)
		for _, scope := range [...]string{cwd, agentDir} {
			if manifestDir == scope || strings.HasPrefix(manifestDir, scope+string(filepath.Separator)) {
				root = manifestDir
				break
			}
		}
	}
	if root != cwd {
		return root
	}
	if entryDir != cwd {
		return entryDir
	}
	return ""
}

// writePackageTreeListing appends a (relpath,size,mtimeNS) row for every
// regular file under root, in WalkDir's deterministic lexical order, so a
// dependency edit or install invalidates the fingerprint without hashing whole
// node_modules trees. remaining caps the total rows across every root.
func writePackageTreeListing(digest io.Writer, root string, remaining *int) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// A checkout's .git changes on every commit and can never change
			// what a load registers; walking it only burns the file budget.
			if entry.Name() == ".git" && path != root {
				return fs.SkipDir
			}
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
