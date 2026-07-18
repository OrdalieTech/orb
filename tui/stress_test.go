package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type stressLines struct {
	mu    sync.RWMutex
	lines []string
}

func (component *stressLines) set(lines []string) {
	component.mu.Lock()
	component.lines = append(component.lines[:0], lines...)
	component.mu.Unlock()
}

func (component *stressLines) Render(width int) []string {
	component.mu.RLock()
	lines := append([]string(nil), component.lines...)
	component.mu.RUnlock()
	for index, line := range lines {
		lines[index] = TruncateToWidth(line, max(1, width), "…", false)
	}
	return lines
}

func setStressTerminalSize(terminal *fakeTerminal, columns, rows int) func() {
	terminal.mu.Lock()
	terminal.columns, terminal.rows = columns, rows
	resize := terminal.onResize
	terminal.mu.Unlock()
	return resize
}

func TestTUIResizeStormRaceDeterministic(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	component := &stressLines{lines: []string{"initial 你好世界 👩🏽‍💻", "status: ready"}}
	uiInstance := NewTUI(terminal)
	uiInstance.AddChild(component)
	if err := uiInstance.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = uiInstance.Stop() }()

	widths := []int{32, 48, 72, 100, 40, 96, 56, 80}
	heights := []int{8, 12, 20, 30, 16, 24}
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(5)
	go func() {
		defer workers.Done()
		<-start
		for step := 0; step < 256; step++ {
			width := widths[step%len(widths)]
			height := heights[step%len(heights)]
			component.set([]string{
				fmt.Sprintf("resize-%03d 你好世界 👩🏽‍💻 %s", step, strings.Repeat("x", step%91)),
				fmt.Sprintf("viewport %dx%d", width, height),
			})
			if resize := setStressTerminalSize(terminal, width, height); resize != nil {
				resize()
			}
		}
	}()
	for worker := 0; worker < 4; worker++ {
		go func(offset int) {
			defer workers.Done()
			<-start
			for step := 0; step < 256; step++ {
				if (step+offset)%3 == 0 {
					uiInstance.RequestRender()
				} else {
					uiInstance.RenderNow()
				}
			}
		}(worker)
	}
	close(start)
	workers.Wait()

	component.set([]string{"final-resize-frame 你好世界 👩🏽‍💻", "viewport 64x18"})
	setStressTerminalSize(terminal, 64, 18)
	terminal.resetOutput()
	uiInstance.ForceRender()
	output := terminal.output()
	if !strings.Contains(output, "final-resize-frame") || !strings.Contains(output, "\x1b[2J\x1b[H\x1b[3J") {
		t.Fatalf("final forced frame = %q", output)
	}
	if redraws := uiInstance.FullRedraws(); redraws < 2 {
		t.Fatalf("full redraws = %d, want at least 2", redraws)
	}
}

