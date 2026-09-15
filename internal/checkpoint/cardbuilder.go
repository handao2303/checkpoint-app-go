package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
)

// cardRepo is the data layer the CardBuilder needs (satisfied by *Repository).
type cardRepo interface {
	OGCardsPending(ctx context.Context, afterID string, limit int) ([]PendingOGCard, error)
	OGCardsPendingForCheckpoint(ctx context.Context, checkpointID string) ([]PendingOGCard, error)
	SetOGCard(ctx context.Context, imageID, ogKey string, width, height int) error
}

// cardStorage is the object storage the CardBuilder needs (satisfied by *storage.R2).
type cardStorage interface {
	thumbSizeReader
	Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error)
}

// OGCard is a rendered social card's location and size.
type OGCard struct {
	Key    string
	Width  int
	Height int
}

// CardBuilder renders 1200x630 social cards from checkpoint originals and
// stores them alongside the media.
//
// Thumbnails cannot serve this role: they are capped at a 640px longest edge
// for the feed, so portrait photos come out 360-480px wide — under the ~600px
// Messenger needs before it will render a large card. Rather than make every
// feed image heavier, the card is a separate object generated only for sharing.
type CardBuilder struct {
	repo    cardRepo
	storage cardStorage
}

// NewCardBuilder builds a social-card renderer.
func NewCardBuilder(repo cardRepo, storage cardStorage) *CardBuilder {
	return &CardBuilder{repo: repo, storage: storage}
}

// EnsureForCheckpoint renders cards for one checkpoint's media that lacks them,
// returning what it produced keyed by image id. Used on the share path so a
// checkpoint shared before the backfill reached it still gets a proper card.
//
// Best-effort per image: an original that cannot be fetched, decoded, or is too
// small to crop from is logged and skipped, leaving the row without a card so
// the share page falls back to the thumbnail.
func (b *CardBuilder) EnsureForCheckpoint(ctx context.Context, checkpointID string) (map[string]OGCard, error) {
	pending, err := b.repo.OGCardsPendingForCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	cards := make(map[string]OGCard, len(pending))
	for _, p := range pending {
		card, err := b.build(ctx, p)
		if err != nil {
			logCardSkip(p, err)
			continue
		}
		cards[p.ImageID] = card
	}
	return cards, nil
}

// CardBackfillStats reports what a BackfillCards run did.
type CardBackfillStats struct {
	Scanned int
	Built   int
	Skipped int // original too small to crop a card from
	Failed  int
}

// BackfillCards renders a card for every media row that lacks one, paging by id.
// It stops at the end of the table, when ctx is cancelled, or — if limit > 0 —
// after that many rows have been scanned.
//
// Rows whose original is too small are counted as skipped and left alone, so a
// re-run retries them; that is harmless but means the scan never empties. Use
// the reported counts, not "zero pending", to judge completion.
func (b *CardBuilder) BackfillCards(ctx context.Context, batchSize, limit int, onProgress func(CardBackfillStats)) (CardBackfillStats, error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	var stats CardBackfillStats
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

		batch, err := b.repo.OGCardsPending(ctx, afterID, size)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			return stats, nil
		}

		for _, p := range batch {
			stats.Scanned++
			afterID = p.ImageID

			if _, err := b.build(ctx, p); err != nil {
				if errors.Is(err, ErrSourceTooSmall) {
					stats.Skipped++
				} else {
					stats.Failed++
				}
				logCardSkip(p, err)
				continue
			}
			stats.Built++
		}

		if onProgress != nil {
			onProgress(stats)
		}
	}
}

// build renders one card, uploads it and records it.
func (b *CardBuilder) build(ctx context.Context, p PendingOGCard) (OGCard, error) {
	rc, _, err := b.storage.GetObject(ctx, p.ObjectKey)
	if err != nil {
		return OGCard{}, fmt.Errorf("fetch original: %w", err)
	}
	jpegBytes, width, height, err := RenderOGCard(rc)
	_ = rc.Close()
	if err != nil {
		return OGCard{}, err
	}

	key := ogCardKeyFor(p.ObjectKey)
	if _, err := b.storage.Upload(ctx, key, "image/jpeg", bytes.NewReader(jpegBytes)); err != nil {
		return OGCard{}, fmt.Errorf("upload card: %w", err)
	}
	if err := b.repo.SetOGCard(ctx, p.ImageID, key, width, height); err != nil {
		return OGCard{}, fmt.Errorf("record card: %w", err)
	}
	return OGCard{Key: key, Width: width, Height: height}, nil
}

func logCardSkip(p PendingOGCard, err error) {
	if errors.Is(err, ErrSourceTooSmall) {
		// Expected for the handful of originals that are themselves small —
		// not a failure, and upscaling them would look worse.
		log.Printf("checkpoint: no og card for image %s: %v", p.ImageID, err)
		return
	}
	log.Printf("checkpoint: og card for image %s (%s): %v", p.ImageID, p.ObjectKey, err)
}
