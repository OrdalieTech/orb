package clipboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
)

// Image is upstream utils/clipboard-image.ts ClipboardImage.
type Image struct {
	Bytes    []byte
	MimeType string
}

// Order matches upstream SUPPORTED_IMAGE_MIME_TYPES; it doubles as the
// xclip fallback probe order.
var supportedImageMimeTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

const (
	listTimeout       = 1 * time.Second
	readTimeout       = 3 * time.Second
	powerShellTimeout = 5 * time.Second
	maxBufferBytes    = 50 * 1024 * 1024
)

type imageDependencies struct {
	platform  string
	getenv    func(string) string
	runOutput func(name string, arguments []string, timeout time.Duration) ([]byte, bool)
	readFile  func(string) ([]byte, error)
	tempDir   func() string
}

func defaultImageDependencies() imageDependencies {
	return imageDependencies{
		platform:  runtime.GOOS,
		getenv:    os.Getenv,
		runOutput: runCommandOutput,
		readFile:  os.ReadFile,
		tempDir:   os.TempDir,
	}
}

func runCommandOutput(name string, arguments []string, timeout time.Duration) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := command.Start(); err != nil {
		return nil, false
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxBufferBytes+1))
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || len(output) > maxBufferBytes {
		return nil, false
	}
	return output, true
}

func IsWaylandSession(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	return getenv("WAYLAND_DISPLAY") != "" || getenv("XDG_SESSION_TYPE") == "wayland"
}

func baseMimeType(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// selectPreferredImageMimeType picks the first supported base type in upstream
// preference order, falling back to any image/* type.
func selectPreferredImageMimeType(mimeTypes []string) string {
	type candidate struct{ raw, base string }
	normalized := make([]candidate, 0, len(mimeTypes))
	for _, mimeType := range mimeTypes {
		trimmed := strings.TrimSpace(mimeType)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, candidate{raw: trimmed, base: baseMimeType(trimmed)})
	}
	for _, preferred := range supportedImageMimeTypes {
		for _, entry := range normalized {
			if entry.base == preferred {
				return entry.raw
			}
		}
	}
	for _, entry := range normalized {
		if strings.HasPrefix(entry.base, "image/") {
			return entry.raw
		}
	}
	return ""
}

func isSupportedImageMimeType(mimeType string) bool {
	return slices.Contains(supportedImageMimeTypes, baseMimeType(mimeType))
}

func splitLines(output []byte) []string {
	var lines []string
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func readImageViaWlPaste(deps imageDependencies) *Image {
	list, ok := deps.runOutput("wl-paste", []string{"--list-types"}, listTimeout)
	if !ok {
		return nil
	}
	selected := selectPreferredImageMimeType(splitLines(list))
	if selected == "" {
		return nil
	}
	data, ok := deps.runOutput("wl-paste", []string{"--type", selected, "--no-newline"}, readTimeout)
	if !ok || len(data) == 0 {
		return nil
	}
	return &Image{Bytes: data, MimeType: baseMimeType(selected)}
}

func readImageViaXclip(deps imageDependencies) *Image {
	targets, ok := deps.runOutput("xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, listTimeout)
	var tryTypes []string
	if ok {
		if preferred := selectPreferredImageMimeType(splitLines(targets)); preferred != "" {
			tryTypes = append(tryTypes, preferred)
		}
	}
	tryTypes = append(tryTypes, supportedImageMimeTypes...)
	for _, mimeType := range tryTypes {
		data, ok := deps.runOutput("xclip", []string{"-selection", "clipboard", "-t", mimeType, "-o"}, readTimeout)
		if ok && len(data) > 0 {
			return &Image{Bytes: data, MimeType: baseMimeType(mimeType)}
		}
	}
	return nil
}

// ponytail: AppKit through the installed osascript reads macOS image data without cgo or a bundled helper.
const macOSReadImageScript = `ObjC.import("AppKit");
var pasteboard = $.NSPasteboard.generalPasteboard;
var data = pasteboard.dataForType($.NSPasteboardTypePNG);
if (ObjC.unwrap(data) === undefined) {
  var image = $.NSImage.alloc.initWithPasteboard(pasteboard);
  if (ObjC.unwrap(image) !== undefined) {
    var bitmap = $.NSBitmapImageRep.alloc.initWithData(image.TIFFRepresentation);
    if (ObjC.unwrap(bitmap) !== undefined) data = bitmap.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $({}));
  }
}
ObjC.unwrap(data) === undefined ? "" : ObjC.unwrap(data.base64EncodedStringWithOptions(0));`

func readImageViaMacOS(deps imageDependencies) *Image {
	output, ok := deps.runOutput("osascript", []string{"-l", "JavaScript", "-e", macOSReadImageScript}, readTimeout)
	if !ok {
		return nil
	}
	output = bytes.TrimSpace(output)
	data := make([]byte, base64.StdEncoding.DecodedLen(len(output)))
	count, err := base64.StdEncoding.Decode(data, output)
	if err != nil || count == 0 {
		return nil
	}
	return &Image{Bytes: data[:count], MimeType: "image/png"}
}

func isWSL(deps imageDependencies) bool {
	if deps.getenv("WSL_DISTRO_NAME") != "" || deps.getenv("WSLENV") != "" {
		return true
	}
	release, err := deps.readFile("/proc/version")
	if err != nil {
		return false
	}
	lowered := strings.ToLower(string(release))
	return strings.Contains(lowered, "microsoft") || strings.Contains(lowered, "wsl")
}

// readImageViaPowerShell reads the Windows clipboard natively, or from inside
// WSL, where the Linux clipboard does not receive Windows screenshot data.
func readImageViaPowerShell(deps imageDependencies) *Image {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return nil
	}
	tmpFile := filepath.Join(deps.tempDir(), "pi-wsl-clip-"+hex.EncodeToString(suffix)+".png")
	defer func() { _ = os.Remove(tmpFile) }()
	winPath := tmpFile
	if deps.platform != "windows" {
		winPathOutput, ok := deps.runOutput("wslpath", []string{"-w", tmpFile}, listTimeout)
		if !ok {
			return nil
		}
		winPath = strings.TrimSpace(string(winPathOutput))
	}
	if winPath == "" {
		return nil
	}
	script := strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms",
		"Add-Type -AssemblyName System.Drawing",
		"$path = '" + strings.ReplaceAll(winPath, "'", "''") + "'",
		"$img = [System.Windows.Forms.Clipboard]::GetImage()",
		"if ($img) { $img.Save($path, [System.Drawing.Imaging.ImageFormat]::Png); Write-Output 'ok' } else { Write-Output 'empty' }",
	}, "; ")
	output, ok := deps.runOutput("powershell.exe", []string{"-NoProfile", "-Command", script}, powerShellTimeout)
	if !ok || strings.TrimSpace(string(output)) != "ok" {
		return nil
	}
	data, err := deps.readFile(tmpFile)
	if err != nil || len(data) == 0 {
		return nil
	}
	return &Image{Bytes: data, MimeType: "image/png"}
}

