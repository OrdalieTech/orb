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
	"unicode/utf8"

	"golang.org/x/term"

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

	// Twelve ticks — ten mark stages, then Orb, then update — and the process
	// rule grows on the same clock, 0% to 100% by the time the wordmark lands.
	// An update is not a session opening, so it plays a cut of the unfold, a
	// touch slow so an instant outcome (dev build, offline) still reads.
	logoRevealDelay = 42 * time.Millisecond

	// Editorial plate: the mark and wordmark sit top-left like the home
	// screen; the process line under them spans every column, its steps
	// linked by faint full-width rules. That line is the point of the screen.
	updateIndent    = "  "
	updateLockupGap = 4
	updateBarCap    = 72
	updateMinLink   = 2

	// The rules between steps recede so the steps themselves read. Bold and
	// faint are the only styling: both survive light and dark palettes.
	faintOn  = "\x1b[2m"
	boldOn   = "\x1b[1m"
	styleOff = "\x1b[22m"
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
	width          int
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
	from := plainVersion(updater.currentVersion)
	screen := newUpdateScreen(out, updater)
	screen.reveal(from)

	current, parsed := semver.Parse(updater.currentVersion)
	if !parsed || isDevelopmentVersion(updater.currentVersion) {
		return screen.done(0, from, "development build")
	}
	if updater.offline {
		return screen.done(0, from, "offline")
	}
	if err := httpsOnly(updater.releaseURL); err != nil {
		return screen.done(1, from, err.Error(), "unchanged")
	}
	// One guarded client for every request the upgrade makes: a metadata redirect off HTTPS
	// picks the release this binary trusts, so it is as dangerous as a download redirect.
	updater.client = guardRedirects(updater.client)
	stop := screen.spin(from, "checking release")
	tag, err := fetchLatestReleaseVersion(ctx, updater.currentVersion, updater.client, updater.releaseURL, selfUpdateMetadataWait)
	stop()
	if err != nil {
		return screen.done(1, from, err.Error(), "unchanged")
	}
	latest, parsed := semver.Parse(tag)
	if !parsed {
		return screen.done(1, from, fmt.Sprintf("GitHub returned an unparseable version %q", tag), "unchanged")
	}
	if semver.Compare(latest, current) <= 0 {
		return screen.done(0, from, "already current ✓")
	}
	// Resolving the target first means a refused install costs no download.
	target, before, err := updater.resolveTarget()
	if err != nil {
		return screen.done(1, from, err.Error(), "unchanged")
	}
	stop = screen.spin(from, "fetching release")
	payload, err := updater.download(ctx, tag)
	stop()
	if err != nil {
		return screen.done(1, from, err.Error(), "unchanged")
	}
	stop = screen.spin(from, "archive verified", "replacing binary")
	err = swapBinary(target, payload, before)
	stop()
	if err != nil {
		return screen.done(1, from, "archive verified", err.Error(), "unchanged")
	}
	return screen.done(0, from, "archive verified", "binary replaced", plainVersion(tag)+" ✓")
}

type updateScreen struct {
	out     io.Writer
	animate bool
	styled  bool
	delay   time.Duration
	width   int
}

func newUpdateScreen(out io.Writer, updater selfUpdater) updateScreen {
	return updateScreen{out: out, animate: updater.animate, styled: updater.animate && os.Getenv("NO_COLOR") == "", delay: updater.logoDelay, width: updateBarWidth(out, updater.width)}
}

func (screen updateScreen) done(code int, cells ...string) int {
	if screen.styled {
		last := len(cells) - 1
		cells = append(append([]string{}, cells[:last]...), boldOn+cells[last]+styleOff)
	}
	if screen.animate {
		_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s\r\n", screen.bar(cells))
	} else {
		_, _ = fmt.Fprintln(screen.out, screen.bar(cells))
	}
	_, _ = fmt.Fprint(screen.out, "\n\n")
	return code
}

func (screen updateScreen) paint(cells ...string) {
	_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s", screen.bar(cells))
}

// bar is the process line at the screen's width; the links go faint only
// when the terminal can render it, so piped output carries no escape bytes.
func (screen updateScreen) bar(cells []string) string {
	return joinBarStyled(screen.width, cells, screen.styled)
}

func (screen updateScreen) reveal(from string) {
	revealLockup(screen.out, screen.animate, screen.delay, screen.styled, screen.width, from)
}

