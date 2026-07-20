package upstreamsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run fetches and analyzes one upstream revision, then optionally promotes it.
func Run(ctx context.Context, config Config) (Result, error) {
	if config.Bump && config.DryRun {
		return Result{}, fmt.Errorf("--bump and --dry-run are mutually exclusive")
	}
	root, err := resolveRoot(config.Root)
	if err != nil {
		return Result{}, err
	}
	if config.UpstreamDir == "" {
		config.UpstreamDir = filepath.Join(root, ".upstream")
	} else if !filepath.IsAbs(config.UpstreamDir) {
		config.UpstreamDir = filepath.Join(root, config.UpstreamDir)
	}
	if config.Target == "" {
		config.Target = "origin/main"
	}
	if config.Now.IsZero() {
		config.Now = time.Now().UTC()
	} else {
		config.Now = config.Now.UTC()
	}
	date := config.Now.Format(time.DateOnly)
	if config.ReportPath == "" {
		config.ReportPath = filepath.Join("docs", "sync", "reports", date+".md")
	}

	lock, err := readLock(filepath.Join(root, "UPSTREAM.lock"))
	if err != nil {
		return Result{}, err
	}
	mirrorData, err := os.ReadFile(filepath.Join(root, "docs", "MIRROR.md"))
	if err != nil {
		return Result{}, fmt.Errorf("read MIRROR.md: %w", err)
	}
	mirror, err := parseMirror(mirrorData)
	if err != nil {
		return Result{}, err
	}
	if config.Bump {
		if err := requireCleanPromotionPaths(ctx, root); err != nil {
			return Result{}, err
		}
	}
	if err := ensureUpstream(ctx, config.UpstreamDir, lock.Repo, config.Fetch); err != nil {
		return Result{}, err
	}
	if err := requireCleanUpstream(ctx, config.UpstreamDir); err != nil {
		return Result{}, err
	}
	if _, err := git(ctx, config.UpstreamDir, "cat-file", "-e", lock.Commit+"^{commit}"); err != nil {
		return Result{}, fmt.Errorf("pinned upstream commit %s is unavailable: %w", lock.Commit, err)
	}
	target, err := git(ctx, config.UpstreamDir, "rev-parse", "--verify", "--end-of-options", config.Target+"^{commit}")
	if err != nil {
		return Result{}, fmt.Errorf("resolve target %q: %w", config.Target, err)
	}
	target = strings.TrimSpace(target)
	result := Result{Base: lock, TargetCommit: target, TargetRef: config.Target}
	result.TargetDate, result.TargetSubject, err = targetMetadata(ctx, config.UpstreamDir, target)
	if err != nil {
		return Result{}, err
	}
	result.TargetVersion = targetVersion(ctx, config.UpstreamDir, target, lock.Version)
	candidateLock := Lock{Repo: lock.Repo, Commit: target, Version: result.TargetVersion, SyncedAt: date}
	result.Descendant, err = isAncestor(ctx, config.UpstreamDir, lock.Commit, target)
	if err != nil {
		return Result{}, err
	}
	result.Changes, err = changedPaths(ctx, config.UpstreamDir, lock.Commit, target, mirror)
	if err != nil {
		return Result{}, err
	}
	for _, change := range result.Changes {
		if len(change.Targets) == 0 {
			result.UnmappedPathCount++
		}
	}

	temporary, err := os.MkdirTemp("", "pi-go-sync-fixtures-*")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	candidate := filepath.Join(temporary, "fixtures")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		return Result{}, err
	}
	originalHead, err := git(ctx, config.UpstreamDir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	originalHead = strings.TrimSpace(originalHead)
	if _, err := git(ctx, config.UpstreamDir, "checkout", "--detach", target); err != nil {
		return Result{}, fmt.Errorf("check out target %s: %w", target, err)
	}
	generate := config.generate
	if generate == nil {
		generate = generateFixtures
	}
	extractionOutput, extractionErr := generate(ctx, root, config.UpstreamDir, candidate, target)
	if _, restoreErr := git(context.WithoutCancel(ctx), config.UpstreamDir, "checkout", "--detach", originalHead); restoreErr != nil {
		return Result{}, fmt.Errorf("restore upstream checkout %s: %w", originalHead, restoreErr)
	}
	result.Extraction = checkFrom(extractionOutput, extractionErr)
	result.Conformance = Check{Status: "skipped"}
	if extractionErr == nil {
		result.FixtureChanges, err = compareFixtures(filepath.Join(root, "conformance", "fixtures"), candidate)
		if err != nil {
			return Result{}, err
		}
		conformance := config.conformance
		if conformance == nil {
			conformance = runConformance
		}
		conformanceOutput, conformanceErr := conformance(ctx, root, candidate, candidateLock)
		result.Conformance = checkFrom(conformanceOutput, conformanceErr)
		result.Green = conformanceErr == nil
	}

	switch {
	case !config.Bump:
		result.Promotion = "Not attempted (dry run)."
	case !result.Green:
		result.Promotion = "Refused because fixture extraction or conformance is red."
	case !result.Descendant:
		result.Promotion = "Refused because the target does not descend from the pinned commit."
	default:
		if err := promote(root, candidate, candidateLock, result.Green, result.Descendant); err != nil {
			return Result{}, err
		}
		result.Promotion = "Promoted generated fixtures and UPSTREAM.lock."
	}

	result.Report = renderReport(result, date)
	result.ReportPath, err = writeReport(root, config.ReportPath, result.Report, config.Stdout)
	if err != nil {
		return Result{}, err
	}
	if extractionErr != nil || result.Conformance.Status == "red" {
		if config.Bump {
			return result, errors.Join(ErrPromotionUnsafe, ErrRed)
		}
		return result, ErrRed
	}
	if config.Bump && !result.Descendant {
		return result, fmt.Errorf("%w: target does not descend from pinned commit", ErrPromotionUnsafe)
	}
	return result, nil
}

