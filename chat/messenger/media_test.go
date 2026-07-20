package messenger

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OrdalieTech/pi-go/chat"
)

func TestDownloadPlainUnauthenticatedGET(t *testing.T) {
	var gotAuth *string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		gotAuth = &auth
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	}))
	t.Cleanup(cdn.Close)

	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	body, mime, err := adapter.Download(context.Background(), chat.AttachmentRef{
		Kind: "photo",
		ID:   cdn.URL + "/v/t1/img.jpg?expires=123",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if gotAuth == nil || *gotAuth != "" {
		t.Fatalf("CDN download carried Authorization %v; must be a plain unauthenticated GET", gotAuth)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q", mime)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "jpeg-bytes" {
		t.Fatalf("body = %q", data)
	}
}

func TestDownloadExpiredURLSurfacesError(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "url signature expired", http.StatusForbidden)
	}))
	t.Cleanup(cdn.Close)

	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{Kind: "photo", ID: cdn.URL + "/img.jpg"}); err == nil {
		t.Fatal("expired CDN url did not surface an error")
	}
}

func TestDownloadRejectsNonHTTPRefs(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	for _, id := range []string{"", "not a url", "ftp://example.com/x", "file:///etc/passwd"} {
		if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{ID: id}); err == nil {
			t.Fatalf("Download accepted ref id %q", id)
		}
	}
}
