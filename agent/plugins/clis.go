package plugins

import "os/exec"

// knownCLI is an agent CLI orb can drive as an external subagent: the command
// is its print-mode invocation reading the task on stdin and answering on
// stdout (the contract runExternalChild expects).
type knownCLI struct {
	Name    string
	Command string
}

// knownCLIs is deliberately short: only invocations known to honor the
// stdin/stdout contract. A wrong template would fail visibly (the child's
// output is surfaced), but a curated list keeps auto-detection trustworthy.
var knownCLIs = []knownCLI{
	{Name: "claude", Command: "claude -p --output-format text"},
	{Name: "codex", Command: "codex exec --skip-git-repo-check -"},
	{Name: "gemini", Command: "gemini"},
}

// detectCLIs returns the known CLIs present on PATH, in registry order.
// lookPath is injectable for tests.
func detectCLIs(lookPath func(string) (string, error)) []knownCLI {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	detected := make([]knownCLI, 0, len(knownCLIs))
	for _, cli := range knownCLIs {
		if _, err := lookPath(cli.Name); err == nil {
			detected = append(detected, cli)
		}
	}
	return detected
}
