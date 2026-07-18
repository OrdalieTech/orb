package tui

import (
	"testing"
	"time"
)

func TestPrimitiveComponents(t *testing.T) {
	text := NewText("hello world", 1, 1, nil)
	lines := text.Render(8)
	if len(lines) != 4 || lines[1] != " hello  " || lines[2] != " world  " {
		t.Fatalf("Text.Render = %#v", lines)
	}

	truncated := NewTruncatedText("first line that is long\nsecond", 1, 1)
	lines = truncated.Render(12)
	if len(lines) != 3 || VisibleWidth(lines[1]) != 12 || lines[1] != " first l\x1b[0m...\x1b[0m " {
		t.Fatalf("TruncatedText.Render = %#v", lines)
	}

	container := &Container{}
	container.AddChild(NewSpacer(2))
	container.AddChild(NewTruncatedText("ok", 0, 0))
	if got := container.Render(4); !equalLines(got, []string{"", "", "ok  "}) {
		t.Fatalf("Container.Render = %#v", got)
	}

	box := NewBox(1, 1, func(value string) string { return "\x1b[44m" + value + "\x1b[49m" })
	box.AddChild(NewTruncatedText("ok", 0, 0))
	lines = box.Render(6)
	if len(lines) != 3 {
		t.Fatalf("Box line count = %d", len(lines))
	}
	for _, line := range lines {
		if VisibleWidth(line) != 6 {
			t.Fatalf("Box line width = %d: %q", VisibleWidth(line), line)
		}
	}
}

type renderCounter struct{ count int }

func (counter *renderCounter) RequestRender() { counter.count++ }

func TestLoaderStaticIndicator(t *testing.T) {
	counter := &renderCounter{}
	loader := NewLoader(counter, func(value string) string { return "<" + value + ">" }, func(value string) string { return "[" + value + "]" }, "Work", &LoaderIndicatorOptions{Frames: []string{"*"}, Interval: time.Millisecond})
	defer loader.Stop()
	lines := loader.Render(20)
	if !equalLines(lines, []string{"", " * [Work]           "}) {
		t.Fatalf("Loader.Render = %#v", lines)
	}
	if counter.count == 0 {
		t.Fatal("loader did not request a render")
	}
}

func TestCancellableLoaderUsesSelectCancelBinding(t *testing.T) {
	loader := NewCancellableLoader(nil, nil, nil, "Work", &LoaderIndicatorOptions{Frames: []string{}})
	defer loader.Dispose()
	aborts := 0
	loader.OnAbort = func() { aborts++ }
	loader.HandleInput(KeyEvent{Raw: "a"})
	if loader.Aborted() {
		t.Fatal("ordinary input aborted loader")
	}
	loader.HandleInput(KeyEvent{Raw: "\x1b"})
	if !loader.Aborted() || loader.Context().Err() == nil || aborts != 1 {
		t.Fatalf("aborted=%v err=%v callbacks=%d", loader.Aborted(), loader.Context().Err(), aborts)
	}
}
