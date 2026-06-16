package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
)

// processorRepo is the data layer the media processor needs (consumer-side
// interface, satisfied by *Repository).
type processorRepo interface {
	PendingMedia(ctx context.Context, checkpointID string) ([]pendingMedia, error)
	SetMediaThumbnail(ctx context.Context, imageID, thumbnailKey string) error
	RefreshCover(ctx context.Context, checkpointID string) error
}

// processorStorage is the object storage the processor needs (satisfied by *storage.R2).
type processorStorage interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, string, error)
	Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error)
}

// Processor turns raw uploaded media into web-friendly derivatives: it transcodes
// videos to MP4 (in place) and generates thumbnails, then marks each row done by
// setting its thumbnail_key. Runs in the Cloud Run worker (Pub/Sub push) and, in
// local dev without Pub/Sub, inline in the API process.
type Processor struct {
	repo    processorRepo
	storage processorStorage
}

// NewProcessor builds a media processor.
func NewProcessor(repo processorRepo, storage processorStorage) *Processor {
	return &Processor{repo: repo, storage: storage}
}

// ProcessCheckpoint processes all of a checkpoint's pending media, then refreshes
// the denormalized cover. Idempotent: already-processed rows (thumbnail_key set)
// are skipped, so duplicate Pub/Sub deliveries are safe.
func (p *Processor) ProcessCheckpoint(ctx context.Context, checkpointID string) error {
	items, err := p.repo.PendingMedia(ctx, checkpointID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil // nothing to do (or a duplicate delivery)
	}
	for _, it := range items {
		if err := p.processItem(ctx, it); err != nil {
			return fmt.Errorf("process media %s: %w", it.ID, err)
		}
	}
	return p.repo.RefreshCover(ctx, checkpointID)
}

func (p *Processor) processItem(ctx context.Context, it pendingMedia) error {
	rc, _, err := p.storage.GetObject(ctx, it.ObjectKey)
	if err != nil {
		return fmt.Errorf("download raw: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, maxVideoInputBytes+1))
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("read raw: %w", err)
	}

	switch it.Type {
	case MediaTypeVideo:
		return p.processVideo(ctx, it, raw)
	default:
		// Phase 1 only processes video asynchronously; images are still prepared
		// client-side, so an image row should never be pending here.
		return fmt.Errorf("async processing not enabled for media type %q", it.Type)
	}
}

func (p *Processor) processVideo(ctx context.Context, it pendingMedia, raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty video")
	}
	if len(raw) > maxVideoInputBytes {
		return fmt.Errorf("video exceeds %d MB input limit", maxVideoInputBytes>>20)
	}

	mp4, err := transcodeToMP4(ctx, raw)
	if err != nil {
		return fmt.Errorf("transcode: %w", err)
	}
	if len(mp4) > maxVideoOutputBytes {
		return fmt.Errorf("transcoded video exceeds %d MB", maxVideoOutputBytes>>20)
	}

	// Overwrite the raw upload in place with the MP4 (object_key keeps its .mp4
	// extension, so cover media-type inference stays correct).
	if _, err := p.storage.Upload(ctx, it.ObjectKey, "video/mp4", bytes.NewReader(mp4)); err != nil {
		return fmt.Errorf("upload mp4: %w", err)
	}

	thumb, err := extractThumbnailJPEG(ctx, mp4)
	if err != nil {
		return fmt.Errorf("thumbnail: %w", err)
	}
	thumbKey := thumbnailKeyFor(it.ObjectKey)
	if _, err := p.storage.Upload(ctx, thumbKey, "image/jpeg", bytes.NewReader(thumb)); err != nil {
		return fmt.Errorf("upload thumbnail: %w", err)
	}

	// Setting thumbnail_key marks the row done (and makes duplicate deliveries skip it).
	return p.repo.SetMediaThumbnail(ctx, it.ID, thumbKey)
}

// thumbnailKeyFor derives a thumbnail object key in the media's own namespace,
// e.g. checkpoints/<prefix>/<rand>.mp4 → checkpoints/<prefix>/thumbnails/<rand>.jpg.
// This satisfies validateMediaKeys (checkpoints/ prefix, .jpg suffix).
func thumbnailKeyFor(objectKey string) string {
	dir := path.Dir(objectKey)
	return path.Join(dir, "thumbnails", randHex(8)+".jpg")
}
