package runechunk

import "testing"

func TestUTF16LimitsPreserveRuneBoundaries(t *testing.T) {
	for _, test := range []struct {
		text  string
		limit int
		want  string
	}{
		{"ab😀cd", -1, ""}, {"", -1, ""}, {"ab😀cd", 0, ""},
		{"ab😀cd", 3, "ab"}, {"ab😀cd", 4, "ab😀"}, {"ab😀cd", 20, "ab😀cd"},
	} {
		if got := TruncateUTF16(test.text, test.limit); got != test.want {
			t.Fatalf("TruncateUTF16(%q, %d) = %q, want %q", test.text, test.limit, got, test.want)
		}
	}
	if got := LenUTF16("a😀日本語"); got != 6 {
		t.Fatalf("UTF-16 length = %d, want 6", got)
	}
}
