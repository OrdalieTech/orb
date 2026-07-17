package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const miniTreeHelperEnv = "PI_GO_SEARCH_MINITREE_HELPER"

func TestSearchToolsActuallySearchCommittedMiniTree(t *testing.T) {
	requireUnixSearchTest(t)
	root, err := filepath.Abs(filepath.Join("testdata", "search", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	installMiniTreeSearchHelpers(t)

	grepResult, err := NewGrepTool(root, nil).Execute(context.Background(), "grep", map[string]any{
		"pattern": "kept",
		"path":    root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	grepWant := strings.Join([]string{
		"a/deep/kept.txt:1: deep kept",
		"a/kept.txt:1: a kept",
		"b/kept.txt:1: b kept",
		"kept.txt:1: root kept",
	}, "\n")
	if got := toolResultText(t, grepResult); got != grepWant {
		t.Fatalf("grep output = %q, want %q", got, grepWant)
	}

	findResult, err := NewFindTool(root, nil).Execute(context.Background(), "find", map[string]any{
		"pattern": "*.txt",
		"path":    root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	findWant := strings.Join([]string{
		".secret/hidden.txt",
		"a/deep/kept.txt",
		"a/kept.txt",
		"b/ignored.txt",
		"b/kept.txt",
		"context.txt",
		"kept.txt",
		"root.txt",
		"visible.txt",
	}, "\n")
	if got := toolResultText(t, findResult); got != findWant {
		t.Fatalf("find output = %q, want %q", got, findWant)
	}
}

func TestSearchMiniTreeHelperProcess(t *testing.T) {
	mode := os.Getenv(miniTreeHelperEnv)
	if mode == "" {
		return
	}
	args := miniTreeHelperArgs(os.Args)
	exitCode := 2
	switch mode {
	case "rg":
		exitCode = runMiniTreeRG(args)
	case "fd":
		exitCode = runMiniTreeFD(args)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown mini-tree helper mode %q\n", mode)
	}
	os.Exit(exitCode)
}

func installMiniTreeSearchHelpers(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rg", "fd"} {
		script := fmt.Sprintf(
			"#!/bin/sh\nexport %s=%s\nexec %s -test.run='^TestSearchMiniTreeHelperProcess$' -- \"$@\"\n",
			miniTreeHelperEnv,
			miniTreeShellQuote(name),
			miniTreeShellQuote(executable),
		)
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PI_OFFLINE", "1")
}

func miniTreeHelperArgs(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

func runMiniTreeRG(args []string) int {
	if len(args) < 3 || args[len(args)-3] != "--" {
		_, _ = fmt.Fprintln(os.Stderr, "invalid rg arguments")
		return 2
	}
	pattern, searchRoot := args[len(args)-2], args[len(args)-1]
	files, err := miniTreeFiles(searchRoot)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 2
	}
	encoder := json.NewEncoder(os.Stdout)
	matches := 0
	for _, file := range files {
		opened, openErr := os.Open(file)
		if openErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, openErr)
			return 2
		}
		scanner := bufio.NewScanner(opened)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if !strings.Contains(line, pattern) {
				continue
			}
			event := miniTreeRGEvent{Type: "match"}
			event.Data.Path.Text = file
			event.Data.Lines.Text = line + "\n"
			event.Data.LineNumber = lineNumber
			if encodeErr := encoder.Encode(event); encodeErr != nil {
				_ = opened.Close()
				return 2
			}
			matches++
		}
		scanErr := scanner.Err()
		closeErr := opened.Close()
		if scanErr != nil || closeErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, firstMiniTreeError(scanErr, closeErr))
			return 2
		}
	}
	if matches == 0 {
		return 1
	}
	return 0
}

type miniTreeRGEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func runMiniTreeFD(args []string) int {
	if len(args) < 3 || args[len(args)-3] != "--" {
		_, _ = fmt.Fprintln(os.Stderr, "invalid fd arguments")
		return 2
	}
	pattern, searchRoot := args[len(args)-2], args[len(args)-1]
	files, err := miniTreeFiles(searchRoot)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, file := range files {
		matched, matchErr := path.Match(pattern, filepath.Base(file))
		if matchErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, matchErr)
			return 2
		}
		if matched {
			_, _ = fmt.Fprintln(os.Stdout, file)
		}
	}
	return 0
}

func miniTreeFiles(root string) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "gitignore.rules" && !miniTreeIgnored(root, file) {
			files = append(files, file)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func miniTreeIgnored(root, file string) bool {
	for current := filepath.Dir(file); ; current = filepath.Dir(current) {
		rules, err := os.ReadFile(filepath.Join(current, "gitignore.rules"))
		if err == nil {
			relative, relErr := filepath.Rel(current, file)
			if relErr == nil && miniTreeRulesMatch(string(rules), filepath.ToSlash(relative)) {
				return true
			}
		}
		if current == root {
			return false
		}
	}
}

func miniTreeRulesMatch(rules, relative string) bool {
	for _, rawRule := range strings.Split(rules, "\n") {
		rule := strings.TrimSpace(rawRule)
		if rule == "" || strings.HasPrefix(rule, "#") {
			continue
		}
		if strings.HasPrefix(rule, "/") && relative == strings.TrimPrefix(rule, "/") {
			return true
		}
		if !strings.HasPrefix(rule, "/") {
			matched, _ := path.Match(rule, path.Base(relative))
			if matched {
				return true
			}
		}
	}
	return false
}

func firstMiniTreeError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

func miniTreeShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
