package codingagent

import (
	"os/exec"
	"runtime"
)

// openBrowserCommand returns the platform launcher invocation for target,
// mirroring upstream utils/open-browser.ts. This intentionally never invokes a
// shell: on Windows `cmd /c start` would re-parse metacharacters (&, |, ^,
// ...) before `start` runs, making attacker-controlled URLs injectable.
func openBrowserCommand(goos, target string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return "xdg-open", []string{target}
	}
}

// OpenBrowser opens a URL or file in the platform browser/default handler
// (upstream utils/open-browser.ts openBrowser). The launch is non-blocking and
// best-effort: callers still present the target to the user, so launcher
// failures (for example a missing xdg-open) are swallowed rather than
// surfaced.
func OpenBrowser(target string) {
	name, args := openBrowserCommand(runtime.GOOS, target)
	launchDetached(name, args)
}

func launchDetached(name string, args []string) {
	command := exec.Command(name, args...)
	configureDetachedProcess(command)
	if err := command.Start(); err != nil {
		return
	}
	// Reap the launcher without blocking the caller (upstream unref()).
	go func() { _ = command.Wait() }()
}