func TestEditorPasteFloodRaceDeterministic(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	uiInstance := NewTUI(terminal)
	editor := NewEditor(uiInstance, EditorTheme{})

	const pasteCount = 72
	payloads := make([]string, pasteCount)
	var want strings.Builder
	for index := range payloads {
		switch index % 3 {
		case 0:
			payloads[index] = fmt.Sprintf("small-%03d-你好-👩🏽‍💻", index)
		case 1:
			lines := make([]string, 12)
			for line := range lines {
				lines[line] = fmt.Sprintf("paste-%03d-line-%02d-日本語", index, line)
			}
			payloads[index] = strings.Join(lines, "\n")
		case 2:
			payloads[index] = fmt.Sprintf("large-%03d-", index) + strings.Repeat("界", 1002)
		}
		want.WriteString(payloads[index])
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(5)
	go func() {
		defer workers.Done()
		<-start
		for _, payload := range payloads {
			raw := bracketedPasteStart + payload + bracketedPasteEnd
			editor.HandleInput(KeyEvent{Raw: raw})
		}
	}()
	widths := []int{32, 48, 72, 100}
	for worker := 0; worker < 4; worker++ {
		go func(offset int) {
			defer workers.Done()
			<-start
			for step := 0; step < pasteCount*2; step++ {
				width := widths[(step+offset)%len(widths)]
				for _, line := range editor.Render(width) {
					if VisibleWidth(line) > width {
						t.Errorf("rendered line exceeds width %d: %q", width, line)
						return
					}
				}
			}
		}(worker)
	}
	close(start)
	workers.Wait()

	if got := editor.GetExpandedText(); got != want.String() {
		t.Fatalf("expanded paste flood length = %d, want %d", len(got), want.Len())
	}
	if got := editor.GetText(); !strings.Contains(got, "[paste #") {
		t.Fatalf("collapsed editor text contains no paste markers: %q", got)
	}
}

func TestStdinBufferPasteFloodArbitraryChunking(t *testing.T) {
	const pasteCount = 256
	want := make([]string, pasteCount)
	var stream strings.Builder
	for index := range want {
		want[index] = fmt.Sprintf("paste-%03d-你好-日本語-👩🏽‍💻-%s", index, strings.Repeat("x", index%29))
		stream.WriteString(bracketedPasteStart)
		stream.WriteString(want[index])
		stream.WriteString(bracketedPasteEnd)
	}

	var got []string
	buffer := NewStdinBuffer(time.Second, func(value string) {
		t.Errorf("unexpected non-paste input %q", value)
	}, func(value string) {
		got = append(got, value)
	})
	defer buffer.Close()

	chunkSizes := []int{1, 2, 3, 5, 8, 13, 21}
	input := stream.String()
	for offset, chunk := 0, 0; offset < len(input); chunk++ {
		end := min(len(input), offset+chunkSizes[chunk%len(chunkSizes)])
		buffer.Process(input[offset:end])
		offset = end
	}
	if !equalLines(got, want) {
		t.Fatalf("paste flood delivered %d payloads, want %d", len(got), len(want))
	}
}

func TestTUIGiantLineAndOutputDeterministic(t *testing.T) {
	giantLine := strings.Repeat("0123456789", 10_000) + "你好世界日本語👩🏽‍💻"
	text := NewText(giantLine, 0, 0, nil)
	editor := NewEditor(NewTUI(newFakeTerminal(80, 24)), EditorTheme{})
	editor.SetText(giantLine)
	for name, lines := range map[string][]string{
		"text":   text.Render(80),
		"editor": editor.Render(80),
	} {
		for lineIndex, line := range lines {
			if width := VisibleWidth(line); width > 80 {
				t.Fatalf("%s line %d width = %d", name, lineIndex, width)
			}
		}
	}

	lines := make([]string, 4096)
	for index := range lines {
		lines[index] = fmt.Sprintf("output-%04d %s", index, strings.Repeat("界👩🏽‍💻", 80))
	}
	component := &stressLines{lines: lines}
	terminal := newFakeTerminal(80, 24)
	uiInstance := NewTUI(terminal)
	uiInstance.AddChild(component)
	if err := uiInstance.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = uiInstance.Stop() }()
	terminal.resetOutput()
	lines[len(lines)-1] = "final giant-output line"
	component.set(lines)
	uiInstance.RenderNow()
	output := terminal.output()
	if !strings.Contains(output, "final giant-output line") {
		t.Fatalf("tail update did not render final line: %q", output)
	}
	if len(output) > 2048 {
		t.Fatalf("giant-output tail update wrote %d bytes", len(output))
	}
}

func FuzzTUIResizePaste(f *testing.F) {
	f.Add("hello world", 80, 24)
	f.Add("你好世界 日本語 👩🏽‍💻", 32, 8)
	f.Add(strings.Repeat("giant-paste-界", 4000), 48, 12)
	f.Fuzz(func(t *testing.T, input string, rawWidth, rawHeight int) {
		if len(input) > 64<<10 {
			input = input[:64<<10]
		}
		input = strings.ToValidUTF8(input, "�")
		input = strings.ReplaceAll(input, bracketedPasteStart, "")
		input = strings.ReplaceAll(input, bracketedPasteEnd, "")
		var printable strings.Builder
		for _, character := range input {
			if character == '\n' || character >= 32 {
				printable.WriteRune(character)
			}
		}
		input = printable.String()
		width := int(uint(rawWidth)%157) + 4
		height := int(uint(rawHeight)%61) + 2

		terminal := newFakeTerminal(width, height)
		uiInstance := NewTUI(terminal)
		editor := NewEditor(uiInstance, EditorTheme{})
		uiInstance.AddChild(editor)
		if err := uiInstance.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = uiInstance.Stop() }()

		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			editor.HandleInput(KeyEvent{Raw: bracketedPasteStart + input + bracketedPasteEnd})
		}()
		go func() {
			defer workers.Done()
			<-start
			for _, size := range [][2]int{{max(4, width/2), height}, {min(160, width+17), max(2, height/2)}, {width, height}} {
				if resize := setStressTerminalSize(terminal, size[0], size[1]); resize != nil {
					resize()
				}
			}
		}()
		close(start)
		workers.Wait()

		setStressTerminalSize(terminal, width, height)
		uiInstance.ForceRender()
		if got := editor.GetExpandedText(); got != input {
			t.Fatalf("expanded paste = %q, want %q", got, input)
		}
		for lineIndex, line := range editor.Render(width) {
			if got := VisibleWidth(line); got > width {
				t.Fatalf("line %d width = %d, terminal width %d", lineIndex, got, width)
			}
		}
	})
}

