// Package selfupdate replaces the running orb with a signed release: it
// fetches the same archive and checksums.txt that scripts/install.sh does,
// verifies the signature and sha256, and swaps the binary by atomic rename.
// Deliberately absent: backups, retries, elevation.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/semver"
)

const (
	// Hard ceilings on attacker-influenced input, not tuning knobs: the shipped
	// archive is ~10 MiB and the binary inside it ~20 MiB.
	maxArchive   = 64 << 20
	maxBinary    = 192 << 20
	maxChecksums = 1 << 20
	maxRelease   = 64 << 10
	MetadataWait = 30 * time.Second
	DownloadWait = 5 * time.Minute

	modulePath       = "github.com/OrdalieTech/orb"
	LatestReleaseURL = "https://api.github.com/repos/OrdalieTech/orb/releases/latest"
	releaseBase      = "https://github.com/OrdalieTech/orb/releases/download"
)

// ReleaseKey signs every release's checksums.txt (checksums.txt.sig, raw Ed25519): an update
// trusts no file GitHub serves unless this key vouches for it. The private half is the
// ORB_RELEASE_SIGNING_KEY secret, with a backup at ~/.config/orb/release-signing.pem.
var ReleaseKey = ed25519.PublicKey(mustBase64("/xaew4KpYjMRLnDkPbbkbUgToYycCEv5Ur5LWpfLMTc="))

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

var (
	ErrDevelopment = errors.New("development build")
	ErrOffline     = errors.New("offline")
	ErrCurrent     = errors.New("already current")
)

// Updater injects every effect an update has, so a test never resolves or
// overwrites the binary running it.
type Updater struct {
	Key            ed25519.PublicKey
	CurrentVersion string
	ReleaseURL     string
	ReleaseBase    string
	Client         *http.Client
	Offline        bool
	Executable     func() (string, error)
	ResolveLinks   func(string) (string, error)
}

// New updates this build, whose stamped version is stamped.
func New(stamped string, offline bool) Updater {
	info, ok := debug.ReadBuildInfo()
	return Updater{
		Key:            ReleaseKey,
		CurrentVersion: BuildVersion(stamped, info, ok),
		ReleaseURL:     LatestReleaseURL,
		ReleaseBase:    releaseBase,
		Client:         http.DefaultClient,
		Offline:        offline,
		Executable:     os.Executable,
		ResolveLinks:   filepath.EvalSymlinks,
	}
}

// BuildVersion recovers the version for `go install ...@latest`, which builds without the release
// ldflags: the module proxy stamps the tag into the build info. A "(devel)" build has none and skips.
func BuildVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if !IsDevelopment(stamped) || !ok || info.Main.Path != modulePath || !semver.Valid(info.Main.Version) {
		return stamped
	}
	return info.Main.Version
}

func IsDevelopment(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || value == "dev" || strings.Contains(value, "-dev")
}

func Plain(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "v") }

// Update brings the running binary to the latest release. step announces
// "checking release", "fetching release" and "replacing binary" as each
// begins and returns the func that ends it. It returns the release installed
// and the file replaced; ErrDevelopment, ErrOffline and ErrCurrent leave
// everything as it was.
func (u Updater) Update(ctx context.Context, step func(string) func()) (tag, target string, err error) {
	current, parsed := semver.Parse(u.CurrentVersion)
	if !parsed || IsDevelopment(u.CurrentVersion) {
		return "", "", ErrDevelopment
	}
	if u.Offline {
		return "", "", ErrOffline
	}
	if err := httpsOnly(u.ReleaseURL); err != nil {
		return "", "", err
	}
	done := step("checking release")
	tag, err = u.Latest(ctx)
	done()
	if err != nil {
		return "", "", err
	}
	latest, parsed := semver.Parse(tag)
	if !parsed {
		return "", "", fmt.Errorf("GitHub returned an unparseable version %q", tag)
	}
	if semver.Compare(latest, current) <= 0 {
		return "", "", ErrCurrent
	}
	// Resolving the target first means a refused install costs no download.
	target, before, err := u.resolveTarget()
	if err != nil {
		return "", "", err
	}
	done = step("fetching release")
	payload, err := u.Download(ctx, tag, runtime.GOOS, runtime.GOARCH)
	done()
	if err != nil {
		return "", "", err
	}
	done = step("replacing binary")
	err = Swap(target, payload, before)
	done()
	return tag, target, err
}

