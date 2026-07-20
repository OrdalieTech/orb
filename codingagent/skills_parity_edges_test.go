package codingagent

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

	result := LoadSkillsFromDir(LoadSkillsFromDirOptions{Dir: root, Source: "path"})
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

	result := LoadSkillsFromDir(LoadSkillsFromDirOptions{Dir: root, Source: "path"})
	names := make(map[string]bool)
	for _, skill := range result.Skills {
		names[skill.Name] = true
	}
	if want := map[string]bool{"nblocked-deep": true}; !reflect.DeepEqual(names, want) {
		t.Fatalf("loaded skills = %v, want %v", names, want)
	}
}

// Upstream validateName / validateDescription call string methods on the raw
// frontmatter value; truthy non-strings throw a TypeError that discards the
// validator's own messages and is caught as a single warning, so the skill is
// not loaded. Falsy values (0, false, null, "") fall back like absent ones.
func TestSkillFrontmatterNonStringValues(t *testing.T) {
	cases := []struct {
		dir      string
		content  string
		loaded   string // expected skill name, "" when the skill must not load
		messages []string
	}{
		{"fm-numname", "---\nname: 123\ndescription: Numeric name value.\n---\nBody.\n", "", []string{"name.startsWith is not a function"}},
		{"fm-floatname", "---\nname: 1.5\ndescription: Float name.\n---\nBody.\n", "", []string{"name.startsWith is not a function"}},
		{"fm-arrname", "---\nname: [a, b]\ndescription: Array name.\n---\nBody.\n", "", []string{"name.startsWith is not a function"}},
		{"fm-truename", "---\nname: true\ndescription: True name.\n---\nBody.\n", "", []string{"name.startsWith is not a function"}},
		{"fm-mapname", "---\nname: {a: b}\ndescription: Map name.\n---\nBody.\n", "", []string{"name.startsWith is not a function"}},
		{"fm-nodesc-numname", "---\nname: 123\n---\nBody.\n", "", []string{"description is required", "name.startsWith is not a function"}},
		{"fm-numdesc", "---\nname: num-desc\ndescription: 123\n---\nBody.\n", "", []string{"description.trim is not a function"}},
		{"fm-arrdesc", "---\nname: arr-desc\ndescription: [a, b]\n---\nBody.\n", "", []string{"description.trim is not a function"}},
		{"fm-zeroname", "---\nname: 0\ndescription: Zero name.\n---\nBody.\n", "fm-zeroname", nil},
		{"fm-falsename", "---\nname: false\ndescription: False name.\n---\nBody.\n", "fm-falsename", nil},
		{"fm-nullname", "---\nname: null\ndescription: Null name.\n---\nBody.\n", "fm-nullname", nil},
		{"fm-nanname", "---\nname: .nan\ndescription: NaN name.\n---\nBody.\n", "fm-nanname", nil},
		{"fm-nulldesc", "---\nname: null-desc\ndescription: null\n---\nBody.\n", "", []string{"description is required"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.dir, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, testCase.dir, "SKILL.md")
			mustWriteResource(t, path, testCase.content)
			result := LoadSkillsFromDir(LoadSkillsFromDirOptions{Dir: root, Source: "path"})
			messages := make([]string, 0, len(result.Diagnostics))
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Path != path {
					t.Fatalf("diagnostic path = %q, want %q", diagnostic.Path, path)
				}
				messages = append(messages, diagnostic.Message)
			}
			if len(messages) != len(testCase.messages) || (len(messages) > 0 && !reflect.DeepEqual(messages, testCase.messages)) {
				t.Fatalf("diagnostics = %q, want %q", messages, testCase.messages)
			}
			switch {
			case testCase.loaded == "" && len(result.Skills) != 0:
				t.Fatalf("skill unexpectedly loaded: %#v", result.Skills)
			case testCase.loaded != "" && (len(result.Skills) != 1 || result.Skills[0].Name != testCase.loaded):
				t.Fatalf("skills = %#v, want one named %q", result.Skills, testCase.loaded)
			}
		})
	}
}

func TestLoadSkillsCollisionDiagnosticsTrailWarnings(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	mustWriteResource(t, filepath.Join(first, "one", "SKILL.md"), "---\nname: same\ndescription: First\n---\nOne")
	mustWriteResource(t, filepath.Join(second, "two", "SKILL.md"), "---\nname: same\ndescription: Second\n---\nTwo")
	missing := filepath.Join(root, "missing")

	result := LoadSkills(LoadSkillsOptions{CWD: root, AgentDir: root, SkillPaths: []string{first, second, missing}})
	if len(result.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	// The collision is discovered before the missing-path warning, but upstream
	// concatenates every collision diagnostic after all warnings.
	if result.Diagnostics[0].Type != "warning" || result.Diagnostics[0].Message != "skill path does not exist" || result.Diagnostics[0].Path != missing {
		t.Fatalf("first diagnostic = %#v, want missing-path warning", result.Diagnostics[0])
	}
	last := result.Diagnostics[1]
	if last.Type != "collision" || last.Message != `name "same" collision` || last.Collision == nil || last.Collision.WinnerPath != filepath.Join(first, "one", "SKILL.md") {
		t.Fatalf("last diagnostic = %#v, want trailing collision", last)
	}
}
