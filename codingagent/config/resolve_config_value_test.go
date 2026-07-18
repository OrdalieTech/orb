package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestConfigValueInspectionAndStrictResolution(t *testing.T) {
	t.Setenv("PRESENT", "value")
	if name, ok := GetConfigValueEnvVarName("${PRESENT}"); !ok || name != "PRESENT" {
		t.Fatalf("env name = %q, %t", name, ok)
	}
	if _, ok := GetConfigValueEnvVarName("prefix-$PRESENT"); ok {
		t.Fatal("template was reported as a single environment reference")
	}
	if names := GetConfigValueEnvVarNames("$PRESENT/${MISSING}/$PRESENT"); !reflect.DeepEqual(names, []string{"PRESENT", "MISSING"}) {
		t.Fatalf("env names = %#v", names)
	}
	if IsConfigValueConfigured("$PRESENT/$MISSING", nil) {
		t.Fatal("value with missing environment variable reported configured")
	}
	if _, err := ResolveConfigValueOrThrow("$PRESENT/$MISSING", "test value", nil); err == nil || err.Error() != "failed to resolve test value from environment variable: MISSING" {
		t.Fatalf("strict error = %v", err)
	}
	resolved, err := ResolveHeadersOrThrow(map[string]string{"x-key": "$PRESENT"}, "provider", nil)
	if err != nil || !reflect.DeepEqual(resolved, map[string]string{"x-key": "value"}) {
		t.Fatalf("headers = %#v, %v", resolved, err)
	}
}

func TestResolveConfigValueCommandsAreTrimmedAndCachedIncludingFailure(t *testing.T) {
	ClearConfigValueCache()
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
