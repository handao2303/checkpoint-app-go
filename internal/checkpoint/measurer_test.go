package checkpoint

import (
	"context"
	"errors"
	"testing"
)

// fakeMeasurerRepo is an in-memory stand-in for the media rows.
type fakeMeasurerRepo struct {
	rows     []UnmeasuredThumb
	measured map[string]ThumbSize
	pages    int
}

func (f *fakeMeasurerRepo) UnmeasuredThumbsForCheckpoint(context.Context, string) ([]UnmeasuredThumb, error) {
	return f.rows, nil
}

// ThumbnailsMissingDimensions pages over rows not yet measured, mirroring the
// real query: measured rows fall out of the scan.
func (f *fakeMeasurerRepo) ThumbnailsMissingDimensions(_ context.Context, afterID string, limit int) ([]UnmeasuredThumb, error) {
	f.pages++
	var out []UnmeasuredThumb
	for _, r := range f.rows {
		if r.ImageID <= afterID {
			continue
		}
		if _, done := f.measured[r.ImageID]; done {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeMeasurerRepo) SetThumbDimensions(_ context.Context, imageID string, w, h int) error {
	f.measured[imageID] = ThumbSize{Width: w, Height: h}
	return nil
}

// sizedStorage serves a distinct JPEG per key; keys absent from sizes fail.
type sizedStorage struct {
	t     *testing.T
	sizes map[string][2]int
}

func (s *sizedStorage) GetObject(_ context.Context, key string) (readCloser, string, error) {
	wh, ok := s.sizes[key]
	if !ok {
		return nil, "", errors.New("no such object: " + key)
	}
	return newReadCloser(encodeJPEG(s.t, wh[0], wh[1])), "image/jpeg", nil
}

func TestBackfillMeasuresEveryRowAcrossPages(t *testing.T) {
	repo := &fakeMeasurerRepo{measured: map[string]ThumbSize{}}
	store := &sizedStorage{t: t, sizes: map[string][2]int{}}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		key := "thumbs/" + id + ".jpg"
		repo.rows = append(repo.rows, UnmeasuredThumb{ImageID: id, ThumbnailKey: key})
		store.sizes[key] = [2]int{640, 360}
	}

	stats, err := NewMeasurer(repo, store).Backfill(context.Background(), 2, 0, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Scanned != 5 || stats.Measured != 5 || stats.Failed != 0 {
		t.Errorf("got %+v, want scanned=5 measured=5 failed=0", stats)
	}
	if len(repo.measured) != 5 {
		t.Errorf("persisted %d rows, want 5", len(repo.measured))
	}
	if repo.pages < 3 {
		t.Errorf("expected paging with batch=2, got %d page(s)", repo.pages)
	}
}

func TestBackfillSkipsUnreadableRowsAndKeepsGoing(t *testing.T) {
	repo := &fakeMeasurerRepo{measured: map[string]ThumbSize{}}
	store := &sizedStorage{t: t, sizes: map[string][2]int{
		"thumbs/a.jpg": {640, 360},
		// b is missing from storage — a deleted object must not abort the run.
		"thumbs/c.jpg": {800, 600},
	}}
	for _, id := range []string{"a", "b", "c"} {
		repo.rows = append(repo.rows, UnmeasuredThumb{ImageID: id, ThumbnailKey: "thumbs/" + id + ".jpg"})
	}

	stats, err := NewMeasurer(repo, store).Backfill(context.Background(), 10, 0, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Measured != 2 || stats.Failed != 1 {
		t.Errorf("got %+v, want measured=2 failed=1", stats)
	}
	if got := repo.measured["c"]; got.Width != 800 || got.Height != 600 {
		t.Errorf("row after the failure was not measured: %+v", got)
	}
}

func TestBackfillHonoursLimit(t *testing.T) {
	repo := &fakeMeasurerRepo{measured: map[string]ThumbSize{}}
	store := &sizedStorage{t: t, sizes: map[string][2]int{}}
	for _, id := range []string{"a", "b", "c", "d"} {
		key := "thumbs/" + id + ".jpg"
		repo.rows = append(repo.rows, UnmeasuredThumb{ImageID: id, ThumbnailKey: key})
		store.sizes[key] = [2]int{100, 100}
	}

	stats, err := NewMeasurer(repo, store).Backfill(context.Background(), 10, 2, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Scanned != 2 {
		t.Errorf("scanned %d, want 2 (limit)", stats.Scanned)
	}
}

func TestMeasureCheckpointReturnsSizesByImageID(t *testing.T) {
	repo := &fakeMeasurerRepo{
		measured: map[string]ThumbSize{},
		rows:     []UnmeasuredThumb{{ImageID: "img-1", ThumbnailKey: "thumbs/a.jpg"}},
	}
	store := &sizedStorage{t: t, sizes: map[string][2]int{"thumbs/a.jpg": {640, 360}}}

	sizes, err := NewMeasurer(repo, store).MeasureCheckpoint(context.Background(), "cp-1")
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if got := sizes["img-1"]; got.Width != 640 || got.Height != 360 {
		t.Errorf("got %+v, want 640x360", got)
	}
	if _, persisted := repo.measured["img-1"]; !persisted {
		t.Error("size was not persisted")
	}
}
