package session

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDefaultSessionDirEncodingAndCreation(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	cwd := filepath.Join(t.TempDir(), `one:two\three`)
	wantName := "--" + strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/")) + "--"
	path, err := DefaultSessionDirPath(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(agentDir, "sessions", wantName) {
		t.Fatalf("default path = %q", path)
	}
	created, err := DefaultSessionDir(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if created != path {
		t.Fatalf("created path = %q, want %q", created, path)
	}
}

func TestUUIDv7LayoutTimestampAndMonotonicity(t *testing.T) {
	now := time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
	first, err := randomUUIDv7(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomUUIDv7(now)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatalf("invalid uuidv7 layout: %q %q", first, second)
	}
	if first >= second {
		t.Fatalf("uuidv7 values are not monotonic: %q >= %q", first, second)
	}
	wantTimestamp := regexp.MustCompile(`^03bb2cc3-d800-`)
	if !wantTimestamp.MatchString(first) {
		t.Fatalf("uuid timestamp prefix = %q", first)
	}
}

func TestAssertValidSessionID(t *testing.T) {
	for _, id := range []string{"a", "a.b-c_d", "A1"} {
		if err := AssertValidSessionID(id); err != nil {
			t.Fatalf("valid id %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", "-a", "a-", "a/b", "a b"} {
		if err := AssertValidSessionID(id); err == nil {
			t.Fatalf("invalid id %q accepted", id)
		}
	}
}
