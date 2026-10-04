package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/OrdalieTech/orb/engine/harness"
)

func TestSessionStorageLeafEdgeCases(t *testing.T) {
	t.Run("null leaf clears the active path but stays in the physical log", func(t *testing.T) {
		root := harness.SessionTreeEntry{
			Type: "message", ID: "root", Timestamp: "2026-01-01T00:00:00.000Z",
			Message: json.RawMessage(`{"role":"user","content":"root"}`),
		}
		storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
			root,
			{
				Type: "leaf", ID: "leaf-null", ParentID: stringPointer("root"),
				Timestamp: "2026-01-01T00:00:01.000Z", HasTargetID: true,
			},
		}, harness.SessionMetadata{ID: "session", CreatedAt: "2026-01-01T00:00:00.000Z"})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := storage.LeafID()
		if err != nil {
			t.Fatal(err)
		}
		if leaf != nil {
			t.Fatalf("leaf = %q, want nil", *leaf)
		}
		entries := storage.Entries()
		if len(entries) != 2 || entries[1].Type != "leaf" || !entries[1].HasTargetID || entries[1].TargetID != nil {
			t.Fatalf("physical entries = %#v", entries)
		}
	})

	t.Run("dangling persisted leaf is reported when read", func(t *testing.T) {
		content := []byte("{\"type\":\"session\",\"version\":3,\"id\":\"session\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":\"/tmp\"}\n" +
			"{\"type\":\"leaf\",\"id\":\"leaf\",\"parentId\":null,\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"targetId\":\"missing\"}\n")
		storage, err := harness.RehydrateJSONLSession(content, "/tmp/session.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		_, err = storage.LeafID()
		assertSessionErrorCode(t, err, harness.SessionErrorInvalidSession)
	})

	t.Run("broken ancestry is an invalid session", func(t *testing.T) {
		storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{{
			Type: "message", ID: "child", ParentID: stringPointer("missing"),
			Timestamp: "2026-01-01T00:00:00.000Z", Message: json.RawMessage(`{"role":"user"}`),
		}}, harness.SessionMetadata{ID: "session", CreatedAt: "2026-01-01T00:00:00.000Z"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = storage.PathToRootOrCompaction(stringPointer("child"))
		assertSessionErrorCode(t, err, harness.SessionErrorInvalidSession)
	})
}

func stringPointer(value string) *string {
	return &value
}

func assertSessionErrorCode(t *testing.T, err error, want harness.SessionErrorCode) {
	t.Helper()
	var sessionError *harness.SessionError
	if !errors.As(err, &sessionError) || sessionError.Code != want {
		t.Fatalf("error = %v, want SessionError(%s)", err, want)
	}
}
