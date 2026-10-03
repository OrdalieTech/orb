package herdr

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"

	"github.com/OrdalieTech/orb/agent/extensions"
)

const piLifecycleEvent = "orb:herdr:pi-lifecycle"

// IsPiIntegration recognizes Herdr's managed integration, not an arbitrary file with the same name.
func IsPiIntegration(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	header, err := io.ReadAll(io.LimitReader(file, 1024))
	header = bytes.ReplaceAll(header, []byte("\r\n"), []byte("\n"))
	return err == nil && bytes.Contains(header, []byte("// installed by herdr\n")) && bytes.Contains(header, []byte("// HERDR_INTEGRATION_ID=pi\n"))
}

// InPane reports whether this process runs inside a Herdr pane it can report to.
func InPane() bool {
	return os.Getenv("HERDR_ENV") == "1" && os.Getenv("HERDR_PANE_ID") != "" && os.Getenv("HERDR_SOCKET_PATH") != ""
}

// WrapPiIntegration hands lifecycle ownership to Herdr's managed pi integration once it registers.
func WrapPiIntegration(path string, factory extensions.Factory) extensions.Factory {
	if IsPiIntegration(path) {
		return WithPiLifecycle(factory)
	}
	return factory
}

// PiForegroundHint lets Herdr recognize the extension host running its pi integration as pi.
func PiForegroundHint(paths []string) []string {
	if slices.ContainsFunc(paths, IsPiIntegration) {
		return []string{"HERDR_AGENT=pi"}
	}
	return nil
}

// WithPiLifecycle claims ownership only after successful registration, including registry recreation.
func WithPiLifecycle(factory extensions.Factory) extensions.Factory {
	return func(api extensions.API) error {
		if err := factory(api); err != nil {
			return err
		}
		api.Events().Emit(context.Background(), piLifecycleEvent, nil)
		return nil
	}
}
