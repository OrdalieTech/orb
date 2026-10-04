package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigValueTemplates(t *testing.T) {
	t.Setenv("GLOBAL_VALUE", "global")
	tests := []struct {
		value  string
		env    map[string]string
		want   string
		wantOK bool
	}{
		{"literal", nil, "literal", true},
		{"$GLOBAL_VALUE", nil, "global", true},
		{"pre-${SCOPED_VALUE}-$GLOBAL_VALUE", map[string]string{"SCOPED_VALUE": "scoped"}, "pre-scoped-global", true},
		{"$GLOBAL_VALUE", map[string]string{"GLOBAL_VALUE": "scoped"}, "scoped", true},
		{"$$HOME-$!command", nil, "$HOME-!command", true},
		{"${INVALID-NAME}", nil, "${INVALID-NAME}", true},
		{"$MISSING", nil, "", false},
		{"prefix-$", nil, "prefix-$", true},
	}
	for _, test := range tests {
		got, ok := ResolveAuthConfigValue(test.value, test.env)
		if got != test.want || ok != test.wantOK {
			t.Errorf("ResolveConfigValue(%q) = %q, %t; want %q, %t", test.value, got, ok, test.want, test.wantOK)
		}
	}
}

func TestResolveConfigValueCommandsAreTrimmedAndCachedIncludingFailure(t *testing.T) {
	value, ok := ResolveAuthConfigValue("!printf '  command-value \\n'", nil)
	if !ok || value != "command-value" {
		t.Fatalf("command = %q, %t", value, ok)
	}

	marker := filepath.Join(t.TempDir(), "marker")
	command := "!test -f '" + marker + "' && printf present"
	if value, ok := ResolveAuthConfigValue(command, nil); ok || value != "" {
		t.Fatalf("missing command = %q, %t", value, ok)
	}
	if err := os.WriteFile(marker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, ok := ResolveAuthConfigValue(command, nil); ok || value != "" {
		t.Fatalf("cached failure = %q, %t", value, ok)
	}
	if value, ok := ResolveAuthConfigValueUncached(command, nil); !ok || value != "present" {
		t.Fatalf("uncached command = %q, %t", value, ok)
	}
}
