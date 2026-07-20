// Package graphhook holds the Meta Graph webhook plumbing shared by the
// Cloud API chat adapters (WhatsApp today, Messenger next): the one-time
// hub.challenge subscribe handshake and the X-Hub-Signature-256 raw-body
// HMAC check. Both compare in constant time via hmac.Equal.
package graphhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// HandleVerify answers the subscribe handshake: it echoes the raw
// hub.challenge with a 200 iff hub.mode is "subscribe" and hub.verify_token
// matches verifyToken (constant time), and replies 403 otherwise.
func HandleVerify(w http.ResponseWriter, r *http.Request, verifyToken string) {
	query := r.URL.Query()
	mode := query.Get("hub.mode")
	token := query.Get("hub.verify_token")
	if mode != "subscribe" || !hmac.Equal([]byte(token), []byte(verifyToken)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(query.Get("hub.challenge")))
}

// ValidSignature checks header = "sha256=" + hex(HMAC-SHA256(body, secret))
// in constant time, as Meta signs every webhook POST over the raw body.
func ValidSignature(header string, body []byte, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(header[len(prefix):]))
}
