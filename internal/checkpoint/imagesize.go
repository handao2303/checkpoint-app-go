package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"

	// Registers the decoders used by DecodeThumbSize. Thumbnails are always JPEG
	// (validateMediaKeys enforces .jpg/.jpeg), but PNG and GIF cost nothing to
	// support and make the helper reusable for avatars.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// maxThumbHeaderBytes caps how much of a thumbnail is read to measure it.
// image.DecodeConfig only needs the header, and thumbnails are bounded to a
// 640px longest edge, so this is far more than enough — it exists to stop a
// malformed or hostile object from streaming unboundedly into memory.
const maxThumbHeaderBytes = 512 << 10 // 512 KiB

// DecodeThumbSize reports the pixel dimensions of an encoded image, reading only
// as much as the format's header requires.
func DecodeThumbSize(r io.Reader) (width, height int, err error) {
	cfg, _, err := image.DecodeConfig(io.LimitReader(r, maxThumbHeaderBytes))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("image reports non-positive size %dx%d", cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, nil
}

// decodeThumbSizeBytes is the in-memory form, used where the thumbnail was just
// generated and is already held as bytes.
func decodeThumbSizeBytes(b []byte) (width, height int, err error) {
	return DecodeThumbSize(bytes.NewReader(b))
}

// thumbSizeReader is the subset of object storage needed to measure a stored
// thumbnail (satisfied by *storage.R2).
type thumbSizeReader interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, string, error)
}

// MeasureStoredThumb downloads just enough of the thumbnail at key to read its
// dimensions. The body is closed early, so only the header is transferred.
func MeasureStoredThumb(ctx context.Context, s thumbSizeReader, key string) (width, height int, err error) {
	rc, _, err := s.GetObject(ctx, key)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch thumbnail %s: %w", key, err)
	}
	defer func() { _ = rc.Close() }()

	return DecodeThumbSize(rc)
}
