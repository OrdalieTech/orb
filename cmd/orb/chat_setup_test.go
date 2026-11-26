package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

// orb chat setup saves what the platform returns where orb chat reads it,
// private to the user, and a second run keeps the identity it made.
func TestChatSetupSavesWhatOrbChatReads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_CHAT_DATA_DIR", "")
	t.Setenv("BUZZ_PRIVATE_KEY", "")
	t.Setenv("BUZZ_AUTH_TAG", "")
	t.Setenv("BUZZ_RELAY_URL", "")
	run := func(args ...string) string {
		var stdout bytes.Buffer
		if code := runNativeCLI(context.Background(), append([]string{"chat", "setup"}, args...), cliStreams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard}); code != 0 {
			t.Fatalf("orb chat setup %v: exit %d\n%s", args, code, stdout.String())
		}
		return stdout.String()
	}
	run("buzz", "--relay", "wss://relay.example", "--name", "Sales")
	path := filepath.Join(root, "agent", "chat", "buzz", "buzz.env")
	info, err := os.Stat(path)
	// Windows has no POSIX modes to check.
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file: %v, %v", info, err)
	}
	first := readSettings(path)
	run("buzz", "--name", "Sales team")
	second := readSettings(path)
	key := func(entries []string) string {
		for _, entry := range entries {
			if value, ok := strings.CutPrefix(entry, "BUZZ_PRIVATE_KEY="); ok {
				return value
			}
		}
		return ""
	}
	if key(first) == "" || key(first) != key(second) || !strings.Contains(strings.Join(second, "\n"), "BUZZ_NAME=Sales team") || !strings.Contains(strings.Join(second, "\n"), "BUZZ_RELAY_URL=wss://relay.example") {
		t.Fatalf("settings\n%v\nthen\n%v", first, second)
	}
}
