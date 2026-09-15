package checkpoint

import (
	"bytes"
	"errors"
	"image"
	"testing"
)

func TestRenderOGCardAlwaysProducesCardGeometry(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"portrait phone photo", 1440, 2560},
		{"landscape photo", 4032, 3024},
		{"already 1.91:1", 1200, 630},
		{"exactly at the floor", minOGSourceWidth, 400},
		{"very wide panorama", 4000, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card, w, h, err := RenderOGCard(bytes.NewReader(encodeJPEG(t, tc.w, tc.h)))
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if w != OGCardWidth || h != OGCardHeight {
				t.Errorf("reported %dx%d, want %dx%d", w, h, OGCardWidth, OGCardHeight)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(card))
			if err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if cfg.Width != OGCardWidth || cfg.Height != OGCardHeight {
				t.Errorf("encoded %dx%d, want %dx%d", cfg.Width, cfg.Height, OGCardWidth, OGCardHeight)
			}
		})
	}
}

// Upscaling a small original would invent detail and look worse than the small
// card it is trying to avoid, so it must be refused rather than attempted.
func TestRenderOGCardRefusesSmallSources(t *testing.T) {
	for _, w := range []int{390, 428, minOGSourceWidth - 1} {
		_, _, _, err := RenderOGCard(bytes.NewReader(encodeJPEG(t, w, 900)))
		if !errors.Is(err, ErrSourceTooSmall) {
			t.Errorf("width %d: got %v, want ErrSourceTooSmall", w, err)
		}
	}
}

func TestRenderOGCardRejectsGarbage(t *testing.T) {
	if _, _, _, err := RenderOGCard(bytes.NewReader([]byte("nope"))); err == nil {
		t.Error("expected a decode error")
	}
}

func TestCentreCropIsCentredAndCorrectRatio(t *testing.T) {
	for _, b := range []image.Rectangle{
		image.Rect(0, 0, 1440, 2560),
		image.Rect(0, 0, 4032, 3024),
		image.Rect(0, 0, 4000, 500),
	} {
		got := centreCrop(b)

		if got.Dx() > b.Dx() || got.Dy() > b.Dy() {
			t.Errorf("%v: crop %v escapes the source", b, got)
		}
		// Ratio within a pixel of 1.91:1 (integer division rounds).
		want := float64(OGCardWidth) / float64(OGCardHeight)
		ratio := float64(got.Dx()) / float64(got.Dy())
		if diff := ratio - want; diff > 0.01 || diff < -0.01 {
			t.Errorf("%v: crop %v ratio %.3f, want %.3f", b, got, ratio, want)
		}
		// Centred: the margins on opposite sides differ by at most one pixel.
		if l, r := got.Min.X-b.Min.X, b.Max.X-got.Max.X; abs(l-r) > 1 {
			t.Errorf("%v: horizontal margins %d vs %d", b, l, r)
		}
		if tp, bt := got.Min.Y-b.Min.Y, b.Max.Y-got.Max.Y; abs(tp-bt) > 1 {
			t.Errorf("%v: vertical margins %d vs %d", b, tp, bt)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestOGCardKeySitsBesideItsSource(t *testing.T) {
	got := ogCardKeyFor("checkpoints/abc123/photo.jpg")
	if want := "checkpoints/abc123/og/"; len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("got %q, want it under %q", got, want)
	}
	if got[len(got)-4:] != ".jpg" {
		t.Errorf("got %q, want a .jpg suffix", got)
	}
}