func (screen updateScreen) spin(held ...string) func() {
	if !screen.animate {
		return func() {}
	}
	label := held[len(held)-1]
	prefix := held[:len(held)-1]
	frames := [...]string{"·  ", "·· ", "···"}
	cell := func(frame int) string {
		dots := frames[frame]
		if screen.styled {
			dots = faintOn + dots + styleOff
		}
		return dots + " " + label
	}
	_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s", screen.bar(append(append([]string{}, prefix...), cell(0))))
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(140 * time.Millisecond)
		defer ticker.Stop()
		for frame := 1; ; frame = (frame + 1) % len(frames) {
			select {
			case <-ticker.C:
				screen.paint(append(append([]string{}, prefix...), cell(frame))...)
			case <-stop:
				_, _ = fmt.Fprint(screen.out, "\r\x1b[2K")
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func updateBarWidth(out io.Writer, override int) int {
	if override > 0 {
		return override
	}
	if file, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return updateBarCap
}

func joinBar(width int, cells []string) string {
	return joinBarStyled(width, cells, false)
}

func joinBarStyled(width int, cells []string, faintLinks bool) string {
	if width <= 0 {
		width = updateBarCap
	}
	gaps := len(cells) - 1
	if gaps <= 0 {
		return updateIndent + cells[0]
	}
	inner := width - len(updateIndent)
	text := 0
	for _, cell := range cells {
		text += visibleWidth(cell)
	}
	if room := inner - text - gaps*2; room >= gaps*updateMinLink {
		base, extra := room/gaps, room%gaps
		var body strings.Builder
		body.WriteString(updateIndent)
		for index, cell := range cells {
			if index > 0 {
				dashes := base
				if index <= extra {
					dashes++
				}
				rule := strings.Repeat("─", dashes)
				if faintLinks {
					rule = faintOn + rule + styleOff
				}
				body.WriteString(" " + rule + " ")
			}
			body.WriteString(cell)
		}
		return body.String()
	}
	return updateIndent + strings.Join(cells, " · ")
}

// SGR sequences style glyphs without occupying columns.
func visibleWidth(cell string) int {
	width, escape := 0, false
	for _, r := range cell {
		switch {
		case escape:
			escape = r != 'm'
		case r == '\x1b':
			escape = true
		default:
			width++
		}
	}
	return width
}

func lockupRows(index, wordmark int, styled bool) []string {
	mark := orbalogo.CompactFrame(index)
	info := [orbalogo.CompactHeight]string{}
	if wordmark >= 1 {
		info[2] = "Orb"
	}
	if wordmark >= 2 {
		info[3] = "update"
	}
	rows := make([]string, 0, orbalogo.CompactHeight+2)
	rows = append(rows, "")
	for i, row := range mark {
		pad := orbalogo.CompactWidth - utf8.RuneCountInString(row)
		line := updateIndent + row + strings.Repeat(" ", pad)
		if info[i] != "" {
			cell := info[i]
			if styled {
				if cell == "update" || wordmark == 1 {
					cell = faintOn + cell + styleOff
				} else {
					cell = boldOn + cell + styleOff
				}
			}
			line += strings.Repeat(" ", updateLockupGap) + cell
		}
		line = strings.TrimRight(line, " ")
		if styled && line != "" && info[i] == "" {
			line = boldOn + line + styleOff
		}
		rows = append(rows, line)
	}
	rows = append(rows, "")
	return rows
}

func writeLockup(out io.Writer, index, wordmark int, styled bool) {
	for _, row := range lockupRows(index, wordmark, styled) {
		_, _ = fmt.Fprintln(out, row)
	}
}

// The logo appears first, then the wordmark lands on it, then the rule
// appears under them — and from there the mark's own unfold drives the rule
// to 100%. The wordmark sets the rhythm; the growth is one motion. Explicit
// CRLF keeps redraws independent of the terminal's newline mode, and the
// frame ends on the bar row without a newline so the run's spin and done
// redraw that row in place instead of stacking a second line.
func revealLockup(out io.Writer, enabled bool, delay time.Duration, styled bool, width int, from string) {
	if !enabled {
		writeLockup(out, orbalogo.FrameCount-1, 2, styled)
		return
	}
	canvas := len(lockupRows(0, 0, styled))
	frames := len(updateRevealStages)
	full := float64(frames - 1)
	draw := func(index, wordmark int, progress float64) {
		for _, row := range lockupRows(index, wordmark, styled) {
			_, _ = fmt.Fprintf(out, "\r\x1b[2K%s\r\n", row)
		}
		bar := ""
		if progress >= 0 {
			bar = barProgress(width, from, progress, styled)
		}
		_, _ = fmt.Fprintf(out, "\r\x1b[2K%s", bar)
	}
	draw(updateRevealStages[0], 0, -1)
	for wordmark := 1; wordmark <= 2; wordmark++ {
		hold := delay
		if wordmark == 1 {
			hold = 2 * delay
		}
		time.Sleep(hold)
		_, _ = fmt.Fprintf(out, "\x1b[%dA", canvas)
		draw(updateRevealStages[0], wordmark, -1)
	}
	for stage := 1; stage < frames; stage++ {
		t := float64(stage) / full
		time.Sleep(time.Duration(float64(delay) * (0.7 + 0.9*t)))
		_, _ = fmt.Fprintf(out, "\x1b[%dA", canvas)
		draw(updateRevealStages[stage], 2, 1-(1-t)*(1-t)*(1-t))
	}
	time.Sleep(4 * delay)
}

// barProgress is the growing rule during the reveal: the from cell anchored
// left, the rule reaching for the right edge as the mark arrives.
func barProgress(width int, from string, progress float64, styled bool) string {
	if width <= 0 {
		width = updateBarCap
	}
	avail := width - len(updateIndent) - utf8.RuneCountInString(from) - 1
	if avail < 1 {
		return updateIndent + from
	}
	dashes := max(1, int(float64(avail)*progress))
	rule := strings.Repeat("─", dashes)
	if styled {
		if progress < 1 && dashes > 1 {
			rule = faintOn + strings.Repeat("─", dashes-1) + styleOff + "─"
		} else {
			rule = faintOn + rule + styleOff
		}
	}
	return updateIndent + from + " " + rule
}

// startUpdateAnimation owns the active bar until stop returns. Callers only
// print durable state after stop, so animation can never outrun the operation.
func startUpdateAnimation(out io.Writer, enabled bool, width int, cells ...string) func() {
	return (updateScreen{out: out, animate: enabled, width: width}).spin(cells...)
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
