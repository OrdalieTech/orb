package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/OrdalieTech/pigo/agent/harness"
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

func TestSessionStorageLabelsUseJavaScriptTrim(t *testing.T) {
	storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{{
		Type: "message", ID: "root", Timestamp: "2026-01-01T00:00:00.000Z",
		Message: json.RawMessage(`{"role":"user"}`),
	}}, harness.SessionMetadata{ID: "session", CreatedAt: "2026-01-01T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	label := "\ufeff\u00a0 checkpoint \u1680"
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "label", ID: "label-set", ParentID: stringPointer("root"),
		Timestamp: "2026-01-01T00:00:01.000Z", TargetID: stringPointer("root"), HasTargetID: true,
		Label: &label,
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := storage.Label("root"); !ok || got != "checkpoint" {
		t.Fatalf("label = %q, %v", got, ok)
	}
	blank := "\u202f\u3000"
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "label", ID: "label-clear", ParentID: stringPointer("label-set"),
		Timestamp: "2026-01-01T00:00:02.000Z", TargetID: stringPointer("root"), HasTargetID: true,
		Label: &blank,
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := storage.Label("root"); ok {
		t.Fatalf("cleared label = %q", got)
	}
}

func TestJSONLSessionStorageAppendsExactUpstreamBytes(t *testing.T) {
	initial := []byte(`{"type":"session","version":3,"id":"session","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp","metadata":{"z":1, "a":2}}` + "\n")
	storage, err := harness.RehydrateJSONLSession(initial, "/tmp/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "active_tools_change", ID: "tools", Timestamp: "2026-01-01T00:00:01.000Z",
		ActiveToolNames: []string{"read", "bash <>&"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := storage.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := string(initial) + `{"type":"active_tools_change","id":"tools","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","activeToolNames":["read","bash <>&"]}` + "\n"
	if string(got) != want {
		t.Fatalf("JSONL bytes\ngot:  %s\nwant: %s", got, want)
	}
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
