package checkpoint

import (
	"context"
	"fmt"
	"log"
)

// measurerRepo is the data layer the Measurer needs (consumer-side interface,
// satisfied by *Repository).
type measurerRepo interface {
	UnmeasuredThumbsForCheckpoint(ctx context.Context, checkpointID string) ([]UnmeasuredThumb, error)
	ThumbnailsMissingDimensions(ctx context.Context, afterID string, limit int) ([]UnmeasuredThumb, error)
	SetThumbDimensions(ctx context.Context, imageID string, width, height int) error
}

// ThumbSize is a measured thumbnail's pixel dimensions.
type ThumbSize struct {
	Width  int
	Height int
}

// Measurer fills in thumbnail dimensions that were never recorded: everything
// uploaded before the columns existed, plus image thumbnails, which clients
// generate and upload directly so the server never sees their bytes.
//
// Video thumbnails do not need it — the worker measures those as it makes them.
type Measurer struct {
	repo    measurerRepo
	storage thumbSizeReader
}

// NewMeasurer builds a thumbnail measurer.
func NewMeasurer(repo measurerRepo, storage thumbSizeReader) *Measurer {
	return &Measurer{repo: repo, storage: storage}
}

// MeasureCheckpoint measures and persists any unmeasured thumbnails belonging to
// one checkpoint, returning the sizes by image id. Used on the share path, where
// a card needs og:image:width/height for a checkpoint the backfill has not
// reached (or one whose media arrived since).
//
// Best-effort per image: a thumbnail that cannot be fetched or decoded is logged
// and skipped, leaving the row unmeasured rather than failing the share page.
func (m *Measurer) MeasureCheckpoint(ctx context.Context, checkpointID string) (map[string]ThumbSize, error) {
	pending, err := m.repo.UnmeasuredThumbsForCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	sizes := make(map[string]ThumbSize, len(pending))
	for _, t := range pending {
		size, err := m.measure(ctx, t)
		if err != nil {
			log.Printf("checkpoint: measure thumbnail %s (image %s): %v", t.ThumbnailKey, t.ImageID, err)
			continue
		}
		sizes[t.ImageID] = size
	}
	return sizes, nil
}

// BackfillStats reports what a Backfill run did.
type BackfillStats struct {
	Scanned  int
	Measured int
	Failed   int
}

// Backfill measures every thumbnail with no recorded size, paging by id. It
// stops at the end of the table, when ctx is cancelled, or — if limit > 0 —
// after that many rows have been scanned.
//
// Safe to re-run: measured rows drop out of the scan, so a second run only
// retries whatever failed.
func (m *Measurer) Backfill(ctx context.Context, batchSize, limit int, onProgress func(BackfillStats)) (BackfillStats, error) {
	if batchSize <= 0 {
		batchSize = 200
	}

	var stats BackfillStats
	var afterID string
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}

		size := batchSize
		if limit > 0 && limit-stats.Scanned < size {
			size = limit - stats.Scanned
		}
		if size <= 0 {
			return stats, nil
		}

		batch, err := m.repo.ThumbnailsMissingDimensions(ctx, afterID, size)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			return stats, nil
		}

		for _, t := range batch {
			stats.Scanned++
			afterID = t.ImageID

			if _, err := m.measure(ctx, t); err != nil {
				stats.Failed++
				log.Printf("backfill: image %s (%s): %v", t.ImageID, t.ThumbnailKey, err)
				continue
			}
			stats.Measured++
		}

		if onProgress != nil {
			onProgress(stats)
		}
	}
}

// measure downloads the thumbnail's header, decodes its size and persists it.
func (m *Measurer) measure(ctx context.Context, t UnmeasuredThumb) (ThumbSize, error) {
	width, height, err := MeasureStoredThumb(ctx, m.storage, t.ThumbnailKey)
	if err != nil {
		return ThumbSize{}, err
	}
	if err := m.repo.SetThumbDimensions(ctx, t.ImageID, width, height); err != nil {
		return ThumbSize{}, fmt.Errorf("persist size: %w", err)
	}
	return ThumbSize{Width: width, Height: height}, nil
}
