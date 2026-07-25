package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type fragmentedReader struct {
	data []byte
	step int
}

func (reader *fragmentedReader) Read(target []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	count := reader.step
	if count > len(reader.data) {
		count = len(reader.data)
	}
	if count > len(target) {
		count = len(target)
	}
	copy(target, reader.data[:count])
	reader.data = reader.data[count:]
	return count, nil
}

func TestCodecReadsFragmentedLines(t *testing.T) {
	encoded := []byte(`{"protocol":"pigo-extension-host","version":1,"kind":"event","method":"log","params":{"message":"ok"}}` + "\n")
	codec := newCodec(&fragmentedReader{data: encoded, step: 3}, io.Discard)
	value, err := codec.read()
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != frameEvent || value.Method != "log" || !bytes.Contains(value.Params, []byte(`"ok"`)) {
		t.Fatalf("frame = %#v", value)
	}
}

func TestGenerationCorrelatesInterleavedResponses(t *testing.T) {
	first := make(chan pendingResponse, 1)
	second := make(chan pendingResponse, 1)
	generation := &generation{
		pending: map[string]chan pendingResponse{"pigo-1": first, "pigo-2": second},
		updates: make(map[string]func(json.RawMessage)),
	}
	generation.routeResponse(frame{ID: "pigo-2", Result: json.RawMessage(`{"order":2}`)})
	generation.routeResponse(frame{ID: "pigo-1", Result: json.RawMessage(`{"order":1}`)})
	if got := string((<-first).result); got != `{"order":1}` {
		t.Fatalf("first response = %s", got)
	}
	if got := string((<-second).result); got != `{"order":2}` {
		t.Fatalf("second response = %s", got)
	}
}

func TestCodecRejectsOversizedFrames(t *testing.T) {
	encoded := append(bytes.Repeat([]byte{'x'}, MaxFrameSize+1), '\n')
	_, err := newCodec(bytes.NewReader(encoded), io.Discard).read()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
}

func TestCodecWritesOneJSONLine(t *testing.T) {
	var output bytes.Buffer
	codec := newCodec(strings.NewReader(""), &output)
	value, err := eventFrame("log", map[string]string{"message": "line\nvalue"})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.write(value); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte{'\n'}) != 1 || !json.Valid(bytes.TrimSuffix(output.Bytes(), []byte{'\n'})) {
		t.Fatalf("encoded frame = %q", output.String())
	}
}

// Node writes fatal reports to fd 2 with bare LFs, which staircase once the TUI
// has cleared OPOST. Only a terminal is translated; pipes stay byte-exact.
func TestTerminalSafeStderrTranslatesBareLinefeeds(t *testing.T) {
	var buffer bytes.Buffer
	if got := terminalSafeStderr(&buffer); got != io.Writer(&buffer) {
		t.Fatalf("non-terminal writer was wrapped")
	}

	for _, test := range []struct{ name, input, want string }{
		{"bare", "a\nb\n", "a\r\nb\r\n"},
		{"already paired", "a\r\nb", "a\r\nb"},
		{"mixed", "a\r\nb\nc", "a\r\nb\r\nc"},
		{"leading", "\nx", "\r\nx"},
		{"none", "plain", "plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sink bytes.Buffer
			writer := crlfWriter{inner: &sink}
			n, err := writer.Write([]byte(test.input))
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if n != len(test.input) {
				t.Fatalf("n = %d, want %d (io.Writer must report input length)", n, len(test.input))
			}
			if sink.String() != test.want {
				t.Fatalf("got %q, want %q", sink.String(), test.want)
			}
		})
	}
}
