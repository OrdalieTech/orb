package main

// Direct self-upgrade (G4, amended 2026-08-11). orb fetches the same release
// archive and checksums.txt that scripts/install.sh does, verifies the sha256,
// and replaces its own binary by atomic rename. Deliberately absent: signing,
// backups, retries, progress percentages, elevation.

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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/internal/semver"
)

const (
	// Hard ceilings on attacker-influenced input, not tuning knobs: the shipped
	// archive is ~10 MiB and the binary inside it ~20 MiB.
	selfUpdateMaxArchive   = 64 << 20
	selfUpdateMaxBinary    = 192 << 20
	selfUpdateMaxChecksums = 1 << 20
	selfUpdateMetadataWait = 30 * time.Second
	selfUpdateDownloadWait = 5 * time.Minute

	orbModulePath       = "github.com/OrdalieTech/orb"
	releaseDownloadBase = "https://github.com/OrdalieTech/orb/releases/download"

	// Ten stages, one 42ms gap between each: the mark lands in 378ms. An update
	// is not a session opening, so it plays a cut of the unfold, not all of it.
	logoRevealDelay = 42 * time.Millisecond
)

// updateRevealStages samples the unfold down to the stages the update
// trace draws; the tail is denser because that is where the mark settles.
var updateRevealStages = [...]int{0, 6, 12, 18, 24, 30, 36, 41, 45, 47}

// selfUpdater injects every effect the upgrade has, so a test never resolves or overwrites the binary running it.
type selfUpdater struct {
	currentVersion string
	releaseURL     string
	releaseBase    string
	client         *http.Client
	offline        bool
	animate        bool
	logoDelay      time.Duration
	executable     func() (string, error)
	resolveLinks   func(string) (string, error)
}

func newSelfUpdater(currentVersion string, offline bool) selfUpdater {
	return selfUpdater{
		currentVersion: currentVersion,
		releaseURL:     latestReleaseURL,
		releaseBase:    releaseDownloadBase,
		client:         http.DefaultClient,
		offline:        offline,
		logoDelay:      logoRevealDelay,
		executable:     os.Executable,
		resolveLinks:   filepath.EvalSymlinks,
	}
}

func runSelfUpdate(ctx context.Context, out io.Writer, offline, animate bool) int {
	info, ok := debug.ReadBuildInfo()
	updater := newSelfUpdater(buildVersion(version, info, ok), offline)
	// A dumb terminal has no cursor addressing, so the reveal and the spinner would
	// stack their frames instead of redrawing them.
	updater.animate = animate && os.Getenv("TERM") != "dumb"
	return updater.run(ctx, out)
}

// buildVersion recovers the version for `go install ...@latest`, which builds without the release
// ldflags: the module proxy stamps the tag into the build info. A "(devel)" build has none and skips.
func buildVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if !isDevelopmentVersion(stamped) || !ok || info.Main.Path != orbModulePath || !semver.Valid(info.Main.Version) {
		return stamped
	}
	return info.Main.Version
}

func (updater selfUpdater) run(ctx context.Context, out io.Writer) int {
	revealLogo(out, updater.animate, updater.logoDelay)
	_, _ = fmt.Fprintf(out, "Orb update\n\n  %s\n     │\n", plainVersion(updater.currentVersion))

	current, parsed := semver.Parse(updater.currentVersion)
	if !parsed || isDevelopmentVersion(updater.currentVersion) {
		return skipUpgrade(out, "development build")
	}
	if updater.offline {
		return skipUpgrade(out, "offline")
	}
	if err := httpsOnly(updater.releaseURL); err != nil {
		return failUpgrade(out, err)
	}
	// One guarded client for every request the upgrade makes: a metadata redirect off HTTPS
	// picks the release this binary trusts, so it is as dangerous as a download redirect.
	updater.client = guardRedirects(updater.client)
	stopAnimation := startUpdateAnimation(out, updater.animate, "checking release")
	tag, err := fetchLatestReleaseVersion(ctx, updater.currentVersion, updater.client, updater.releaseURL, selfUpdateMetadataWait)
	stopAnimation()
	if err != nil {
		return failUpgrade(out, err)
	}
	latest, parsed := semver.Parse(tag)
	if !parsed {
		return failUpgrade(out, fmt.Errorf("GitHub returned an unparseable version %q", tag))
	}
	if semver.Compare(latest, current) <= 0 {
		return skipUpgrade(out, "already current ✓")
	}
	// Resolving the target first means a refused install costs no download.
	target, before, err := updater.resolveTarget()
	if err != nil {
		return failUpgrade(out, err)
	}
	stopAnimation = startUpdateAnimation(out, updater.animate, "fetching release")
	payload, err := updater.download(ctx, tag)
	stopAnimation()
	if err != nil {
		return failUpgrade(out, err)
	}
	_, _ = fmt.Fprint(out, "     ● archive verified\n     │\n")
	stopAnimation = startUpdateAnimation(out, updater.animate, "replacing binary")
	err = swapBinary(target, payload, before)
	stopAnimation()
	if err != nil {
		return failUpgrade(out, err)
	}
	_, _ = fmt.Fprintf(out, "     ● binary replaced\n     │\n  %s ✓\n", plainVersion(tag))
	return 0
}

