package slack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/chat"
)

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{SigningSecret: testSecret}); err == nil || !strings.Contains(err.Error(), "Token") {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := New(Options{Token: testToken}); err == nil || !strings.Contains(err.Error(), "SigningSecret") {
		t.Fatalf("missing signing secret error = %v", err)
	}
}

func TestNewResolvesIdentityViaAuthTest(t *testing.T) {
	f := newFakeAPI(t)
	adapter, err := New(Options{Token: testToken, SigningSecret: testSecret, BaseURL: f.server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := f.callMethods(); len(got) != 1 || got[0] != "auth.test" {
		t.Fatalf("startup calls = %v, want [auth.test]", got)
	}
	if adapter.Account() != testBotUser {
		t.Fatalf("Account = %q, want %q", adapter.Account(), testBotUser)
	}
	if adapter.Platform() != "slack" {
		t.Fatalf("Platform = %q", adapter.Platform())
	}
}

func TestNewPreseededIdentitySkipsAuthTest(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	if got := len(f.callMethods()); got != 0 {
		t.Fatalf("startup calls = %d, want 0 with a pre-seeded BotUserID", got)
	}
	if adapter.Account() != testBotUser {
		t.Fatalf("Account = %q", adapter.Account())
	}
}

func TestNewSurfacesAuthTestFailure(t *testing.T) {
	f := newFakeAPI(t)
	f.stub("auth.test", stubResponse{body: `{"ok":false,"error":"invalid_auth"}`})
	_, err := New(Options{Token: testToken, SigningSecret: testSecret, BaseURL: f.server.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("err = %v, want invalid_auth surfaced", err)
	}
}

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

func TestTrustedSlackHost(t *testing.T) {
	tests := map[string]bool{
		"slack.com":              true,
		"files.slack.com":        true,
		"FILES.SLACK.COM.":       true,
		"slack.com.attacker.tld": false,
		"notslack.com":           false,
		"":                       false,
	}
	for host, want := range tests {
		if got := trustedSlackHost(host); got != want {
			t.Errorf("trustedSlackHost(%q) = %t, want %t", host, got, want)
		}
	}
	client := downloadClient(http.DefaultClient, testToken)
	via, _ := http.NewRequest(http.MethodGet, "https://files.slack.com/source", nil)
	external, _ := http.NewRequest(http.MethodGet, "https://attacker.example/target", nil)
	external.Header.Set("Authorization", "Bearer "+testToken)
	if err := client.CheckRedirect(external, []*http.Request{via}); err != nil {
		t.Fatalf("external CheckRedirect: %v", err)
	}
	if got := external.Header.Get("Authorization"); got != "" {
		t.Fatalf("external redirect Authorization = %q, want empty", got)
	}
	trusted, _ := http.NewRequest(http.MethodGet, "https://downloads.slack.com/target", nil)
	if err := client.CheckRedirect(trusted, []*http.Request{via}); err != nil {
		t.Fatalf("trusted CheckRedirect: %v", err)
	}
	if got, want := trusted.Header.Get("Authorization"), "Bearer "+testToken; got != want {
		t.Fatalf("trusted redirect Authorization = %q, want %q", got, want)
	}
}

func TestDownloadRejectsNonURLRef(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{ID: "F0FILE"}); err == nil {
		t.Fatal("Download accepted a bare file id")
	}
}

func TestDownloadNon200(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(files.Close)
	adapter := newTestAdapter(t, newFakeAPI(t))
	if _, _, err := adapter.Download(context.Background(), chat.AttachmentRef{ID: files.URL + "/x"}); err == nil {
		t.Fatal("Download succeeded on 404")
	}
}
