package clipboard

import (
	"bytes"
	"context"
	"crypto/rand"
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

// ExtensionForImageMimeType returns the file extension for a supported image
// MIME type, or "" for unsupported types (upstream returns null).
func ExtensionForImageMimeType(mimeType string) string {
	switch baseMimeType(mimeType) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
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

// readImageViaPowerShell reads the Windows clipboard from inside WSL, where
// the Linux clipboard does not receive Windows screenshot data.
func readImageViaPowerShell(deps imageDependencies) *Image {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return nil
	}
	tmpFile := filepath.Join(deps.tempDir(), "pi-wsl-clip-"+hex.EncodeToString(suffix)+".png")
	defer func() { _ = os.Remove(tmpFile) }()
	winPathOutput, ok := deps.runOutput("wslpath", []string{"-w", tmpFile}, listTimeout)
	if !ok {
		return nil
	}
	winPath := strings.TrimSpace(string(winPathOutput))
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

// ReadImage ports upstream readClipboardImage. The native-clipboard addon path
// (the only source on darwin and win32, and a Linux X11 fallback) has no pure-Go
// equivalent per D7, so those steps return nil; the Linux command paths are
// ported in upstream order.
func ReadImage() *Image { return readImage(defaultImageDependencies()) }

func readImage(deps imageDependencies) *Image {
	if deps.getenv("TERMUX_VERSION") != "" {
		return nil
	}
	if deps.platform != "linux" {
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
