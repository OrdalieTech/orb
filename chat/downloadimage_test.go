package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// zeroReader streams zeros forever without allocating its length.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// streamAdapter serves one unbounded download stream of a fixed length.
type streamAdapter struct {
	*fauxAdapter
	length     int64
	downloaded bool
}

func (a *streamAdapter) Download(ctx context.Context, ref AttachmentRef) (io.ReadCloser, string, error) {
	a.downloaded = true
	return io.NopCloser(io.LimitReader(zeroReader{}, a.length)), "image/png", nil
}

func newDownloadTestProcessor() *Processor {
	return &Processor{logger: slog.New(slog.DiscardHandler)}
}

func TestDownloadImageSkipsOversizedRefWithoutDownloading(t *testing.T) {
	p := newDownloadTestProcessor()
	adapter := &streamAdapter{fauxAdapter: &fauxAdapter{}, length: 1}
	ref := AttachmentRef{Kind: "photo", ID: "big", Size: maxImageBytes + 1}
	if image := p.downloadImage(context.Background(), adapter, ref); image != nil {
		t.Fatalf("downloadImage = %v, want nil for oversized ref", image)
	}
	if adapter.downloaded {
		t.Fatal("Download was called for a ref already known to be oversized")
	}
}

func TestDownloadImageCapsUnsizedStream(t *testing.T) {
	p := newDownloadTestProcessor()
	adapter := &streamAdapter{fauxAdapter: &fauxAdapter{}, length: maxImageBytes + 1}
	ref := AttachmentRef{Kind: "photo", ID: "big-stream"}
	if image := p.downloadImage(context.Background(), adapter, ref); image != nil {
		t.Fatal("downloadImage returned image content for a stream over the cap")
	}
}

func TestDownloadImageAcceptsStreamAtCap(t *testing.T) {
	p := newDownloadTestProcessor()
	adapter := &streamAdapter{fauxAdapter: &fauxAdapter{}, length: 16}
	ref := AttachmentRef{Kind: "photo", ID: "small", Size: 16}
	image := p.downloadImage(context.Background(), adapter, ref)
	if image == nil {
		t.Fatal("downloadImage = nil, want image content under the cap")
	}
	if image.MimeType != "image/png" {
		t.Fatalf("MimeType = %q, want image/png", image.MimeType)
	}
}
