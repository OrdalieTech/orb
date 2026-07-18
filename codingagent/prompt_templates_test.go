package codingagent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseCommandArgsMatchesUpstreamGrammar(t *testing.T) {
	tests := map[string][]string{
		`a b c`:                          {"a", "b", "c"},
		`"first arg" second`:             {"first arg", "second"},
		`'first arg' second`:             {"first arg", "second"},
		`"" " "`:                         {" "},
		"label-2\n\nHere is description": {"label-2", "Here", "is", "description"},
		`"quoted \"text\""`:              {`quoted \text\`},
		"日本語 🎉 café":                     {"日本語", "🎉", "café"},
	}
	for input, want := range tests {
		if got := ParseCommandArgs(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseCommandArgs(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestSubstituteArgsMatchesUpstreamOnePassAndSlices(t *testing.T) {
	tests := []struct {
		content string
		args    []string
		want    string
	}{
		{"$1: $@ ($ARGUMENTS)", []string{"first", "second"}, "first: first second (first second)"},
		{"$1 $2 $3 $4", []string{"a", "b"}, "a b  "},
		{"$10 $12 $15", positionalValues(15), "val9 val11 val14"},
		{`${1:-7} ${2:-brief}`, []string{}, "7 brief"},
		{`${1:-$ARGUMENTS}`, []string{}, "$ARGUMENTS"},
		{`${@:2}`, []string{"a", "b", "c"}, "b c"},
		{`${@:2:2}`, []string{"a", "b", "c", "d"}, "b c"},
		{`${@:0}`, []string{"a", "b"}, "a b"},
		{`${@:2:0}`, []string{"a", "b"}, ""},
		{"$ARGUMENTS", []string{"$1", "$ARGUMENTS"}, "$1 $ARGUMENTS"},
		{`Price: \$100`, nil, `Price: \`},
	}
	for _, test := range tests {
		if got := SubstituteArgs(test.content, test.args); got != test.want {
			t.Fatalf("SubstituteArgs(%q, %#v) = %q, want %q", test.content, test.args, got, test.want)
		}
	}
}

func positionalValues(count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = "val" + string(rune('0'+index))
	}
	for index := 10; index < count; index++ {
		values[index] = "val1" + string(rune('0'+index-10))
	}
	return values
}

func TestPromptTemplateLoadingAndExpansion(t *testing.T) {
	root := t.TempDir()
	prompts := filepath.Join(root, "prompts")
	if err := os.MkdirAll(filepath.Join(prompts, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(prompts, "review.md"), "---\ndescription: Review staged changes\nargument-hint: \"<path>\"\n---\nReview $1 with ${@:2}")
	mustWriteResource(t, filepath.Join(prompts, "fallback.md"), "\nFirst line description that is deliberately longer than sixty characters to truncate\nBody")
	mustWriteResource(t, filepath.Join(prompts, "nested", "ignored.md"), "Ignored")

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{CWD: root, AgentDir: root, PromptPaths: []string{prompts}})
	if len(templates) != 2 || templates[0].Name != "fallback" || templates[1].Name != "review" {
		t.Fatalf("templates = %#v", templates)
	}
	if templates[1].ArgumentHint != "<path>" || templates[1].Description != "Review staged changes" {
		t.Fatalf("review metadata = %#v", templates[1])
	}
	if got := ExpandPromptTemplate(`/review src/main.go "focus here"`, templates); got != "Review src/main.go with focus here" {
		t.Fatalf("expanded = %q", got)
	}
	if got := ExpandPromptTemplate("/review\nfile.go", templates); got != "Review file.go with " {
		t.Fatalf("newline expansion = %q", got)
	}
	if got := ExpandPromptTemplate("/missing x", templates); got != "/missing x" {
		t.Fatalf("unknown template = %q", got)
	}
}