// Latest is the tag of the latest release.
func (u Updater) Latest(ctx context.Context) (string, error) {
	// A metadata redirect off HTTPS picks the release this binary trusts, so it is as dangerous
	// as a download redirect.
	return LatestTag(ctx, u.CurrentVersion, guardRedirects(u.Client), u.ReleaseURL, MetadataWait)
}

// LatestTag asks endpoint, GitHub's latest-release API, for its tag.
func LatestTag(ctx context.Context, currentVersion string, client *http.Client, endpoint string, timeout time.Duration) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.New("invalid release endpoint")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "orb/"+currentVersion)
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return "", errors.New("timed out")
		}
		if errors.Is(err, context.Canceled) {
			return "", errors.New("canceled")
		}
		return "", errors.New("network error")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("GitHub returned %s", response.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, maxRelease)).Decode(&release) != nil {
		return "", errors.New("invalid GitHub response")
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return "", errors.New("GitHub response had no version")
	}
	return tag, nil
}

// resolveTarget names the canonical regular file this process runs from. An
// install orb cannot write simply fails when the staged file is created.
func (u Updater) resolveTarget() (string, os.FileInfo, error) {
	// os.Executable resolves the kernel's own link but not a PATH symlink into an install
	// directory; the file the installer owns is the one to replace.
	resolved, err := u.Executable()
	if err == nil {
		resolved, err = u.ResolveLinks(resolved)
	}
	if err != nil {
		return "", nil, fmt.Errorf("could not locate the running orb binary: %w", err)
	}
	if managedInstall(resolved) {
		return "", nil, fmt.Errorf("%s is managed by its package manager; update orb the same way you installed it", resolved)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", resolved)
	}
	return resolved, info, nil
}

// ponytail: canonical store paths, not a package-manager probe — these managers own the file.
// Homebrew has no single prefix (Intel installs land under /usr/local, and a custom --prefix
// anywhere), so its cellar directories match as a path component rather than a root.
func managedInstall(canonical string) bool {
	for _, prefix := range []string{"/nix/store/", "/snap/", "/opt/homebrew/", "/home/linuxbrew/"} {
		if strings.HasPrefix(canonical, prefix) {
			return true
		}
	}
	return strings.Contains(canonical, "/Cellar/") || strings.Contains(canonical, "/Caskroom/")
}

// Download fetches and verifies the orb binary of release tag for goos/goarch,
// from the exact URLs scripts/install.sh builds.
func (u Updater) Download(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	if (goos != "linux" && goos != "darwin") || (goarch != "amd64" && goarch != "arm64") {
		return nil, fmt.Errorf("unsupported Orb platform: %s/%s", goos, goarch)
	}
	tag = strings.TrimSpace(tag)
	// semver.Parse alone would pass build metadata such as "0.5.0+/../evil".
	if strings.ContainsAny(tag, "/+%?#=") || !semver.Valid(tag) {
		return nil, fmt.Errorf("release tag %q is not a plain version", tag)
	}
	name := fmt.Sprintf("orb_%s_%s_%s.tar.gz", Plain(tag), goos, goarch)
	base := u.ReleaseBase + "/" + tag + "/"

	checksums, err := u.get(ctx, base+"checksums.txt", maxChecksums)
	if err != nil {
		return nil, err
	}
	signature, err := u.get(ctx, base+"checksums.txt.sig", ed25519.SignatureSize)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(u.Key, checksums, signature) {
		return nil, errors.New("checksums.txt is not signed by Orb's release key")
	}
	want, err := checksumFor(string(checksums), name)
	if err != nil {
		return nil, err
	}
	archive, err := u.get(ctx, base+name, maxArchive)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s failed its sha256 checksum", name)
	}
	return extractOrb(bytes.NewReader(archive))
}

// checksumFor accepts exactly one well-formed line for the asset: a second is a tampered or
// ambiguous file, not a preference to resolve.
func checksumFor(body, name string) (string, error) {
	want := ""
	for line := range strings.SplitSeq(body, "\n") {
		digest, entry, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if !ok || entry != name {
			continue
		}
		if want != "" {
			return "", fmt.Errorf("checksums.txt lists %s more than once", name)
		}
		if _, err := hex.DecodeString(digest); err != nil || len(digest) != hex.EncodedLen(sha256.Size) {
			return "", fmt.Errorf("checksums.txt has a malformed sha256 for %s", name)
		}
		want = strings.ToLower(digest)
	}
	if want == "" {
		return "", fmt.Errorf("checksums.txt has no sha256 for %s", name)
	}
	return want, nil
}

