// Package graphhook serves the Meta Graph webhook endpoint shared by the
// Cloud API chat adapters (WhatsApp, Messenger): the one-time hub.challenge
// subscribe handshake and the X-Hub-Signature-256 raw-body HMAC check, both
// compared in constant time via hmac.Equal.
package graphhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const maxBody = 5 << 20

// Handler serves a Graph webhook. GET echoes the raw hub.challenge with a
// 200 iff hub.mode is "subscribe" and hub.verify_token matches verifyToken,
// else 403. POST checks header = "sha256=" + hex(HMAC-SHA256(body,
// appSecret)) over the raw body BEFORE parsing (Meta signs the exact bytes
// sent), decodes it into a T and hands it to handle; a handle error replies
// 500 so Meta redelivers.
func Handler[T any](verifyToken, appSecret string, handle func(*T) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			query := r.URL.Query()
			if query.Get("hub.mode") != "subscribe" || !hmac.Equal([]byte(query.Get("hub.verify_token")), []byte(verifyToken)) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(query.Get("hub.challenge")))
		case http.MethodPost:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				http.Error(w, "unreadable body", http.StatusBadRequest)
				return
			}
			signature, ok := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
			mac := hmac.New(sha256.New, []byte(appSecret))
			mac.Write(body)
			if !ok || !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature)) {
				http.Error(w, "invalid signature", http.StatusForbidden)
				return
			}
			var payload T
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "malformed payload", http.StatusBadRequest)
				return
			}
			if err := handle(&payload); err != nil {
				http.Error(w, "publish failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