func FuzzStdinBufferChunkedPaste(f *testing.F) {
	f.Add("hello world", 1)
	f.Add("你好世界 日本語 👩🏽‍💻", 3)
	f.Add(strings.Repeat("chunked-paste-界", 4000), 17)
	f.Fuzz(func(t *testing.T, input string, rawChunkSize int) {
		if len(input) > 64<<10 {
			input = input[:64<<10]
		}
		input = strings.ToValidUTF8(input, "�")
		input = strings.ReplaceAll(input, bracketedPasteStart, "")
		input = strings.ReplaceAll(input, bracketedPasteEnd, "")
		chunkSize := int(uint(rawChunkSize)%97) + 1
		stream := bracketedPasteStart + input + bracketedPasteEnd

		var got []string
		buffer := NewStdinBuffer(time.Second, func(value string) {
			t.Fatalf("unexpected non-paste input %q", value)
		}, func(value string) {
			got = append(got, value)
		})
		defer buffer.Close()
		for offset := 0; offset < len(stream); {
			end := min(len(stream), offset+chunkSize)
			buffer.Process(stream[offset:end])
			offset = end
		}
		if len(got) != 1 || got[0] != input {
			t.Fatalf("chunked paste = %q, want [%q]", got, input)
		}
	})
}

func FuzzTUIRendererLineAndOutput(f *testing.F) {
	f.Add("hello world", 80)
	f.Add("你好世界 日本語 👩🏽‍💻", 32)
	f.Add(strings.Repeat("giant-line-界", 10_000), 48)
	f.Fuzz(func(t *testing.T, input string, rawWidth int) {
		if len(input) > 64<<10 {
			input = input[:64<<10]
		}
		input = strings.ToValidUTF8(input, "�")
		width := int(uint(rawWidth)%157) + 4
		component := NewText(input, 0, 0, nil)
		for lineIndex, line := range component.Render(width) {
			if got := VisibleWidth(line); got > width {
				t.Fatalf("line %d width = %d, terminal width %d", lineIndex, got, width)
			}
		}

		terminal := newFakeTerminal(width, 24)
		uiInstance := NewTUI(terminal)
		uiInstance.AddChild(component)
		if err := uiInstance.Start(); err != nil {
			t.Fatal(err)
		}
		component.SetText(input + "\nchanged")
		uiInstance.RenderNow()
		if err := uiInstance.Stop(); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzEditorRenderLineWidths(f *testing.F) {
	f.Add("short", 80)
	f.Add("你好世界 日本語 👩🏽‍💻", 32)
	f.Add(strings.Repeat("long-editor-line-界", 4000), 48)
	f.Fuzz(func(t *testing.T, input string, rawWidth int) {
		if len(input) > 64<<10 {
			input = input[:64<<10]
		}
		input = strings.ToValidUTF8(input, "�")
		width := int(uint(rawWidth)%157) + 4
		editor := NewEditor(NewTUI(newFakeTerminal(width, 24)), EditorTheme{})
		editor.SetText(input)
		for lineIndex, line := range editor.Render(width) {
			if got := VisibleWidth(line); got > width {
				t.Fatalf("line %d width = %d, terminal width %d", lineIndex, got, width)
			}
		}
	})
}
