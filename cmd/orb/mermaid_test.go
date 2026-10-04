package main

import (
	"bytes"
	"strings"
	"testing"
)

// Clients with no renderer of their own draw a diagram through orb mermaid; text that is no
// diagram says so by its exit code, so they show the code block instead.
func TestOrbMermaidDrawsADiagramAsText(t *testing.T) {
	var out bytes.Buffer
	if code := runMermaid(strings.NewReader("graph LR\n  A[phone] --> B[lab-3]\n"), &out); code != 0 || !strings.Contains(out.String(), "│ phone ├") || !strings.Contains(out.String(), "lab-3") {
		t.Fatalf("code %d, art:\n%s", code, out.String())
	}
	if code := runMermaid(strings.NewReader("not a diagram"), &out); code != 1 {
		t.Fatalf("not a diagram: code %d", code)
	}
}
