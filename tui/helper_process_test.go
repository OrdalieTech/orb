package tui

import (
	"fmt"
	"os"
	"testing"
)

// fakeFDEnv turns the test binary into a stand-in fd, so fd-backed tests run
// on hosts without a POSIX shell for script fakes.
const fakeFDEnv = "ORB_TUI_FAKE_FD_OUTPUT"

func TestMain(m *testing.M) {
	if output, ok := os.LookupEnv(fakeFDEnv); ok {
		fmt.Print(output)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
