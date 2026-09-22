package subagents

import "os/exec"

// KnownCLI is an agent CLI orb can drive as an external subagent: the command
// is its print-mode invocation reading the task on stdin and answering on
// stdout (the contract runExternalChild expects).
type KnownCLI struct {
	Name    string
	Command string
}

// KnownCLIs is deliberately short: only invocations known to honor the
// stdin/stdout contract. A wrong template would fail visibly (the child's
// output is surfaced), but a curated list keeps auto-detection trustworthy.
func KnownCLIs() []KnownCLI {
	return []KnownCLI{
		{Name: "claude", Command: "claude -p --output-format text"},
		{Name: "codex", Command: "codex exec --skip-git-repo-check -"},
		{Name: "gemini", Command: "gemini"},
	}
}

// DetectCLIs returns the known CLIs present on PATH, in registry order.
// lookPath is injectable for tests.
func DetectCLIs(lookPath func(string) (string, error)) []KnownCLI {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	detected := make([]KnownCLI, 0, len(KnownCLIs()))
	for _, cli := range KnownCLIs() {
		if _, err := lookPath(cli.Name); err == nil {
			detected = append(detected, cli)
		}
	}
	return detected
}
