package host

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// The embedded orb-extension-sdk: orb's own implementation of the
// @earendil-works/pi-* SDK surface (sdk/sdk.json carries its version and
// capability manifest). It is materialized to disk at host start exactly like
// host.mjs — never npm-installed, never resolved from ~/.pi — and loader.mjs
// repoints the legacy SDK specifiers at the materialized tree via the
// ORB_EXTENSION_SDK_ROOT environment variable set in startLocked.
//
//go:embed all:sdk
var sdkFS embed.FS

// extensionSDKRootEnv names the materialized orb-extension-sdk directory for
// the host process; loader.mjs resolves the legacy pi-* specifiers against it.
const extensionSDKRootEnv = "ORB_EXTENSION_SDK_ROOT"

// materializeSDK mirrors materializeHost for a directory tree: the target is
// content-addressed under <agentDir>/host, an existing tree with the same hash
// is reused as-is, and a fresh tree is staged in a temp directory and renamed
// into place so a crashed write never leaves a half-materialized SDK behind.
func materializeSDK(agentDir string) (string, error) {
	if agentDir == "" {
		return "", errors.New("agent directory is empty")
	}
	entries, hash, err := collectSDKEntries()
	if err != nil {
		return "", err
	}
	hostDir := filepath.Join(agentDir, "host")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(hostDir, "sdk-"+hash[:16])
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return target, nil
	}
	staging, err := os.MkdirTemp(hostDir, ".sdk-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	for _, entry := range entries {
		path := filepath.Join(staging, filepath.FromSlash(entry.name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, entry.data, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		// A concurrent host start can win the rename; the content-addressed
		// name guarantees an existing target carries identical bytes.
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			return target, nil
		}
		return "", err
	}
	return target, nil
}

type sdkEntry struct {
	name string
	data []byte
}

// collectSDKEntries lists the embedded SDK files in deterministic order and
// hashes names plus contents, so any change to the tree yields a new
// materialization directory.
func collectSDKEntries() ([]sdkEntry, string, error) {
	var entries []sdkEntry
	err := fs.WalkDir(sdkFS, "sdk", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := sdkFS.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel("sdk", path)
		if err != nil {
			return err
		}
		entries = append(entries, sdkEntry{name: filepath.ToSlash(relative), data: data})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	digest := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(digest, "%s\x00%d\x00", entry.name, len(entry.data))
		digest.Write(entry.data)
	}
	return entries, fmt.Sprintf("%x", digest.Sum(nil)), nil
}