func (u Updater) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	if err := httpsOnly(endpoint); err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, DownloadWait)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid release URL")
	}
	request.Header.Set("User-Agent", "orb/"+u.CurrentVersion)
	name := path.Base(endpoint)
	response, err := guardRedirects(u.Client).Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not download %s", name)
	}
	defer func() { _ = response.Body.Close() }()
	// GitHub redirects downloads to its object store; a redirect off HTTPS would
	// hand the payload to the network.
	if response.Request != nil && response.Request.URL.Scheme != "https" {
		return nil, fmt.Errorf("refusing a release URL that is not HTTPS: %s", response.Request.URL)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: GitHub returned %s", name, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, fmt.Errorf("could not read %s within %d bytes", name, limit)
	}
	return body, nil
}

func httpsOnly(endpoint string) error {
	if parsed, err := url.Parse(endpoint); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("refusing a release URL that is not HTTPS: %s", endpoint)
	}
	return nil
}

// guardRedirects copies the injected client so no hop can leave HTTPS, keeping any callback the
// caller set and the net/http ceiling of ten redirects when it set none.
func guardRedirects(client *http.Client) *http.Client {
	guarded, inner := *client, client.CheckRedirect
	guarded.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := httpsOnly(request.URL.String()); err != nil {
			return err
		}
		if inner != nil {
			return inner(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &guarded
}

// extractOrb takes exactly one root-level regular "orb" entry and never writes anything the archive
// names: sibling entries (LICENSE, README) are read past, so no archived path reaches the filesystem.
func extractOrb(reader io.Reader) ([]byte, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, errors.New("release archive is not gzip")
	}
	defer func() { _ = gzipReader.Close() }()
	archive := tar.NewReader(io.LimitReader(gzipReader, maxBinary))
	var payload []byte
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("release archive is corrupt")
		}
		if header.Name != "orb" {
			continue
		}
		switch {
		case payload != nil:
			return nil, errors.New("release archive has more than one orb entry")
		case header.Typeflag != tar.TypeReg:
			return nil, errors.New("release archive orb entry is not a regular file")
		case header.Size <= 0 || header.Size > maxBinary:
			return nil, fmt.Errorf("release archive orb entry is %d bytes, outside the accepted range", header.Size)
		}
		payload = make([]byte, header.Size)
		if _, err := io.ReadFull(archive, payload); err != nil {
			return nil, errors.New("release archive orb entry is truncated")
		}
	}
	if payload == nil {
		return nil, errors.New("release archive has no orb entry")
	}
	return payload, nil
}

// Swap stages the payload beside the target so the rename is atomic on the same filesystem,
// and leaves the target untouched on every failure.
func Swap(target string, payload []byte, before os.FileInfo) error {
	directory := filepath.Dir(target)
	staged, err := os.CreateTemp(directory, ".orb-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", directory, err)
	}
	stagedPath := staged.Name()
	discard := func(cause error) error {
		_ = staged.Close()
		_ = os.Remove(stagedPath)
		return cause
	}
	// The mode comes from the target, not the archive, and the bytes reach the disk first.
	_, writeErr := staged.Write(payload)
	if err := errors.Join(writeErr, staged.Chmod(before.Mode().Perm()), staged.Sync(), staged.Close()); err != nil {
		return discard(fmt.Errorf("could not stage the new binary: %w", err))
	}
	release, err := filelock.Acquire(target)
	if err != nil {
		return discard(fmt.Errorf("another orb update holds %s", target))
	}
	defer func() { _ = release() }()
	// Between the first stat and the lock another installer may have replaced the target;
	// overwriting it now would silently undo their work.
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return discard(fmt.Errorf("%s changed while the update was staged", target))
	}
	if err := os.Rename(stagedPath, target); err != nil {
		return discard(fmt.Errorf("could not replace %s: %w", target, err))
	}
	// The rename already succeeded; a directory fsync that fails is a durability gap on a crash.
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
