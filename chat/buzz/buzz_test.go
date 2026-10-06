package buzz

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/toolenv"
)

// The agent's shell posts on Buzz the way Buzz's harness prompt says, `buzz
// messages send`, with a key it never sees, and the agent signs its own
// profile, owner's tag included.
func TestShellPostsWithTheAgentsKeyItNeverSees(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake buzz CLI is a shell script")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"OPENROUTER_API_KEY": "sk-or-secret", "BUZZ_PRIVATE_KEY": "nostr-secret", "BUZZ_AUTH_TAG": "auth-tag-secret",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv("BUZZ_ACP_DISPLAY_NAME", "Sales")
	t.Setenv("ORB_BUZZ_ABOUT", "Answers the sales team")
	// A team agent's tools inherit only this allowlist.
	t.Setenv(toolenv.Allow, "PATH,HOME")
	t.Setenv("ORB_BUZZ", "")
	// The shell's buzz is Orb answering as buzz; the real CLI records its runs.
	link := "#!/bin/sh\nORB_BUZZ_SHIM_HELPER=1 exec '" + os.Args[0] + "' -test.run='^TestShimHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "buzz"), []byte(link), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runs, cli := filepath.Join(root, "runs"), filepath.Join(root, "buzz-cli")
	real := "#!/bin/sh\n{ echo \"args: $*\"; echo \"input: $(cat)\"; env; } > '" + runs + "'.$$\necho '{\"ok\":true}'\n"
	if err := os.WriteFile(cli, []byte(real), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_BUZZ_CLI", cli)
	stop, err := serveCLI(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	shell := exec.Command("sh", "-c", "env; printf 'Hello team' | buzz messages send --channel c1 --content -")
	shell.Dir, shell.Env = root, toolenv.Environ()
	output, err := shell.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `{"ok":true}`) {
		t.Fatalf("shell (%v): %s", err, output)
	}
	// The profile is published beside the shell's run, so wait for its run.
	var record string
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(record, "set-profile") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		files, _ := filepath.Glob(runs + ".*")
		record = ""
		for _, file := range files {
			run, _ := os.ReadFile(file)
			record += string(run)
		}
	}
	for name, value := range secrets {
		if strings.Contains(string(output), value) {
			t.Errorf("the shell saw %s", name)
		}
		if leaked := strings.Contains(record, value); leaked != strings.HasPrefix(name, "BUZZ_") {
			t.Errorf("the buzz CLI holds %s: %t", name, leaked)
		}
	}
	for _, run := range []string{"args: messages send --channel c1 --content -\ninput: Hello team", "args: users set-profile --name Sales --about Answers the sales team\n"} {
		if !strings.Contains(record, run) {
			t.Fatalf("the buzz CLI ran with %s", record)
		}
	}
}

func TestShimHelper(t *testing.T) {
	if os.Getenv("ORB_BUZZ_SHIM_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	os.Exit(shim(args, os.Stdin, os.Stdout, os.Stderr))
}
