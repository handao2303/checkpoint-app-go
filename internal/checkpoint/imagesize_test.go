package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"
)

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeThumbSize(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{640, 360}, {360, 640}, {1, 1}} {
		got, gotH, err := decodeThumbSizeBytes(encodeJPEG(t, tc.w, tc.h))
		if err != nil {
			t.Fatalf("%dx%d: %v", tc.w, tc.h, err)
		}
		if got != tc.w || gotH != tc.h {
			t.Errorf("got %dx%d, want %dx%d", got, gotH, tc.w, tc.h)
		}
	}
}

func TestDecodeThumbSizeRejectsGarbage(t *testing.T) {
	if _, _, err := decodeThumbSizeBytes([]byte("not an image")); err == nil {
		t.Error("expected an error for non-image bytes")
	}
}

// stubStorage serves one object and records whether it was closed.
type stubStorage struct {
	body   []byte
	err    error
	closed bool
}

type trackingCloser struct {
	io.Reader
	onClose func()
}

func (t *trackingCloser) Close() error { t.onClose(); return nil }

func (s *stubStorage) GetObject(context.Context, string) (io.ReadCloser, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &trackingCloser{Reader: bytes.NewReader(s.body), onClose: func() { s.closed = true }}, "image/jpeg", nil
}

func TestMeasureStoredThumbClosesBody(t *testing.T) {
	st := &stubStorage{body: encodeJPEG(t, 640, 360)}
	w, h, err := MeasureStoredThumb(context.Background(), st, "checkpoints/x/thumbnails/a.jpg")
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if w != 640 || h != 360 {
		t.Errorf("got %dx%d, want 640x360", w, h)
	}
	if !st.closed {
		t.Error("body was not closed — the R2 connection would leak")
	}
}

func TestMeasureStoredThumbPropagatesFetchError(t *testing.T) {
	st := &stubStorage{err: errors.New("object missing")}
	if _, _, err := MeasureStoredThumb(context.Background(), st, "k"); err == nil {
		t.Error("expected fetch error to propagate")
	}
}
