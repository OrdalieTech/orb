package tools

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/OrdalieTech/pigo/internal/truncate"
)

func TestOutputAccumulatorDecodesStreamingUTF8AndInitialBOM(t *testing.T) {
	output := NewOutputAccumulator()
	for _, chunk := range [][]byte{{0xef}, {0xbb, 0xbf, 0xe2}, {0x82}, {0xac, '\n'}} {
		if err := output.Append(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "€\n" || snapshot.Truncation.TotalLines != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestOutputAccumulatorSpillsOriginalBytesAndKeepsPathAfterClose(t *testing.T) {
	tempFilePrefix := "pi-output-test"
	output := NewOutputAccumulator(OutputAccumulatorOptions{
		MaxBytes:       truncate.Int(3),
		TempFilePrefix: &tempFilePrefix,
	})
	raw := []byte{0xff, 0xfe, 'x', '\n'}
	if err := output.Append(raw); err != nil {
		t.Fatal(err)
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Snapshot(OutputSnapshotOptions{PersistIfTruncated: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FullOutputPath == "" || !snapshot.Truncation.Truncated {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	path := snapshot.FullOutputPath
	t.Cleanup(func() { _ = os.Remove(path) })
	if err := output.CloseTempFile(); err != nil {
		t.Fatal(err)
	}
	afterClose, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterClose.FullOutputPath != path {
		t.Fatalf("path after close = %q, want %q", afterClose.FullOutputPath, path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, raw) {
		t.Fatalf("spilled bytes = %x, want %x", written, raw)
	}
}

func TestOutputAccumulatorTransformedTextUsesRawSizeForSpill(t *testing.T) {
	tempFilePrefix := "pi-output-transformed-test"
	output := NewOutputAccumulator(OutputAccumulatorOptions{
		MaxBytes:       truncate.Int(3),
		TempFilePrefix: &tempFilePrefix,
	})
	if err := output.appendTransformed(4, "x"); err != nil {
		t.Fatal(err)
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Truncation.Truncated || snapshot.Content != "x" || snapshot.FullOutputPath == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	t.Cleanup(func() { _ = os.Remove(snapshot.FullOutputPath) })
	if err := output.CloseTempFile(); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(snapshot.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "x" {
		t.Fatalf("transformed spill = %q", written)
	}
}

func TestRPCBashPipelinePreservesSplitUTF8(t *testing.T) {
	output := NewOutputAccumulator()
	var decoder streamingUTF8Decoder
	for _, chunk := range [][]byte{{0xe2}, {0x82}, {0xac, '\r', '\n'}} {
		text := sanitizeBashOutput(decoder.Decode(chunk, false))
		if err := output.appendTransformed(len(chunk), text); err != nil {
			t.Fatal(err)
		}
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "€\n" {
		t.Fatalf("bash output = %q", snapshot.Content)
	}
}

func TestOutputAccumulatorFinishIsIdempotentAndRejectsAppend(t *testing.T) {
	output := NewOutputAccumulator()
	if err := output.Append([]byte{0xe2, 0x82}); err != nil {
		t.Fatal(err)
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := output.Append([]byte("late")); err == nil || err.Error() != "Cannot append to a finished output accumulator" {
		t.Fatalf("append error = %v", err)
	}
	snapshot, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "�" || snapshot.Truncation.TotalBytes != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestOutputAccumulatorHonorsExplicitEmptyTempPrefix(t *testing.T) {
	emptyPrefix := ""
	output := NewOutputAccumulator(OutputAccumulatorOptions{TempFilePrefix: &emptyPrefix})
	if output.tempFilePrefix != "" {
		t.Fatalf("temp prefix = %q, want explicit empty prefix", output.tempFilePrefix)
	}
}

func TestOutputAccumulatorDefaultsTempPrefixWhenOtherOptionsArePresent(t *testing.T) {
	output := NewOutputAccumulator(OutputAccumulatorOptions{MaxBytes: truncate.Int(3)})
	if output.tempFilePrefix != "pi-output" {
		t.Fatalf("temp prefix = %q, want default", output.tempFilePrefix)
	}
}

func TestOutputAccumulatorEmitsInvalidContinuationAsSoonAsDecidable(t *testing.T) {
	for _, testCase := range []struct {
		chunks [][]byte
		want   string
	}{
		{chunks: [][]byte{{0xe2}, {'A'}}, want: "�A"},
		{chunks: [][]byte{{0xf0}, {0x90}, {'A'}}, want: "�A"},
		{chunks: [][]byte{{0xed}, {0xa0}}, want: "��"},
	} {
		chunks := testCase.chunks
		output := NewOutputAccumulator()
		for index, chunk := range chunks {
			if err := output.Append(chunk); err != nil {
				t.Fatal(err)
			}
			snapshot, err := output.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if index < len(chunks)-1 && snapshot.Content != "" {
				t.Fatalf("chunks %x premature content = %q", chunks, snapshot.Content)
			}
		}
		snapshot, err := output.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Content != testCase.want {
			t.Fatalf("chunks %x content = %q, want %q", chunks, snapshot.Content, testCase.want)
		}
	}
}

func TestOutputAccumulatorConcurrentAppendIsRaceSafe(t *testing.T) {
	output := NewOutputAccumulator(OutputAccumulatorOptions{MaxLines: truncate.Int(200)})
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := output.Append([]byte("x\n")); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	group.Wait()
	if err := output.Finish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := output.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Truncation.TotalLines != 100 || snapshot.Truncation.Truncated {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
