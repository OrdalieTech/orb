package main

import (
	"reflect"
	"testing"
)

func TestResolveToolSelection(t *testing.T) {
	tests := []struct {
		name       string
		args       CLIArgs
		registered []string
		active     []string
	}{
		{
			name:       "defaults include only default active tools",
			registered: []string{"read", "bash", "edit", "write", "grep", "custom"},
			active:     []string{"read", "bash", "edit", "write"},
		},
		{
			name:       "explicit allowlist keeps CLI order and known names",
			args:       CLIArgs{Tools: []string{"custom", "missing", "read"}},
			registered: []string{"read", "custom"},
			active:     []string{"custom", "read"},
		},
		{
			name:       "denylist filters explicit tools",
			args:       CLIArgs{Tools: []string{"read", "bash"}, ExcludeTools: []string{"bash"}},
			registered: []string{"read", "bash"},
			active:     []string{"read"},
		},
		{
			name:       "no tools is a defined empty allowlist",
			args:       CLIArgs{NoTools: true},
			registered: []string{"read"},
			active:     []string{},
		},
		{
			name:       "no builtins leaves allowlist unrestricted for extensions",
			args:       CLIArgs{NoBuiltinTools: true},
			registered: []string{"read", "custom"},
			active:     []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveToolSelection(test.args, test.registered)
			if !reflect.DeepEqual(got.Active, nonNil(test.active)) {
				t.Fatalf("selection = %#v", got)
			}
		})
	}
}
