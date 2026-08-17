package clipboard

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/bmp"
)

type fakeCall struct {
	command string
	timeout time.Duration
}

type fakeResponse struct {
	output []byte
	ok     bool
}

func fakeImageDependencies(env map[string]string, responses map[string]fakeResponse, calls *[]fakeCall) imageDependencies {
	deps := defaultImageDependencies()
	deps.platform = "linux"
	deps.getenv = func(key string) string { return env[key] }
	deps.readFile = func(string) ([]byte, error) { return nil, nil }
	deps.runOutput = func(name string, arguments []string, timeout time.Duration) ([]byte, bool) {
		command := name + " " + strings.Join(arguments, " ")
		*calls = append(*calls, fakeCall{command: command, timeout: timeout})
		response, exists := responses[command]
		if !exists {
			return nil, false
		}
		return response.output, response.ok
	}
	return deps
}

func TestReadImageWaylandPrefersSupportedMimeOrder(t *testing.T) {
	var calls []fakeCall
	deps := fakeImageDependencies(
		map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
		map[string]fakeResponse{
			"wl-paste --list-types":                  {output: []byte("text/html\nimage/jpeg\nimage/png\n"), ok: true},
			"wl-paste --type image/png --no-newline": {output: []byte("PNGDATA"), ok: true},
		},
		&calls,
	)
	result := readImage(deps)
	if result == nil || result.MimeType != "image/png" || string(result.Bytes) != "PNGDATA" {
		t.Fatalf("result = %#v, want preferred image/png over listed-first jpeg", result)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].timeout != listTimeout || calls[1].timeout != readTimeout {
		t.Fatalf("timeouts = %#v, want upstream list/read timeouts", calls)
	}
}

func TestReadImageWaylandFallsBackToXclip(t *testing.T) {
	var calls []fakeCall
	deps := fakeImageDependencies(
		map[string]string{"XDG_SESSION_TYPE": "wayland"},
		map[string]fakeResponse{
			"xclip -selection clipboard -t TARGETS -o":    {output: []byte("image/webp\n"), ok: true},
			"xclip -selection clipboard -t image/webp -o": {output: []byte("WEBPDATA"), ok: true},
		},
		&calls,
	)
	result := readImage(deps)
	if result == nil || result.MimeType != "image/webp" || string(result.Bytes) != "WEBPDATA" {
		t.Fatalf("result = %#v", result)
	}
	if calls[0].command != "wl-paste --list-types" || calls[1].command != "xclip -selection clipboard -t TARGETS -o" {
		t.Fatalf("command order = %#v, want wl-paste before xclip", calls)
	}
}

func TestReadImageX11SkipsWlPasteAndProbesSupportedOrder(t *testing.T) {
	var calls []fakeCall
	deps := fakeImageDependencies(
		map[string]string{"DISPLAY": ":0"},
		map[string]fakeResponse{
			"xclip -selection clipboard -t image/png -o":  {output: nil, ok: true},
			"xclip -selection clipboard -t image/gif -o":  {output: []byte("GIFDATA"), ok: true},
			"xclip -selection clipboard -t image/jpeg -o": {output: nil, ok: false},
		},
		&calls,
	)
	result := readImage(deps)
	if result == nil || result.MimeType != "image/gif" {
		t.Fatalf("result = %#v", result)
	}
	wantOrder := []string{
		"xclip -selection clipboard -t TARGETS -o",
		"xclip -selection clipboard -t image/png -o",
		"xclip -selection clipboard -t image/jpeg -o",
		"xclip -selection clipboard -t image/webp -o",
		"xclip -selection clipboard -t image/gif -o",
	}
	if len(calls) != len(wantOrder) {
		t.Fatalf("calls = %#v", calls)
	}
	for index, call := range calls {
		if call.command != wantOrder[index] {
			t.Fatalf("call %d = %q, want %q", index, call.command, wantOrder[index])
		}
	}
}

