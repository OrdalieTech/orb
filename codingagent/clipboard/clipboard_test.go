package clipboard

import (
	"errors"
	"strings"
	"testing"
)

type commandCall struct {
	name  string
	args  []string
	input string
}

func fakeDependencies(platform string, environment map[string]string) (dependencies, *[]commandCall, *strings.Builder) {
	calls := []commandCall{}
	output := &strings.Builder{}
	return dependencies{
		platform: platform,
		getenv:   func(name string) string { return environment[name] },
		run: func(name string, args []string, input string) error {
			calls = append(calls, commandCall{name: name, args: args, input: input})
			return nil
		},
		spawn: func(name string, args []string, input string) error {
			calls = append(calls, commandCall{name: name, args: args, input: input})
			return nil
		},
		lookPath: func(string) error { return nil },
		output:   output,
	}, &calls, output
}

func TestCopyToClipboardDarwinLinuxAndRemoteProfiles(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		deps, calls, output := fakeDependencies("darwin", nil)
		if err := copyToClipboard("hello", deps); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].name != "pbcopy" || (*calls)[0].input != "hello" || output.Len() != 0 {
			t.Fatalf("calls=%#v output=%q", *calls, output.String())
		}
	})
	t.Run("wayland", func(t *testing.T) {
		deps, calls, output := fakeDependencies("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_SESSION_TYPE": "wayland"})
		if err := copyToClipboard("hello", deps); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].name != "wl-copy" || output.Len() != 0 {
			t.Fatalf("calls=%#v output=%q", *calls, output.String())
		}
	})
	t.Run("termux", func(t *testing.T) {
		deps, calls, output := fakeDependencies("linux", map[string]string{"TERMUX_VERSION": "0.119"})
		if err := copyToClipboard("hello", deps); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].name != "termux-clipboard-set" || output.Len() != 0 {
			t.Fatalf("calls=%#v output=%q", *calls, output.String())
		}
	})
	t.Run("x11-xsel-fallback", func(t *testing.T) {
		deps, calls, _ := fakeDependencies("linux", map[string]string{"DISPLAY": ":0"})
		deps.run = func(name string, args []string, input string) error {
			*calls = append(*calls, commandCall{name: name, args: args, input: input})
			if name == "xclip" {
				return errors.New("missing")
			}
			return nil
		}
		if err := copyToClipboard("hello", deps); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 2 || (*calls)[0].name != "xclip" || (*calls)[1].name != "xsel" {
			t.Fatalf("calls=%#v", *calls)
		}
	})
	t.Run("remote-native-and-osc52", func(t *testing.T) {
		deps, calls, output := fakeDependencies("darwin", map[string]string{"SSH_CONNECTION": "remote"})
		if err := copyToClipboard("hello", deps); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || output.String() != "\x1b]52;c;aGVsbG8=\x07" {
			t.Fatalf("calls=%#v output=%q", *calls, output.String())
		}
	})
}

func TestCopyToClipboardOSC52FallbackAndLimit(t *testing.T) {
	deps, _, output := fakeDependencies("linux", nil)
	deps.run = func(string, []string, string) error { return errors.New("missing") }
	deps.lookPath = func(string) error { return errors.New("missing") }
	if err := copyToClipboard("fallback", deps); err != nil {
		t.Fatal(err)
	}
	if output.String() != "\x1b]52;c;ZmFsbGJhY2s=\x07" {
		t.Fatalf("osc52 = %q", output.String())
	}
	tooLarge := strings.Repeat("x", MaxOSC52EncodedLength)
	if err := copyToClipboard(tooLarge, deps); err == nil || err.Error() != "Failed to copy to clipboard" {
		t.Fatalf("oversize error = %v", err)
	}
}
