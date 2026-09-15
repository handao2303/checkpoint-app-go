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

// stubCards returns a prebuilt card for img-1.
type stubCards struct {
	card  checkpoint.OGCard
	err   error
	calls int
}

func (c *stubCards) EnsureForCheckpoint(context.Context, string) (map[string]checkpoint.OGCard, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	if c.card.Key == "" {
		return nil, nil // nothing could be built (e.g. original too small)
	}
	return map[string]checkpoint.OGCard{"img-1": c.card}, nil
}

func TestCheckpointMetaPrefersExistingSocialCard(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", ptr(480), ptr(640))
	detail.Images[0].OGImageURL = ptr("https://media.test/og/card.jpg")
	detail.Images[0].OGWidth, detail.Images[0].OGHeight = ptr(1200), ptr(630)

	cards := &stubCards{}
	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).
		WithThumbMeasurer(&recordingMeasurer{}).
		WithCardBuilder(cards, "https://media.test")

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Image != "https://media.test/og/card.jpg" {
		t.Errorf("used %q, want the social card", meta.Image)
	}
	if meta.ImageWidth != 1200 || meta.ImageHeight != 630 {
		t.Errorf("got %dx%d, want 1200x630", meta.ImageWidth, meta.ImageHeight)
	}
	if cards.calls != 0 {
		t.Errorf("rebuilt the card %d time(s) despite one existing", cards.calls)
	}
}

func TestCheckpointMetaBuildsSocialCardOnDemand(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", ptr(480), ptr(640))
	cards := &stubCards{card: checkpoint.OGCard{Key: "checkpoints/x/og/new.jpg", Width: 1200, Height: 630}}

	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).
		WithThumbMeasurer(&recordingMeasurer{}).
		WithCardBuilder(cards, "https://media.test")

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Image != "https://media.test/checkpoints/x/og/new.jpg" {
		t.Errorf("used %q, want the freshly built card URL", meta.Image)
	}
	if meta.ImageWidth != 1200 || meta.ImageHeight != 630 {
		t.Errorf("got %dx%d, want 1200x630", meta.ImageWidth, meta.ImageHeight)
	}
	if cards.calls != 1 {
		t.Errorf("built %d time(s), want 1", cards.calls)
	}
}

// When no card can be made (original too small), the card must fall back to the
// thumbnail rather than losing its image.
func TestCheckpointMetaFallsBackToThumbnailWhenNoCardPossible(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", ptr(390), ptr(520))
	cards := &stubCards{} // builds nothing

	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).
		WithThumbMeasurer(&recordingMeasurer{}).
		WithCardBuilder(cards, "https://media.test")

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Image != "https://r2.dev/thumb.jpg" {
		t.Errorf("used %q, want the thumbnail fallback", meta.Image)
	}
	if meta.ImageWidth != 390 || meta.ImageHeight != 520 {
		t.Errorf("got %dx%d, want the thumbnail's 390x520", meta.ImageWidth, meta.ImageHeight)
	}
}

// A card-builder failure must not cost the card its image.
func TestCheckpointMetaSurvivesCardBuildFailure(t *testing.T) {
	detail := detailWithThumb("https://r2.dev/thumb.jpg", ptr(640), ptr(360))
	cards := &stubCards{err: errors.New("r2 down")}

	svc := NewService(&mockCheckpointService{detail: detail}, &mockJourneyService{},
		&mockIdentityService{}, &mockPlaceService{}).
		WithThumbMeasurer(&recordingMeasurer{}).
		WithCardBuilder(cards, "https://media.test")

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("share card should survive a build failure: %v", err)
	}
	if meta.Image != "https://r2.dev/thumb.jpg" {
		t.Errorf("used %q, want the thumbnail fallback", meta.Image)
	}
}
