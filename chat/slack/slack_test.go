package slack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OrdalieTech/orb/chat"
)

func TestDownloadDoesNotSendAuthToNonSlackHosts(t *testing.T) {
	// Both hops are outside slack.com, so neither the caller-provided URL nor
	// its redirect target may receive the bot token.
	var fileHostAuth, cdnHostAuth string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnHostAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("PDFDATA"))
	}))
	t.Cleanup(cdn.Close)
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileHostAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, cdn.URL+"/real/report.pdf", http.StatusFound)
	}))
	t.Cleanup(files.Close)

	adapter := newTestAdapter(t, newFakeAPI(t))
	ref := chat.AttachmentRef{Kind: "document", ID: files.URL + "/files-pri/T0-F0/download/report.pdf", MIME: "application/pdf"}
	body, mime, err := adapter.Download(context.Background(), ref)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(content) != "PDFDATA" {
		t.Fatalf("content = %q", content)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime = %q", mime)
	}
	if fileHostAuth != "" {
		t.Fatalf("first hop Authorization = %q", fileHostAuth)
	}
	if cdnHostAuth != "" {
		t.Fatalf("redirected hop Authorization = %q", cdnHostAuth)
	}
}
