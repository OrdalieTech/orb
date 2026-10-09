package herdr

import (
	"bytes"
	"io"
	"os"
)

// IsPiIntegration recognizes Herdr's managed Pi integration so Orb can omit it
// without modifying Pi's installation or excluding unrelated extensions.
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
