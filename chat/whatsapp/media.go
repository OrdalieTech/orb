package whatsapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/OrdalieTech/pigo/chat"
)

// mediaInfo is the Graph media-metadata subset the gateway reads.
type mediaInfo struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// fetchMediaInfo resolves a media id to its (short-lived) download URL.
func (a *Adapter) fetchMediaInfo(ctx context.Context, mediaID string) (mediaInfo, error) {
	var info mediaInfo
	path := "/" + GraphVersion + "/" + url.PathEscape(mediaID)
	if err := a.do(ctx, http.MethodGet, path, nil, &info); err != nil {
		return mediaInfo{}, err
	}
	if info.URL == "" {
		return mediaInfo{}, fmt.Errorf("whatsapp: media %s metadata has no url", mediaID)
	}
	return info, nil
}

// fetchMediaURL downloads the media content with the Bearer token (the
// lookaside CDN refuses unauthenticated requests).
func (a *Adapter) fetchMediaURL(ctx context.Context, mediaURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: build media request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.opts.Token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download media: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("whatsapp: media download returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// Download implements chat.Adapter: media id → metadata (url) → authenticated
// download. Media URLs expire after ~5 minutes, so a failed download refetches
// the metadata once for a fresh URL before giving up.
func (a *Adapter) Download(ctx context.Context, ref chat.AttachmentRef) (io.ReadCloser, string, error) {
	if ref.ID == "" {
		return nil, "", fmt.Errorf("whatsapp: attachment has no media id")
	}
	info, err := a.fetchMediaInfo(ctx, ref.ID)
	if err != nil {
		return nil, "", err
	}
	body, err := a.fetchMediaURL(ctx, info.URL)
	if err != nil {
		fresh, refetchErr := a.fetchMediaInfo(ctx, ref.ID)
		if refetchErr != nil {
			return nil, "", fmt.Errorf("whatsapp: media refetch after expired url: %w (first attempt: %s)", refetchErr, err)
		}
		info = fresh
		body, err = a.fetchMediaURL(ctx, info.URL)
		if err != nil {
			return nil, "", err
		}
	}
	mime := info.MimeType
	if mime == "" {
		mime = ref.MIME
	}
	return body, mime, nil
}