func skipUpgrade(out io.Writer, reason string) int {
	_, _ = fmt.Fprintf(out, "     └─ %s\n", reason)
	return 0
}

func failUpgrade(out io.Writer, err error) int {
	_, _ = fmt.Fprintf(out, "     × %s\n     │\n  unchanged\n", err)
	return 1
}

// Each stage clears and rewinds the fixed-height canvas. Explicit CRLF keeps
// redraws independent of the terminal's newline mode.
func revealLogo(out io.Writer, enabled bool, delay time.Duration) {
	if !enabled {
		return
	}
	for stage, index := range updateRevealStages {
		if stage > 0 {
			time.Sleep(delay)
			_, _ = fmt.Fprintf(out, "\x1b[%dA", orbalogo.Height)
		}
		for _, row := range orbalogo.Frame(index) {
			_, _ = fmt.Fprintf(out, "\r\x1b[2K%s\r\n", row)
		}
	}
	_, _ = fmt.Fprint(out, "\r\n")
}

// startUpdateAnimation owns the active line until stop returns. Callers only
// print durable state after stop, so animation can never outrun the operation.
func startUpdateAnimation(out io.Writer, enabled bool, label string) func() {
	if !enabled {
		return func() {}
	}
	frames := [...]string{"·  ", "·· ", "···"}
	_, _ = fmt.Fprintf(out, "     %s %s", frames[0], label)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(180 * time.Millisecond)
		defer ticker.Stop()
		for frame := 1; ; frame = (frame + 1) % len(frames) {
			select {
			case <-ticker.C:
				_, _ = fmt.Fprintf(out, "\r\x1b[2K     %s %s", frames[frame], label)
			case <-stop:
				_, _ = fmt.Fprint(out, "\r\x1b[2K")
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func plainVersion(value string) string { return strings.TrimPrefix(strings.TrimSpace(value), "v") }

// resolveTarget names the canonical regular file this process runs from. An
// install orb cannot write simply fails when the staged file is created.
func (updater selfUpdater) resolveTarget() (string, os.FileInfo, error) {
	// os.Executable resolves the kernel's own link but not a PATH symlink into an install
	// directory; the file the installer owns is the one to replace.
	resolved, err := updater.executable()
	if err == nil {
		resolved, err = updater.resolveLinks(resolved)
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

// download builds the exact URLs scripts/install.sh builds.
func (updater selfUpdater) download(ctx context.Context, tag string) ([]byte, error) {
	tag = strings.TrimSpace(tag)
	// semver.Parse alone would pass build metadata such as "0.5.0+/../evil".
	if strings.ContainsAny(tag, "/+%?#=") || !semver.Valid(tag) {
		return nil, fmt.Errorf("release tag %q is not a plain version", tag)
	}
	name := fmt.Sprintf("orb_%s_%s_%s.tar.gz", plainVersion(tag), runtime.GOOS, runtime.GOARCH)
	base := updater.releaseBase + "/" + tag + "/"

	checksums, err := updater.get(ctx, base+"checksums.txt", selfUpdateMaxChecksums)
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(string(checksums), name)
	if err != nil {
		return nil, err
	}
	archive, err := updater.get(ctx, base+name, selfUpdateMaxArchive)
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

func (updater selfUpdater) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	if err := httpsOnly(endpoint); err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, selfUpdateDownloadWait)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid release URL")
	}
	request.Header.Set("User-Agent", "orb/"+updater.currentVersion)
	name := path.Base(endpoint)
	response, err := updater.client.Do(request)
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
	archive := tar.NewReader(io.LimitReader(gzipReader, selfUpdateMaxBinary))
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
		case header.Size <= 0 || header.Size > selfUpdateMaxBinary:
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

// swapBinary stages the payload beside the target so the rename is atomic on the same filesystem,
// and leaves the target untouched on every failure.
func swapBinary(target string, payload []byte, before os.FileInfo) error {
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
