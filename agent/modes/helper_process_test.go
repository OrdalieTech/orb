package modes

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modesTestHelperEnv turns the test binary into a fake child process (fd or an
// external editor) so those fixtures run without a POSIX shell.
const modesTestHelperEnv = "ORB_MODES_TEST_HELPER"

const modesTestStdoutEnv = "ORB_MODES_TEST_STDOUT"

func TestMain(m *testing.M) {
	if helper := os.Getenv(modesTestHelperEnv); helper != "" {
		os.Exit(runModesTestHelper(helper, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runModesTestHelper(helper string, args []string) int {
	switch helper {
	case "stdout":
		_, _ = io.WriteString(os.Stdout, os.Getenv(modesTestStdoutEnv))
		return 0
	case "editor":
		return fakeExternalEditorProcess(args)
	}
	_, _ = fmt.Fprintf(os.Stderr, "unknown test helper %q\n", helper)
	return 2
}

// fakeExternalEditorProcess mirrors upstream test/fixtures/fake-external-editor.mjs:
// arguments are the capture path, optional flags, then the prompt file.
func fakeExternalEditorProcess(args []string) int {
	capture, file := args[0], args[len(args)-1]
	content, _ := os.ReadFile(file)
	entries, _ := os.ReadDir(filepath.Dir(file))
	var listing strings.Builder
	for _, entry := range entries {
		listing.WriteString(entry.Name() + " ")
	}
	record := "file:" + file + "\ncontent:" + strings.TrimRight(string(content), "\n") + "\nentries:" + listing.String() + "\n"
	if err := os.WriteFile(capture, []byte(record), 0o600); err != nil {
		return 3
	}
	flags := strings.Join(args[1:len(args)-1], " ")
	switch {
	case strings.Contains(flags, "--fail"):
		return 1
	case strings.Contains(flags, "--empty"):
		_ = os.WriteFile(file, nil, 0o600)
	default:
		_ = os.WriteFile(file, []byte("edited\n"), 0o600)
	}
	return 0
}