func TestReadImageWSLFallsBackToPowerShell(t *testing.T) {
	var calls []fakeCall
	var readPaths []string
	pngBytes := encodeTestPNG(t)
	deps := fakeImageDependencies(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, &calls)
	tempDir := t.TempDir()
	deps.tempDir = func() string { return tempDir }
	deps.readFile = func(path string) ([]byte, error) {
		readPaths = append(readPaths, path)
		return pngBytes, nil
	}
	baseRun := deps.runOutput
	deps.runOutput = func(name string, arguments []string, timeout time.Duration) ([]byte, bool) {
		switch name {
		case "wslpath":
			baseRun(name, arguments, timeout)
			return []byte("C:\\temp\\clip.png\n"), true
		case "powershell.exe":
			baseRun(name, arguments, timeout)
			return []byte("ok\n"), true
		default:
			return baseRun(name, arguments, timeout)
		}
	}
	result := readImage(deps)
	if result == nil || result.MimeType != "image/png" || !bytes.Equal(result.Bytes, pngBytes) {
		t.Fatalf("result = %#v", result)
	}
	var wslpathCall, powershellCall *fakeCall
	for index := range calls {
		if strings.HasPrefix(calls[index].command, "wslpath -w ") {
			wslpathCall = &calls[index]
		}
		if strings.HasPrefix(calls[index].command, "powershell.exe -NoProfile -Command ") {
			powershellCall = &calls[index]
		}
	}
	if wslpathCall == nil || !strings.Contains(wslpathCall.command, "pi-wsl-clip-") || !strings.HasSuffix(wslpathCall.command, ".png") {
		t.Fatalf("wslpath call = %#v", wslpathCall)
	}
	if powershellCall == nil || !strings.Contains(powershellCall.command, "$path = 'C:\\temp\\clip.png'") ||
		!strings.Contains(powershellCall.command, "[System.Windows.Forms.Clipboard]::GetImage()") {
		t.Fatalf("powershell call = %#v", powershellCall)
	}
	if powershellCall.timeout != powerShellTimeout {
		t.Fatalf("powershell timeout = %v", powershellCall.timeout)
	}
	// The Linux clipboard is tried first even on WSL, matching upstream order.
	if !strings.HasPrefix(calls[0].command, "wl-paste ") {
		t.Fatalf("first call = %#v, want wl-paste", calls[0])
	}
	if len(readPaths) != 1 || !strings.Contains(readPaths[0], "pi-wsl-clip-") {
		t.Fatalf("read paths = %#v", readPaths)
	}
}

func TestReadImageConvertsUnsupportedFormatsToPNG(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	var bmpBuffer bytes.Buffer
	if err := bmp.Encode(&bmpBuffer, source); err != nil {
		t.Fatal(err)
	}
	var calls []fakeCall
	responses := map[string]fakeResponse{
		"xclip -selection clipboard -t TARGETS -o":   {output: []byte("image/bmp\n"), ok: true},
		"xclip -selection clipboard -t image/bmp -o": {output: bmpBuffer.Bytes(), ok: true},
	}
	deps := fakeImageDependencies(map[string]string{"DISPLAY": ":0"}, responses, &calls)
	result := readImage(deps)
	if result == nil || result.MimeType != "image/png" {
		t.Fatalf("result = %#v, want BMP converted to PNG", result)
	}
	decoded, err := png.Decode(bytes.NewReader(result.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("decoded bounds = %v", decoded.Bounds())
	}

	responses["xclip -selection clipboard -t image/bmp -o"] = fakeResponse{output: []byte("not an image"), ok: true}
	calls = nil
	if result := readImage(deps); result != nil {
		t.Fatalf("undecodable unsupported format = %#v, want nil", result)
	}
}

func TestReadImageSkipsTermuxAndNonLinux(t *testing.T) {
	var calls []fakeCall
	deps := fakeImageDependencies(map[string]string{"TERMUX_VERSION": "0.118"}, nil, &calls)
	if result := readImage(deps); result != nil || len(calls) != 0 {
		t.Fatalf("termux result = %#v, calls = %#v", result, calls)
	}
	deps = fakeImageDependencies(nil, nil, &calls)
	deps.platform = "darwin"
	if result := readImage(deps); result != nil || len(calls) != 0 {
		t.Fatalf("darwin result = %#v, calls = %#v (native clipboard is a documented gap)", result, calls)
	}
}

func TestImageMimeTypeHelpers(t *testing.T) {
	if extension := ExtensionForImageMimeType("image/jpeg;charset=binary"); extension != "jpg" {
		t.Fatalf("jpeg extension = %q", extension)
	}
	if extension := ExtensionForImageMimeType("IMAGE/PNG"); extension != "png" {
		t.Fatalf("case-insensitive extension = %q", extension)
	}
	if extension := ExtensionForImageMimeType("application/pdf"); extension != "" {
		t.Fatalf("unsupported extension = %q", extension)
	}
	if !IsWaylandSession(func(key string) string {
		if key == "XDG_SESSION_TYPE" {
			return "wayland"
		}
		return ""
	}) {
		t.Fatal("XDG_SESSION_TYPE=wayland must count as a wayland session")
	}
	if IsWaylandSession(func(string) string { return "" }) {
		t.Fatal("empty env is not a wayland session")
	}
}

func encodeTestPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
