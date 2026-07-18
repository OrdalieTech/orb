package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const MaxOSC52EncodedLength = 100_000

type dependencies struct {
	platform string
	getenv   func(string) string
	run      func(string, []string, string) error
	spawn    func(string, []string, string) error
	lookPath func(string) error
	output   io.Writer
}

func defaultDependencies() dependencies {
	return dependencies{
		platform: runtime.GOOS,
		getenv:   os.Getenv,
		run:      runClipboardCommand,
		spawn:    spawnClipboardCommand,
		lookPath: func(name string) error { _, err := exec.LookPath(name); return err },
		output:   os.Stdout,
	}
}

func runClipboardCommand(name string, arguments []string, input string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = strings.NewReader(input)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return command.Run()
}

func spawnClipboardCommand(name string, arguments []string, input string) error {
	command := exec.Command(name, arguments...)
	command.Stdin = strings.NewReader(input)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func isRemoteSession(getenv func(string) string) bool {
	return getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" || getenv("MOSH_CONNECTION") != ""
}

func EmitOSC52(text string, output io.Writer) bool {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if len(encoded) > MaxOSC52EncodedLength {
		return false
	}
	if output == nil {
		return false
	}
	_, err := io.WriteString(output, "\x1b]52;c;"+encoded+"\x07")
	return err == nil
}

func CopyToClipboard(text string) error { return copyToClipboard(text, defaultDependencies()) }

func copyToClipboard(text string, deps dependencies) error {
	copied := false
	remote := isRemoteSession(deps.getenv)
	switch deps.platform {
	case "darwin":
		copied = deps.run("pbcopy", nil, text) == nil
	case "windows":
		copied = deps.run("clip", nil, text) == nil
	default:
		if deps.getenv("TERMUX_VERSION") != "" {
			copied = deps.run("termux-clipboard-set", nil, text) == nil
		}
		waylandDisplay := deps.getenv("WAYLAND_DISPLAY") != ""
		x11Display := deps.getenv("DISPLAY") != ""
		wayland := waylandDisplay || deps.getenv("XDG_SESSION_TYPE") == "wayland"
		if !copied && wayland && waylandDisplay && deps.lookPath("wl-copy") == nil {
			copied = deps.spawn("wl-copy", nil, text) == nil
		}
		if !copied && x11Display {
			copied = copyX11(text, deps)
		}
	}
	if copied && !remote {
		return nil
	}
	if remote || !copied {
		copied = EmitOSC52(text, deps.output) || copied
	}
	if !copied {
		return errors.New("Failed to copy to clipboard") //nolint:staticcheck // Upstream public error text is capitalized.
	}
	return nil
}

func copyX11(text string, deps dependencies) bool {
	if deps.run("xclip", []string{"-selection", "clipboard"}, text) == nil {
		return true
	}
	return deps.run("xsel", []string{"--clipboard", "--input"}, text) == nil
}
