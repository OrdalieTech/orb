package harness

import "testing"

// Both Go frontmatter parsers must compensate for yaml.v3 not synthesizing the
// end-of-input line break that npm yaml gives a trailing block scalar, and an
// empty ---/--- block must not index normalized[4:3].
func TestHarnessFrontmatterTrailingBlockScalarAndEmptyBlock(t *testing.T) {
	values, _, err := parseHarnessFrontmatter("---\ndesc: >\n  a\n  b\n---\nbody\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := values["desc"]; got != "a b\n" {
		t.Fatalf("desc = %q, want %q (clip chomping keeps the trailing newline)", got, "a b\n")
	}
	if _, _, err := parseHarnessFrontmatter("---\n---\nbody\n"); err != nil {
		t.Fatalf("empty frontmatter block: %v", err)
	}
}
