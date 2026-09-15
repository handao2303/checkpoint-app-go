package share

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
)

func ptr[T any](v T) *T { return &v }

// detailWithThumb builds a checkpoint whose cover is the given image's thumbnail.
func detailWithThumb(thumbURL string, w, h *int) *checkpoint.Detail {
	return &checkpoint.Detail{
		Checkpoint: &checkpoint.Checkpoint{
			ID:           "cp-1",
			Name:         "Vung Tau Beach",
			ThumbnailURL: &thumbURL,
		},
		Author: &checkpoint.Author{Username: "nva"},
		Images: []checkpoint.Image{{
			ID:           "img-1",
			URL:          "https://r2.dev/full.jpg",
			ThumbnailURL: &thumbURL,
			ThumbWidth:   w,
			ThumbHeight:  h,
		}},
	}
}

// recordingMeasurer reports a size and counts how often it was asked.
type recordingMeasurer struct {
	size  checkpoint.ThumbSize
	err   error
	calls int
}

func (m *recordingMeasurer) MeasureCheckpoint(context.Context, string) (map[string]checkpoint.ThumbSize, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return map[string]checkpoint.ThumbSize{"img-1": m.size}, nil
}

func TestCheckpointMetaUsesRecordedDimensions(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", ptr(640), ptr(360))
	m := &recordingMeasurer{}
	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).WithThumbMeasurer(m)

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.ImageWidth != 640 || meta.ImageHeight != 360 {
		t.Errorf("got %dx%d, want 640x360", meta.ImageWidth, meta.ImageHeight)
	}
	if m.calls != 0 {
		t.Errorf("measured %d time(s) despite recorded dimensions", m.calls)
	}
}

func TestCheckpointMetaMeasuresWhenDimensionsMissing(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", nil, nil)
	m := &recordingMeasurer{size: checkpoint.ThumbSize{Width: 800, Height: 600}}
	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).WithThumbMeasurer(m)

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.ImageWidth != 800 || meta.ImageHeight != 600 {
		t.Errorf("got %dx%d, want 800x600", meta.ImageWidth, meta.ImageHeight)
	}
	if m.calls != 1 {
		t.Errorf("measured %d time(s), want 1", m.calls)
	}
}

// A failure to measure must still produce a usable card — just without the tags.
func TestCheckpointMetaSurvivesMeasureFailure(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", nil, nil)
	m := &recordingMeasurer{err: errors.New("r2 unreachable")}
	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).WithThumbMeasurer(m)

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("share card should not fail when measuring fails: %v", err)
	}
	if meta.ImageWidth != 0 || meta.ImageHeight != 0 {
		t.Errorf("got %dx%d, want 0x0", meta.ImageWidth, meta.ImageHeight)
	}
	if meta.Image == "" {
		t.Error("card lost its image")
	}
}

// No measurer configured (and no recorded size) must not panic.
func TestCheckpointMetaWithoutMeasurer(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", nil, nil)
	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{})

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.ImageWidth != 0 || meta.ImageHeight != 0 {
		t.Errorf("got %dx%d, want 0x0", meta.ImageWidth, meta.ImageHeight)
	}
}

func TestTemplateEmitsDimensionTagsOnlyWhenKnown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		meta      MetaData
		wantTags  bool
		wantWidth string
	}{
		{"known", MetaData{Image: "https://r2.dev/t.jpg", ImageWidth: 640, ImageHeight: 360}, true, "640"},
		{"unknown", MetaData{Image: "https://r2.dev/t.jpg"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			if err := shareTemplate.Execute(&sb, &tc.meta); err != nil {
				t.Fatalf("render: %v", err)
			}
			got := sb.String()
			hasTags := strings.Contains(got, "og:image:width")
			if hasTags != tc.wantTags {
				t.Fatalf("og:image:width present=%v, want %v\n%s", hasTags, tc.wantTags, got)
			}
			if tc.wantTags && !strings.Contains(got, `content="`+tc.wantWidth+`"`) {
				t.Errorf("width %s missing:\n%s", tc.wantWidth, got)
			}
		})
	}
}
