package modes

import (
	"encoding/json"
	"testing"

	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

func TestTreeSelectItemsKeepLinearConversationFlat(t *testing.T) {
	root := treeTestMessage("root", "", "user", "one")
	reply := treeTestMessage("reply", "root", "assistant", "two")
	leaf := treeTestMessage("leaf", "reply", "user", "three")
	root.Children = []*sessionstore.SessionTreeNode{reply}
	reply.Children = []*sessionstore.SessionTreeNode{leaf}

	items := treeSelectItems([]*sessionstore.SessionTreeNode{root}, "leaf", "default")
	assertTreeItems(t, items, []string{
		"• user: one",
		"• assistant: two",
		"• user: three",
	})
}

func TestTreeSelectItemsIndentOnlyAtBranches(t *testing.T) {
	root := treeTestMessage("root", "", "user", "root")
	old := treeTestMessage("old", "root", "user", "old")
	active := treeTestMessage("active", "root", "assistant", "active")
	leaf := treeTestMessage("leaf", "active", "user", "leaf")
	root.Children = []*sessionstore.SessionTreeNode{old, active}
	active.Children = []*sessionstore.SessionTreeNode{leaf}

	items := treeSelectItems([]*sessionstore.SessionTreeNode{root}, "leaf", "default")
	assertTreeItems(t, items, []string{
		"• user: root",
		"├─ • assistant: active",
		"│     • user: leaf",
		"└─ user: old",
	})
}

func TestTreeSelectItemsReattachAcrossHiddenEntries(t *testing.T) {
	root := treeTestMessage("root", "", "user", "root")
	hidden := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
		Type: "model_change", ID: "hidden", ParentID: treeTestParent("root"),
	}}
	left := treeTestMessage("left", "hidden", "user", "left")
	right := treeTestMessage("right", "hidden", "user", "right")
	root.Children = []*sessionstore.SessionTreeNode{hidden}
	hidden.Children = []*sessionstore.SessionTreeNode{left, right}

	items := treeSelectItems([]*sessionstore.SessionTreeNode{root}, "right", "default")
	assertTreeItems(t, items, []string{
		"• user: root",
		"├─ • user: right",
		"└─ user: left",
	})
}

func assertTreeItems(t *testing.T, items []tui.SelectItem, labels []string) {
	t.Helper()
	if len(items) != len(labels) {
		t.Fatalf("got %d items, want %d", len(items), len(labels))
	}
	for index, label := range labels {
		if items[index].Label != label {
			t.Errorf("item %d label = %q, want %q", index, items[index].Label, label)
		}
		if items[index].Value == "" {
			t.Errorf("item %d has no stable entry ID", index)
		}
	}
}

func treeTestMessage(id, parent, role, text string) *sessionstore.SessionTreeNode {
	message, err := json.Marshal(map[string]any{"role": role, "content": text})
	if err != nil {
		panic(err)
	}
	return &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
		Type: "message", ID: id, ParentID: treeTestParent(parent), Message: message,
	}}
}

func treeTestParent(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
