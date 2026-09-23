// Command fakeclip stands in for the Windows clip.exe in tests: it copies
// stdin byte for byte to the file named by ORB_TEST_CLIPBOARD.
package main

import (
	"io"
	"os"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err == nil {
		err = os.WriteFile(os.Getenv("ORB_TEST_CLIPBOARD"), data, 0o600)
	}
	if err != nil {
		os.Exit(1)
	}
}
