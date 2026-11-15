//go:build !wasm

package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/truncate"
)

func (tool *findTool) executeFD(
	ctx context.Context,
	pattern string,
	searchPath string,
	effectiveLimit int,
) (engine.AgentToolResult, error) {
	fdPath := ensureManagedTool(ctx, managedFD)
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if fdPath == "" {
		return engine.AgentToolResult{}, upstreamToolError("fd is not available and could not be downloaded")
	}
	args := []string{"--color=never", "--hidden"}
	if !insideGitRepository(searchPath) {
		args = append(args, "--no-require-git")
	}
	// On Windows `\` is a path separator (#9262); on POSIX it stays a glob escape.
	if runtime.GOOS == "windows" {
		pattern = strings.ReplaceAll(pattern, `\`, "/")
	}
	// fd --glob matches the basename unless --full-path is set, which matches the absolute path,
	// so a path pattern like 'src/**/*.spec.ts' needs a leading '**/'.
	isPathPattern := strings.Contains(pattern, "/")
	if isPathPattern && !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
		pattern = "**/" + pattern
	}
	var matches func(string) bool
	if runtime.GOOS == "windows" && isPathPattern {
		// fd before 10.5.1 never matches --full-path globs on Windows, and workarounds for that break
		// on 10.5.1 (sharkdp/fd#2067): fd lists every entry and path patterns are matched here.
		matches = findPathMatcher(pattern)
		args = append(args, "--", "", searchPath)
	} else {
		// One result past the limit tells "exactly N matches" apart from "more than N" (#7121).
		args = append(args, "--glob", "--max-results", strconv.Itoa(effectiveLimit+1))
		if isPathPattern {
			args = append(args, "--full-path")
		}
		args = append(args, "--", pattern, searchPath)
	}
	// A cancel during the .git lookup must not start fd.
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}

	command := exec.CommandContext(ctx, fdPath, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return engine.AgentToolResult{}, upstreamToolErrorf("Failed to run fd: %s", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return engine.AgentToolResult{}, upstreamToolErrorf("Failed to run fd: %s", err)
	}
	// Results are stored only up to the first one past the byte cap; truncateHead drops the rest,
	// which are counted for the totals.
	var stored []string
	count, outputBytes, limitReached := 0, 0, false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if line == "" || matches != nil && !matches(filepath.ToSlash(line)) {
			continue
		}
		if limitReached = count == effectiveLimit; limitReached {
			// Without --max-results, fd keeps listing past the limit.
			_ = command.Process.Kill()
			break
		}
		result := relativizeFindResultPath(line, searchPath)
		if outputBytes-1 <= truncate.DefaultMaxBytes {
			stored = append(stored, result)
		}
		count++
		outputBytes += len(result) + 1
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if scanErr != nil {
		return engine.AgentToolResult{}, scanErr
	}
	if waitErr != nil && count == 0 {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			var exitError *exec.ExitError
			if errors.As(waitErr, &exitError) {
				message = fmt.Sprintf("fd exited with code %d", exitError.ExitCode())
			} else {
				message = waitErr.Error()
			}
		}
		return engine.AgentToolResult{}, upstreamToolError(message)
	}
	if count == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	truncation := truncate.TruncateHead(strings.Join(stored, "\n"), truncate.Options{MaxLines: truncate.Int(truncate.MaxSafeInteger)})
	truncation.TotalLines, truncation.TotalBytes = count, outputBytes-1
	return formatFindResult(truncation, effectiveLimit, limitReached, true), nil
}

func insideGitRepository(searchPath string) bool {
	for current := searchPath; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}
