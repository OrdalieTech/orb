package tools

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/internal/truncate"
)

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
