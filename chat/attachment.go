package chat

import "strings"

// AttachmentKind maps a MIME type to an [AttachmentRef] Kind: image/* is
// "photo", audio/* "audio", video/* "video", and anything else "document".
func AttachmentKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	}
	return "document"
}
