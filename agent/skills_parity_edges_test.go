package agent

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Verified against upstream v0.80.10 core/skills.ts via live driver comparison:
// nested ignore-file patterns are prefixed with their relative dir before the
// npm ignore library anchors them, so they only match the ignore file's own
// directory, while root-level slash-less patterns (anchored "/foo" ones
// included, an upstream bug kept for parity) match basenames at any depth.
func TestSkillIgnoreNestedBasenameAnchorsToOwnDir(t *testing.T) {
	root := t.TempDir()
	mustWriteResource(t, filepath.Join(root, ".gitignore"), "rootblocked\n")
	mustWriteResource(t, filepath.Join(root, "ign", ".gitignore"), "blocked\ndropme\n!dropme\nsl/x\n")
	mustWriteResource(t, filepath.Join(root, "ok", "SKILL.md"), "---\nname: ok\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "sub", "rootblocked", "SKILL.md"), "---\nname: root-deep-blocked\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "ign", "blocked", "SKILL.md"), "---\nname: ign-blocked\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "ign", "deep", "blocked", "SKILL.md"), "---\nname: ign-deep-blocked\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "ign", "dropme", "SKILL.md"), "---\nname: ign-dropme\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "ign", "sl", "x", "SKILL.md"), "---\nname: ign-sl-x\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "ign", "other", "sl", "x", "SKILL.md"), "---\nname: ign-other-sl-x\ndescription: d.\n---\nX.\n")

	result := loadSkillsFromDirInternal(root, "path", true, &skillIgnoreMatcher{}, root, map[string]bool{})
	names := make(map[string]bool)
	for _, skill := range result.Skills {
		names[skill.Name] = true
	}
	want := map[string]bool{"ok": true, "ign-deep-blocked": true, "ign-dropme": true, "ign-other-sl-x": true}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("loaded skills = %v, want %v", names, want)
	}
}

func TestSkillIgnoreRootAnchoredPatternMatchesAnyDepth(t *testing.T) {
	root := t.TempDir()
	mustWriteResource(t, filepath.Join(root, ".gitignore"), "/anchored-only\n")
	mustWriteResource(t, filepath.Join(root, "anchored-only", "SKILL.md"), "---\nname: anchored-root\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "sub", "anchored-only", "SKILL.md"), "---\nname: anchored-deep\ndescription: d.\n---\nX.\n")
	// A nested "/pattern" is prefixed with the nested dir upstream, so it stays
	// scoped to that dir's immediate child.
	mustWriteResource(t, filepath.Join(root, "nest", ".gitignore"), "/nblocked\n")
	mustWriteResource(t, filepath.Join(root, "nest", "nblocked", "SKILL.md"), "---\nname: nblocked-child\ndescription: d.\n---\nX.\n")
	mustWriteResource(t, filepath.Join(root, "nest", "deep", "nblocked", "SKILL.md"), "---\nname: nblocked-deep\ndescription: d.\n---\nX.\n")

	result := loadSkillsFromDirInternal(root, "path", true, &skillIgnoreMatcher{}, root, map[string]bool{})
	names := make(map[string]bool)
	for _, skill := range result.Skills {
		names[skill.Name] = true
	}
	if want := map[string]bool{"nblocked-deep": true}; !reflect.DeepEqual(names, want) {
		t.Fatalf("loaded skills = %v, want %v", names, want)
	}
}
