package platforms

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/chat"
)

// Started by the agent, buzz-acp gets its own settings and none of the
// agent's model or chat credentials, reaches the agent by the command the
// agent gave it, and ends the agent when it exits.
func TestBuzzStartsBuzzACPWithoutTheAgentsCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake buzz-acp is a shell script")
	}
	root := t.TempDir()
	harness, seen, reply := filepath.Join(root, "buzz-acp"), filepath.Join(root, "env"), filepath.Join(root, "reply")
	script := "#!/bin/sh\nenv > '" + seen + "'\nset -f; IFS=,; set -- $BUZZ_ACP_AGENT_ARGS\necho ping | \"$BUZZ_ACP_AGENT_COMMAND\" \"$@\" > '" + reply + "'\n"
	if err := os.WriteFile(harness, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Env{"PATH=" + os.Getenv("PATH"), "OPENROUTER_API_KEY=sk-or-secret", "TELEGRAM_BOT_TOKEN=tg-secret", "BUZZ_PRIVATE_KEY=nostr-secret", "ORB_BUZZ_ACP=" + harness}
	agent := chat.Agent{
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
		// The agent's tools see none of its credentials.
		Environ: func() []string { return []string{"PATH=" + os.Getenv("PATH")} },
		Export:  func(string, string) {}, Log: io.Discard,
	}
	buzz, _ := Lookup("buzz")
	if err := buzz.Front(context.Background(), agent, env); err != nil {
		t.Fatalf("front: %v", err)
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
	for _, want := range []string{"BUZZ_PRIVATE_KEY=nostr-secret", "BUZZ_ACP_NO_MEMORY=true"} {
		if !strings.Contains(string(environ), want) {
			t.Errorf("buzz-acp lacks %s: %s", want, environ)
		}
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
