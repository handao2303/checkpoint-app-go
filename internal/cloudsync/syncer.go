// Package cloudsync fans out checkpoint media sync to all connected cloud
// storage backends (Google Drive, Google Photos, …) through a single call.
// Adding a new backend only requires wiring it here — callers are untouched.
package cloudsync

import (
	"time"

	"github.com/quanluon/checkpoint-app/internal/gdrive"
	"github.com/quanluon/checkpoint-app/internal/gphotos"
)

// CheckpointMeta is the unified payload passed to every backend.
type CheckpointMeta struct {
	ID        string
	Name      string
	Lat, Lng  float64
	Address   *string
	Rating    *int16
	Note      *string
	Category  string
	CreatedAt time.Time
	Media     []Media
}

// Media is one original checkpoint media file to sync.
type Media struct {
	URL  string
	Type string
}

// Syncer fans out SyncCheckpoint to every registered backend.
// All methods are nil-safe — safe to call even when no backend is configured.
type Syncer struct {
	drive  *gdrive.Syncer
	photos *gphotos.Syncer
}

// New builds a Syncer. Pass nil for any backend that is not configured.
func New(drive *gdrive.Syncer, photos *gphotos.Syncer) *Syncer {
	return &Syncer{drive: drive, photos: photos}
}

// SyncCheckpoint dispatches to all backends concurrently (each backend fires
// its own goroutine internally, so this call returns immediately).
func (s *Syncer) SyncCheckpoint(userID string, cp CheckpointMeta) {
	if s == nil || len(cp.Media) == 0 {
		return
	}
	media := make([]gdrive.Media, 0, len(cp.Media))
	photosMedia := make([]gphotos.Media, 0, len(cp.Media))
	for _, item := range cp.Media {
		media = append(media, gdrive.Media{URL: item.URL, Type: item.Type})
		photosMedia = append(photosMedia, gphotos.Media{URL: item.URL, Type: item.Type})
	}
	s.drive.SyncCheckpoint(userID, gdrive.CheckpointMeta{
		ID: cp.ID, Name: cp.Name, Lat: cp.Lat, Lng: cp.Lng,
		Address: cp.Address, Rating: cp.Rating, Note: cp.Note,
		Category: cp.Category, CreatedAt: cp.CreatedAt, Media: media,
	})
	s.photos.SyncCheckpoint(userID, gphotos.CheckpointMeta{
		ID: cp.ID, Name: cp.Name, Lat: cp.Lat, Lng: cp.Lng,
		Address: cp.Address, Rating: cp.Rating, Note: cp.Note,
		Category: cp.Category, CreatedAt: cp.CreatedAt, Media: photosMedia,
	})
}
