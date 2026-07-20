package graphhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func verify(t *testing.T, mode, token, challenge string) *httptest.ResponseRecorder {
	t.Helper()
	query := url.Values{}
	query.Set("hub.mode", mode)
	query.Set("hub.verify_token", token)
	query.Set("hub.challenge", challenge)
	req := httptest.NewRequest(http.MethodGet, "/webhook?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	HandleVerify(rec, req, "verify-token")
	return rec
}

func TestHandleVerify(t *testing.T) {
	t.Run("good handshake echoes raw challenge", func(t *testing.T) {
		rec := verify(t, "subscribe", "verify-token", "1158201444")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "1158201444" {
			t.Fatalf("body = %q, want raw challenge", rec.Body.String())
		}
	})
	t.Run("wrong verify_token is 403", func(t *testing.T) {
		if rec := verify(t, "subscribe", "wrong", "123"); rec.Code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403", rec.Code)
		}
	})
	t.Run("wrong mode is 403", func(t *testing.T) {
		if rec := verify(t, "unsubscribe", "verify-token", "123"); rec.Code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403", rec.Code)
		}
	})
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidSignature(t *testing.T) {
	const secret = "app-secret"
	body := []byte(`{"object":"whatsapp_business_account"}`)

	if !ValidSignature(sign(secret, body), body, secret) {
		t.Fatal("valid signature rejected")
	}
	if ValidSignature(sign("other-secret", body), body, secret) {
		t.Fatal("signature under wrong secret accepted")
	}
	if ValidSignature(sign(secret, []byte(`tampered`)), body, secret) {
		t.Fatal("signature over different body accepted")
	}
	if ValidSignature("", body, secret) {
		t.Fatal("empty header accepted")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if ValidSignature(hex.EncodeToString(mac.Sum(nil)), body, secret) {
		t.Fatal("header without sha256= prefix accepted")
	}
}
