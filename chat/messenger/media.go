package messenger

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/internal/httpjson"
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
	resp, failed, err := httpjson.Get(ctx, a.client, ref.ID)
	if err != nil {
		return nil, "", fmt.Errorf("messenger: download media: %w", err)
	}
	if failed != nil {
		return nil, "", fmt.Errorf("messenger: media download returned HTTP %d (attachment urls expire; download promptly)", failed.Status)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = ref.MIME
	}
	return resp.Body, mime, nil
}