func resolveRoot(configured string) (string, error) {
	if configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		return filepath.Clean(absolute), nil
	}
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "UPSTREAM.lock")); err == nil {
			if _, err := os.Stat(filepath.Join(directory, "docs", "MIRROR.md")); err == nil {
				return directory, nil
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("cannot find pi-go root from current directory")
		}
		directory = parent
	}
}

func ensureUpstream(ctx context.Context, directory, repo string, fetch bool) error {
	if _, err := os.Stat(filepath.Join(directory, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, cloneErr := command(ctx, filepath.Dir(directory), nil, "git", "clone", repo, directory); cloneErr != nil {
			return fmt.Errorf("clone upstream: %w", cloneErr)
		}
	} else if err != nil {
		return fmt.Errorf("inspect upstream checkout: %w", err)
	}
	if fetch {
		if _, err := git(ctx, directory, "fetch", "--prune", "origin"); err != nil {
			return fmt.Errorf("fetch upstream: %w", err)
		}
	}
	return nil
}

func requireCleanUpstream(ctx context.Context, directory string) error {
	status, err := git(ctx, directory, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("upstream checkout has tracked changes; refusing to switch revisions")
	}
	return nil
}

func requireCleanPromotionPaths(ctx context.Context, root string) error {
	status, err := git(ctx, root, "status", "--porcelain", "--untracked-files=all", "--", "UPSTREAM.lock", "conformance/fixtures")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("%w: UPSTREAM.lock or conformance/fixtures has local changes", ErrPromotionUnsafe)
	}
	return nil
}

func targetMetadata(ctx context.Context, upstream, target string) (string, string, error) {
	output, err := git(ctx, upstream, "show", "-s", "--format=%cI%x00%s", target)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(strings.TrimSuffix(output, "\n"), "\x00", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("parse target metadata for %s", target)
	}
	return parts[0], parts[1], nil
}

func targetVersion(ctx context.Context, upstream, target, fallback string) string {
	data, err := git(ctx, upstream, "show", target+":packages/coding-agent/package.json")
	if err != nil {
		return fallback
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(data), &manifest) != nil || manifest.Version == "" {
		return fallback
	}
	return manifest.Version
}

func isAncestor(ctx context.Context, upstream, base, target string) (bool, error) {
	_, err := git(ctx, upstream, "merge-base", "--is-ancestor", base, target)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check target ancestry: %w", err)
}

func changedPaths(ctx context.Context, upstream, base, target string, mirror mirrorMap) ([]Change, error) {
	output, err := git(ctx, upstream, "diff", "--name-status", "-z", "--find-renames", base, target, "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(output, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	changes := make([]Change, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" || index >= len(fields) {
			return nil, fmt.Errorf("parse upstream name-status delta")
		}
		change := Change{Status: status}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("parse renamed upstream path")
			}
			change.OldPath = fields[index]
			change.Path = fields[index+1]
			index += 2
		} else {
			change.Path = fields[index]
			index++
		}
		change.Classification = classifyChange(change.Path, change.OldPath)
		change.Targets, change.WPs = mirror.lookup(change.Path, change.OldPath)
		changes = append(changes, change)
	}
	sort.Slice(changes, func(left, right int) bool { return changes[left].Path < changes[right].Path })
	return changes, nil
}

func generateFixtures(ctx context.Context, root, upstreamDir, outputDir, target string) (string, error) {
	extractor := filepath.Join(root, "conformance", "extract", "generate.ts")
	return command(ctx, upstreamDir, nil, "node", "--import", "tsx", extractor, outputDir, target)
}

func runConformance(ctx context.Context, root, fixtureDir string, lock Lock) (string, error) {
	copyRoot, cleanup, err := prepareConformanceCopy(root, fixtureDir, lock)
	if err != nil {
		return "", err
	}
	defer cleanup()
	goFlags := strings.TrimSpace(os.Getenv("GOFLAGS") + " -buildvcs=false")
	// The tool itself builds with CGO_ENABLED=0; -race needs cgo re-enabled.
	return command(ctx, copyRoot, []string{"GOWORK=off", "GOFLAGS=" + goFlags, "CGO_ENABLED=1"}, "go", "test", "-race", "./...")
}

func checkFrom(output string, err error) Check {
	status := "green"
	if err != nil {
		status = "red"
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += err.Error()
	}
	return Check{Status: status, Output: output}
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	output, err := command(ctx, directory, nil, "git", args...)
	if err != nil && strings.TrimSpace(output) != "" {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return output, err
}

func command(ctx context.Context, directory string, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(output), nil
}
