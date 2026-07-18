package session_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent/harness"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestRehydratedHarnessBytesDoNotClaimDurablePersistence(t *testing.T) {
	path := t.TempDir() + "/rehydrated.jsonl"
	storage, err := harness.RehydrateJSONLSession([]byte(
		`{"type":"session","version":3,"id":"rehydrated","timestamp":"2026-07-18T00:00:00.000Z","cwd":"/fixture"}`+"\n",
	), path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	if manager.IsPersisted() {
		t.Fatal("byte rehydration without a durable append callback reported a persisted session")
	}
	if got := manager.GetSessionFile(); got != "" {
		t.Fatalf("non-durable rehydrated session file = %q, want empty", got)
	}
	if _, err := manager.AppendMessage(map[string]any{"role": "user", "content": "memory only"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rehydrated byte storage unexpectedly created a durable file: %v", err)
	}
}

func TestInMemoryHarnessAppendIsReflectedInSessionManagerJSONL(t *testing.T) {
	storage, err := harness.NewInMemorySessionStorage(nil, harness.SessionMetadata{
		ID: "memory", CreatedAt: "2026-07-18T00:00:00.000Z", CWD: "/fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "message", ID: "external", Timestamp: "2026-07-18T00:00:01.000Z",
		Message: json.RawMessage(`{"role":"user","content":"external write"}`),
	}); err != nil {
		t.Fatal(err)
	}

	encoded, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"id":"external"`) {
		t.Fatalf("manager JSONL omitted the storage-side append:\n%s", encoded)
	}
}

func TestHarnessAdapterTreeUsesResolvedHarnessLabels(t *testing.T) {
	rootID := "root"
	childID := "child"
	rootLabelID := "root-label"
	childLabelID := "child-label"
	trimmedLabel := "\ufeff\u00a0checkpoint\u3000"
	clearedLabel := "\ufeff\u00a0\u3000"
	storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
		{
			Type: "message", ID: rootID, Timestamp: "2026-07-18T00:00:00.000Z",
			Message: json.RawMessage(`{"role":"user","content":"root"}`),
		},
		{
			Type: "message", ID: childID, ParentID: &rootID, Timestamp: "2026-07-18T00:00:01.000Z",
			Message: json.RawMessage(`{"role":"user","content":"child"}`),
		},
		{
			Type: "label", ID: childLabelID, ParentID: &childID, Timestamp: "2026-07-18T00:00:02.000Z",
			TargetID: &childID, HasTargetID: true, Label: &trimmedLabel,
		},
		{
			Type: "label", ID: rootLabelID, ParentID: &childLabelID, Timestamp: "2026-07-18T00:00:03.000Z",
			TargetID: &rootID, HasTargetID: true, Label: &trimmedLabel,
		},
		{
			Type: "label", ID: "root-label-clear", ParentID: &rootLabelID, Timestamp: "2026-07-18T00:00:04.000Z",
			TargetID: &rootID, HasTargetID: true, Label: &clearedLabel,
		},
	}, harness.SessionMetadata{
		ID: "labels", CreatedAt: "2026-07-18T00:00:00.000Z", CWD: "/fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	if got := manager.GetLabel(childID); got == nil || *got != "checkpoint" {
		t.Fatalf("GetLabel(%q) = %v, want trimmed label %q", childID, got, "checkpoint")
	}
	if got := manager.GetLabel(rootID); got != nil {
		t.Fatalf("GetLabel(%q) = %q, want cleared label", rootID, *got)
	}

	tree := manager.GetTree()
	child := findHarnessTreeNode(tree, childID)
	if child == nil {
		t.Fatalf("GetTree omitted entry %q", childID)
	} else if child.Label == nil {
		t.Errorf("GetTree label for %q = nil, want the same trimmed label %q as GetLabel", childID, "checkpoint")
	} else if *child.Label != "checkpoint" {
		t.Errorf("GetTree label for %q = %q, want the same trimmed label %q as GetLabel", childID, *child.Label, "checkpoint")
	}
	root := findHarnessTreeNode(tree, rootID)
	if root == nil {
		t.Fatalf("GetTree omitted entry %q", rootID)
	} else if root.Label != nil {
		t.Errorf("GetTree label for %q = %q, want the same cleared label as GetLabel", rootID, *root.Label)
	}
}

func TestInMemoryHarnessAdapterJSONLMatchesHarnessWire(t *testing.T) {
	cwd := t.TempDir()
	customID := "custom"
	storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
		{
			Type: "custom", ID: customID, Timestamp: "2026-07-18T00:00:01.000Z",
			CustomType: "state", Data: json.RawMessage(`{"ok":true}`),
		},
		{
			Type: "custom_message", ID: "custom-message", ParentID: &customID, Timestamp: "2026-07-18T00:00:02.000Z",
			CustomType: "notice", Content: json.RawMessage(`"hello"`), Display: true,
			Details: json.RawMessage(`{"source":"test"}`),
		},
	}, harness.SessionMetadata{
		ID: "memory-wire", CreatedAt: "2026-07-18T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(cwd))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("adapter JSONL has %d lines, want 3:\n%s", len(lines), encoded)
	}

	t.Run("effective cwd produces valid harness JSONL", func(t *testing.T) {
		if _, err := harness.RehydrateJSONLSession(encoded, "<adapter>"); err != nil {
			t.Errorf("rehydrating adapter JSONL: %v", err)
		}
		encodedCWD, err := json.Marshal(cwd)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"type":"session","version":3,"id":"memory-wire","timestamp":"2026-07-18T00:00:00.000Z","cwd":` + string(encodedCWD) + `}`
		if lines[0] != want {
			t.Errorf("adapter header = %s, want %s", lines[0], want)
		}
	})

	t.Run("custom entries keep harness field order", func(t *testing.T) {
		wantCustom := `{"type":"custom","id":"custom","parentId":null,"timestamp":"2026-07-18T00:00:01.000Z","customType":"state","data":{"ok":true}}`
		if lines[1] != wantCustom {
			t.Errorf("custom entry = %s, want %s", lines[1], wantCustom)
		}
		wantMessage := `{"type":"custom_message","id":"custom-message","parentId":"custom","timestamp":"2026-07-18T00:00:02.000Z","customType":"notice","content":"hello","display":true,"details":{"source":"test"}}`
		if lines[2] != wantMessage {
			t.Errorf("custom message entry = %s, want %s", lines[2], wantMessage)
		}
	})
}

func findHarnessTreeNode(nodes []*sessionstore.SessionTreeNode, id string) *sessionstore.SessionTreeNode {
	for _, node := range nodes {
		if node.Entry.ID == id {
			return node
		}
		if found := findHarnessTreeNode(node.Children, id); found != nil {
			return found
		}
	}
	return nil
}
