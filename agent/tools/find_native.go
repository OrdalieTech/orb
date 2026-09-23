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
	"strings"

	"github.com/OrdalieTech/orb/engine"
)

func (tool *findTool) executeFD(
	ctx context.Context,
	pattern string,
	searchPath string,
	effectiveLimit float64,
) (engine.AgentToolResult, error) {
	fdPath := ensureManagedTool(ctx, managedFD)
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if fdPath == "" {
		return engine.AgentToolResult{}, upstreamToolError("fd is not available and could not be downloaded")
	}
	args := []string{"--glob", "--color=never", "--hidden"}
	if !insideGitRepository(searchPath) {
		args = append(args, "--no-require-git")
	}
	args = append(args, "--max-results", formatSearchNumber(effectiveLimit))
	effectivePattern := pattern
	if strings.Contains(pattern, "/") {
		args = append(args, "--full-path")
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
			effectivePattern = "**/" + pattern
		}
		// fd matches full paths using native separators on Windows.
		if runtime.GOOS == "windows" {
			effectivePattern = strings.ReplaceAll(effectivePattern, "/", `[/\\]`)
		}
	}
	args = append(args, "--", effectivePattern, searchPath)

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
	lines := make([]string, 0)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if scanErr != nil {
		return engine.AgentToolResult{}, scanErr
	}
	if waitErr != nil && len(lines) == 0 {
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
	if len(lines) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	relativized := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" {
			continue
		}
		relativized = append(relativized, relativizeFindResultPath(line, searchPath))
	}
	if len(relativized) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	return formatFindResult(relativized, effectiveLimit, true), nil
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
