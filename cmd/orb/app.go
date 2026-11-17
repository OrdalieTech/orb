package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/mitosis/view"
	nativemitosis "github.com/OrdalieTech/orb/platforms/native/mitosis"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
)

// runApp serves the view every Orb app draws (mitosis/view) as JSON lines: intents on stdin,
// state, rows and replies on stdout. It runs over this machine's Mitosis, the CLI's own.
func runApp(ctx context.Context, args []string, streams cliStreams) int {
	flags := flag.NewFlagSet("orb app", flag.ContinueOnError)
	flags.SetOutput(streams.Stderr)
	profile := flags.String("profile", "personal", "Mitosis profile")
	name := flags.String("name", "this device", "what the app calls this machine")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if err := startMitosis(ctx, *profile, true); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	dir, err := nativemitosis.Dir(*profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	call, done := mitosisOwner(ctx, *profile)
	defer done()
	cwd, _ := os.Getwd()
	tabs, names := filepath.Join(dir, "app-tabs.json"), filepath.Join(dir, "app-names.json")
	saved, _ := os.ReadFile(tabs)
	named, _ := os.ReadFile(names)
	out := json.NewEncoder(streams.Stdout)
	app := view.New(ctx, view.Options{
		Call: call,
		Run: func(ctx context.Context, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				err = errors.New(string(out))
			}
			return string(out), err
		},
		ReadFile:  os.ReadFile,
		Tabs:      saved,
		SaveTabs:  func(b []byte) { _ = os.WriteFile(tabs, b, 0o600) },
		Names:     named,
		SaveNames: func(b []byte) { _ = os.WriteFile(names, b, 0o600) },
		Latest: func(ctx context.Context) string {
			tag, _ := selfupdate.LatestTag(ctx, version, http.DefaultClient, latestReleaseURL, 20*time.Second)
			return selfupdate.Plain(tag)
		},
		Name: *name,
		CWD:  cwd,
		Emit: func(m any) { _ = out.Encode(m) },
	})
	lines := bufio.NewScanner(streams.Stdin)
	lines.Buffer(make([]byte, 64<<10), protocol.MaxFrame+1024)
	for lines.Scan() {
		app.Do(lines.Bytes())
	}
	return 0
}
