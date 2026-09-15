package checkpoint

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	"golang.org/x/image/draw"
)

// Social-card geometry. Facebook and Messenger render a large preview from a
// 1.91:1 image and recommend 1200x630; below roughly 600px wide they fall back
// to a small square card instead.
const (
	OGCardWidth  = 1200
	OGCardHeight = 630

	// minOGSourceWidth is the narrowest original worth rendering a card from.
	// Anything smaller would have to be upscaled, which invents detail and looks
	// worse than the small card it is trying to avoid — those rows keep using
	// their thumbnail.
	minOGSourceWidth = 600

	// ogJPEGQuality trades a little size for a card that still looks clean at
	// full width in a chat bubble.
	ogJPEGQuality = 85

	// maxOGSourceBytes bounds how much of an original is read into memory.
	// Uploads are already limited well below this; it guards against a corrupt
	// or hostile object.
	maxOGSourceBytes = 24 << 20 // 24 MiB
)

// ErrSourceTooSmall means the original cannot yield a card without upscaling.
var ErrSourceTooSmall = fmt.Errorf("source narrower than %dpx", minOGSourceWidth)

// RenderOGCard turns an original photo into a 1200x630 JPEG social card.
//
// The source is centre-cropped to 1.91:1 before scaling, so the card fills the
// whole frame with no letterboxing — on the portrait photos that make up most
// of the corpus this keeps the middle of the shot, which is where the subject
// almost always is. Scaling uses CatmullRom: these are large downscales, and a
// cheaper kernel leaves visible aliasing on foliage and text.
//
// Returns ErrSourceTooSmall when the original is too small to crop from.
func RenderOGCard(r io.Reader) (jpegBytes []byte, width, height int, err error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxOGSourceBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read source: %w", err)
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode source: %w", err)
	}

	b := src.Bounds()
	if b.Dx() < minOGSourceWidth {
		return nil, 0, 0, ErrSourceTooSmall
	}

	dst := image.NewRGBA(image.Rect(0, 0, OGCardWidth, OGCardHeight))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, centreCrop(b), draw.Src, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: ogJPEGQuality}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode card: %w", err)
	}
	return out.Bytes(), OGCardWidth, OGCardHeight, nil
}

// centreCrop returns the largest 1.91:1 rectangle centred inside b.
func centreCrop(b image.Rectangle) image.Rectangle {
	w, h := b.Dx(), b.Dy()

	// Target width for the full height, versus target height for the full width:
	// whichever fits inside the source is the crop that wastes no pixels.
	cropW, cropH := w, w*OGCardHeight/OGCardWidth
	if cropH > h {
		cropH = h
		cropW = h * OGCardWidth / OGCardHeight
	}

	offX := b.Min.X + (w-cropW)/2
	offY := b.Min.Y + (h-cropH)/2
	return image.Rect(offX, offY, offX+cropW, offY+cropH)
}

// ogCardKeyFor derives a card key alongside the media it came from,
// e.g. checkpoints/<prefix>/<rand>.jpg → checkpoints/<prefix>/og/<rand>.jpg.
func ogCardKeyFor(objectKey string) string {
	return siblingKey(objectKey, "og", ".jpg")
}
