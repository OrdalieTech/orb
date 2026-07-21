package whatsapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pigo/chat"
)

func TestDownloadMediaFlow(t *testing.T) {
	var metadataCalls, cdnCalls atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("GET /"+GraphVersion+"/MEDIA123", func(w http.ResponseWriter, r *http.Request) {
		metadataCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("metadata Authorization = %q", got)
		}
		_, _ = fmt.Fprintf(w, `{"url":%q,"mime_type":"image/jpeg","sha256":"abc","file_size":11,"id":"MEDIA123","messaging_product":"whatsapp"}`, server.URL+"/cdn/blob1")
	})
	mux.HandleFunc("GET /cdn/blob1", func(w http.ResponseWriter, r *http.Request) {
		cdnCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			// The lookaside CDN refuses unauthenticated downloads.
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "jpeg\x00bytes")
	})

	adapter := newTestAdapter(t, server.URL, nil)
	body, mime, err := adapter.Download(context.Background(), chat.AttachmentRef{Kind: "photo", ID: "MEDIA123"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q", mime)
	}
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "jpeg\x00bytes" {
		t.Fatalf("content = %q", content)
	}
	if metadataCalls.Load() != 1 || cdnCalls.Load() != 1 {
		t.Fatalf("calls = %d metadata / %d cdn, want 1/1", metadataCalls.Load(), cdnCalls.Load())
	}
}

func TestDownloadRefetchesExpiredURLOnce(t *testing.T) {
	var metadataCalls atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("GET /"+GraphVersion+"/MEDIA123", func(w http.ResponseWriter, r *http.Request) {
		// First metadata fetch hands out an already-expired URL; the
		// refetch hands out a fresh one.
		if metadataCalls.Add(1) == 1 {
			_, _ = fmt.Fprintf(w, `{"url":%q,"mime_type":"application/pdf"}`, server.URL+"/cdn/expired")
			return
		}
		_, _ = fmt.Fprintf(w, `{"url":%q,"mime_type":"application/pdf"}`, server.URL+"/cdn/fresh")
	})
	mux.HandleFunc("GET /cdn/expired", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "url expired", http.StatusNotFound)
	})
	mux.HandleFunc("GET /cdn/fresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pdf bytes")
	})

	adapter := newTestAdapter(t, server.URL, nil)
	body, mime, err := adapter.Download(context.Background(), chat.AttachmentRef{Kind: "document", ID: "MEDIA123"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	content, _ := io.ReadAll(body)
	if string(content) != "pdf bytes" || mime != "application/pdf" {
		t.Fatalf("content/mime = %q/%q", content, mime)
	}
	if metadataCalls.Load() != 2 {
		t.Fatalf("metadata fetched %d times, want exactly 2 (expiry refetch once)", metadataCalls.Load())
	}
}

func TestDownloadGivesUpAfterSecondFailure(t *testing.T) {
	var metadataCalls atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("GET /"+GraphVersion+"/MEDIA123", func(w http.ResponseWriter, r *http.Request) {
		metadataCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"url":%q,"mime_type":"image/jpeg"}`, server.URL+"/cdn/gone")
	})
	mux.HandleFunc("GET /cdn/gone", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})

	adapter := newTestAdapter(t, server.URL, nil)
	if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{Kind: "photo", ID: "MEDIA123"}); err == nil {
		t.Fatal("expected error after refetch-once failed")
	}
	if metadataCalls.Load() != 2 {
		t.Fatalf("metadata fetched %d times, want exactly 2 (refetch once, no loop)", metadataCalls.Load())
	}
}

func TestDownloadRejectsEmptyMediaID(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{Kind: "photo"}); err == nil {
		t.Fatal("expected error for missing media id")
	}
}
