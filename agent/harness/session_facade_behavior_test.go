package harness_test

import (
	"testing"

	harness "github.com/OrdalieTech/pigo/agent/harness"
)

func TestSessionFacadeSanitizesNameLineBreaksAndJavaScriptWhitespace(t *testing.T) {
	storage, err := harness.NewInMemorySessionStorage(nil, harness.SessionMetadata{
		ID: "session", CreatedAt: "2026-01-01T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := harness.NewSession(storage)
	entryID, err := session.AppendName("\ufeff\u00a0 hello\r\nworld\n\nagain \u1680")
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := session.Entry(entryID)
	if !ok || entry.Name != "hello world again" {
		t.Fatalf("stored session name = %#v", entry)
	}
	name, ok, err := session.Name()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "hello world again" {
		t.Fatalf("session name = %q, %v", name, ok)
	}
}
