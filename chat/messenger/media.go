package messenger

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/OrdalieTech/pigo/chat"
)

// Download implements chat.Adapter. Messenger attachment refs carry the
// direct CDN URL from the webhook payload (there is no media-id hop as on
// WhatsApp): the download is a plain unauthenticated GET, deliberately sent
// without the page token so it never leaks to Meta's CDN. The URLs expire
// and cannot be refreshed, so attachments must be downloaded promptly after
// ingress.
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if ref.ID == "" {
		return nil, "", fmt.Errorf("messenger: attachment has no url")
	}
	parsed, err := url.Parse(ref.ID)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, "", fmt.Errorf("messenger: attachment ref is not an http(s) url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.ID, nil)
	if err != nil {
		return nil, "", fmt.Errorf("messenger: build media request: %w", err)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("messenger: download media: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("messenger: media download returned HTTP %d (attachment urls expire; download promptly)", resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = ref.MIME
	}
	return resp.Body, mime, nil
}
