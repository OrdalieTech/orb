package buzz

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

// The agent's shell posts on Buzz the way Buzz's harness prompt says, `buzz
// messages send`, with a key it never sees, and the agent signs its own
// profile, owner's tag included. The shell finds the shim whether the tools'
// allowlist is set before Buzz starts or after it.
func TestShellPostsWithTheAgentsKeyItNeverSees(t *testing.T) {
	for _, allowlistFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowlist first", false: "allowlist after"}[allowlistFirst], func(t *testing.T) {
			shellPosts(t, allowlistFirst)
		})
	}
}

func shellPosts(t *testing.T, allowlistFirst bool) {
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
	t.Setenv("ORB_BUZZ", "")
	t.Setenv(toolenv.Allow, "")
	// A team agent's tools inherit only this allowlist.
	if allowlistFirst {
		t.Setenv(toolenv.Allow, "PATH,HOME")
	}
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
	if !allowlistFirst {
		t.Setenv(toolenv.Allow, "PATH,HOME")
	}

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

// echoAgent answers each connection's first line with "pong:" and that line,
// and relays through this test binary (TestConnectHelper).
func echoAgent(log io.Writer) chat.Agent {
	return chat.Agent{
		Serve: func(_ context.Context, in io.Reader, out io.Writer) error {
			line, err := bufio.NewReader(in).ReadString('\n')
			if err == nil {
				_, err = io.WriteString(out, "pong:"+line)
			}
			return err
		},
		Connect: func(socket string) ([]string, error) {
			return []string{os.Args[0], "-test.run=^TestConnectHelper$", "--", "connect", socket}, nil
		},
		Log: log,
	}
}

// Started by the agent, buzz-acp gets its own settings and none of the
// agent's model or chat credentials, reaches the agent by the command the
// agent gave it, and ends the agent when it exits.
func TestFrontRunsBuzzACPWithoutTheAgentsCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake buzz-acp is a shell script")
	}
	root := t.TempDir()
	for name, value := range map[string]string{"OPENROUTER_API_KEY": "sk-or-secret", "TELEGRAM_BOT_TOKEN": "tg-secret", "BUZZ_PRIVATE_KEY": "nostr-secret", "BUZZ_ACP_DISPLAY_NAME": ""} {
		t.Setenv(name, value)
	}
	t.Setenv(toolenv.Allow, "PATH,HOME")
	t.Setenv("ORB_ACP_SOCKET", "")
	_ = os.Unsetenv("ORB_ACP_SOCKET")
	harness, seen, reply := filepath.Join(root, "buzz-acp"), filepath.Join(root, "env"), filepath.Join(root, "reply")
	script := "#!/bin/sh\nenv > '" + seen + "'\nset -f; IFS=,; set -- $BUZZ_ACP_AGENT_ARGS\necho ping | \"$BUZZ_ACP_AGENT_COMMAND\" \"$@\" > '" + reply + "'\n"
	if err := os.WriteFile(harness, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_BUZZ_ACP", harness)
	var log bytes.Buffer
	if err := front(context.Background(), echoAgent(&log)); err != nil {
		t.Fatalf("front: %v, log: %s", err, log.String())
	}
	if answer, _ := os.ReadFile(reply); string(answer) != "pong:ping\n" {
		t.Fatalf("buzz-acp got %q from the agent", answer)
	}
	environ, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"sk-or-secret", "tg-secret"} {
		if strings.Contains(string(environ), leaked) {
			t.Errorf("buzz-acp holds %s", leaked)
		}
	}
	if !strings.Contains(string(environ), "BUZZ_PRIVATE_KEY=nostr-secret") {
		t.Errorf("buzz-acp lacks its key: %s", environ)
	}
}

// With ORB_ACP_SOCKET, buzz-acp runs as another user: the agent serves that
// socket, open to its group, until it stops.
func TestFrontServesAnExternalBuzzACP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("group permissions on a socket are Unix")
	}
	t.Setenv("BUZZ_ACP_DISPLAY_NAME", "")
	dir, err := os.MkdirTemp("", "acp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "acp.sock")
	t.Setenv("ORB_ACP_SOCKET", socket)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- front(ctx, echoAgent(io.Discard)) }()
	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); conn == nil && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		conn, _ = net.Dial("unix", socket)
	}
	if conn == nil {
		t.Fatal("the agent never served its socket")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v (%v)", info.Mode().Perm(), err)
	}
	_, _ = io.WriteString(conn, "ping\n")
	if answer, _ := bufio.NewReader(conn).ReadString('\n'); answer != "pong:ping\n" {
		t.Fatalf("answer = %q", answer)
	}
	_ = conn.Close()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the front outlived its context")
	}
}

func TestConnectHelper(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) < at+3 || os.Args[at+1] != "connect" {
		return
	}
	conn, err := net.Dial("unix", os.Args[at+2])
	if err != nil {
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}
