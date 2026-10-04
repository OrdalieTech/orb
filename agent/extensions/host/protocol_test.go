package host

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestCodecRejectsOversizedFrames(t *testing.T) {
	encoded := append(bytes.Repeat([]byte{'x'}, MaxFrameSize+1), '\n')
	_, err := newCodec(bytes.NewReader(encoded), io.Discard).read()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
}
