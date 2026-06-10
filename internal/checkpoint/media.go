package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
)

// imageExt maps a supported image content type to a file extension, or "" if
// unsupported.
func imageExt(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}

// randHex returns n random bytes hex-encoded, for object keys.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
