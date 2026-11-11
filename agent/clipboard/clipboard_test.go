package clipboard

import (
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"
)

func fakeDependencies(platform string, env map[string]string, outputs map[string]string) (imageDependencies, *[]string) {
	var ran []string
	return imageDependencies{
		platform: platform,
		getenv:   func(name string) string { return env[name] },
		runOutput: func(name string, _ []string, _ time.Duration) ([]byte, bool) {
			ran = append(ran, name)
			output, ok := outputs[name]
			return []byte(output), ok
		},
		readFile: func(string) ([]byte, error) { return []byte("png-bytes"), nil },
		tempDir:  func() string { return `C:\Temp` },
	}, &ran
}

func TestReadTextOnWindowsAndWSL(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("héllo\r\nworld"))
	for name, test := range map[string]struct {
		platform string
		env      map[string]string
		outputs  map[string]string
		want     string
		ran      []string
	}{
		"windows":         {"windows", nil, map[string]string{"powershell.exe": encoded + "\r\n"}, "héllo\r\nworld", []string{"powershell.exe"}},
		"wsl without X":   {"linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, map[string]string{"powershell.exe": encoded}, "héllo\r\nworld", []string{"powershell.exe"}},
		"wslg wayland":    {"linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WAYLAND_DISPLAY": "wayland-0"}, map[string]string{"wl-paste": "linux text"}, "linux text", []string{"wl-paste"}},
		"x11 xsel":        {"linux", map[string]string{"DISPLAY": ":0"}, map[string]string{"xsel": "x text"}, "x text", []string{"xclip", "xsel"}},
		"nothing to read": {"linux", nil, nil, "", nil},
	} {
		deps, ran := fakeDependencies(test.platform, test.env, test.outputs)
		deps.readFile = func(string) ([]byte, error) { return nil, errors.New("no /proc") }
		if got := readText(deps); got != test.want || !slices.Equal(*ran, test.ran) {
			t.Errorf("%s: read %q via %v, want %q via %v", name, got, *ran, test.want, test.ran)
		}
	}
}

func TestReadImageOnWindowsUsesPowerShell(t *testing.T) {
	deps, ran := fakeDependencies("windows", nil, map[string]string{"powershell.exe": "ok\r\n"})
	image := readImage(deps)
	if image == nil || string(image.Bytes) != "png-bytes" || image.MimeType != "image/png" || !slices.Equal(*ran, []string{"powershell.exe"}) {
		t.Fatalf("windows image = %+v via %v, want the PowerShell PNG without wslpath", image, *ran)
	}
}