// convertToPNG replaces upstream's Photon WASM conversion with stdlib+x/image
// decoding (BMP covers the documented WSLg case).
func convertToPNG(data []byte) []byte {
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, decoded); err != nil {
		return nil
	}
	return buffer.Bytes()
}

// ReadImage reads clipboard images without a native addon. Linux command paths
// retain upstream order; macOS uses AppKit through its installed script runner.
func ReadImage() *Image { return readImage(defaultImageDependencies()) }

func readImage(deps imageDependencies) *Image {
	if deps.getenv("TERMUX_VERSION") != "" {
		return nil
	}
	switch deps.platform {
	case "darwin":
		return readImageViaMacOS(deps)
	case "windows":
		return readImageViaPowerShell(deps)
	case "linux":
	default:
		return nil
	}
	wsl := isWSL(deps)
	wayland := IsWaylandSession(deps.getenv)
	var result *Image
	if wayland || wsl {
		result = readImageViaWlPaste(deps)
		if result == nil {
			result = readImageViaXclip(deps)
		}
	}
	if result == nil && wsl {
		result = readImageViaPowerShell(deps)
	}
	if result == nil && !wayland {
		result = readImageViaXclip(deps)
	}
	if result == nil {
		return nil
	}
	if !isSupportedImageMimeType(result.MimeType) {
		converted := convertToPNG(result.Bytes)
		if converted == nil {
			return nil
		}
		return &Image{Bytes: converted, MimeType: "image/png"}
	}
	return result
}

// The script prints base64 so the console code page cannot alter the text.
const powerShellReadTextScript = "[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string](Get-Clipboard -Raw)))"

// ReadText reads clipboard text with the platform's command-line tools; the
// first tool that runs decides, as upstream readClipboardText.
func ReadText() string { return readText(defaultImageDependencies()) }

func readText(deps imageDependencies) string {
	type command struct {
		name      string
		arguments []string
	}
	var commands []command
	if deps.getenv("TERMUX_VERSION") != "" {
		commands = append(commands, command{"termux-clipboard-get", nil})
	}
	powerShell := command{"powershell.exe", []string{"-NoProfile", "-Command", powerShellReadTextScript}}
	switch deps.platform {
	case "darwin":
		commands = append(commands, command{"pbpaste", nil})
	case "windows":
		commands = append(commands, powerShell)
	case "linux":
		if deps.getenv("WAYLAND_DISPLAY") != "" {
			commands = append(commands, command{"wl-paste", []string{"--no-newline", "--type", "text"}})
		}
		if deps.getenv("DISPLAY") != "" {
			commands = append(commands, command{"xclip", []string{"-selection", "clipboard", "-out"}}, command{"xsel", []string{"--clipboard", "--output"}})
		}
		if isWSL(deps) {
			commands = append(commands, powerShell)
		}
	}
	for _, command := range commands {
		output, ok := deps.runOutput(command.name, command.arguments, readTimeout)
		if !ok {
			continue
		}
		if command.name == powerShell.name {
			output, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(string(output)))
		}
		return string(output)
	}
	return ""
}
