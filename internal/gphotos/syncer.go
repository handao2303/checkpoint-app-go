package gphotos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Syncer uploads checkpoint images to Google Photos in the background.
// All public methods are nil-safe — call them even when Photos is not configured.
type Syncer struct {
	repo   *Repository
	client *Client
}

// NewSyncer builds a Photos syncer.
func NewSyncer(repo *Repository, client *Client) *Syncer {
	return &Syncer{repo: repo, client: client}
}

// SyncCheckpoint uploads all images in cp to the user's Google Photos in a goroutine.
// No-op if the user has not connected Photos or if the syncer is nil.
func (s *Syncer) SyncCheckpoint(userID string, cp CheckpointMeta) {
	if s == nil || len(cp.ImageURLs) == 0 {
		return
	}
	go s.sync(userID, cp)
}

func (s *Syncer) sync(userID string, cp CheckpointMeta) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tokens, err := s.repo.Get(ctx, userID)
	if errors.Is(err, ErrNotConnected) {
		return
	}
	if err != nil {
		log.Printf("gphotos: get tokens user=%s: %v", userID, err)
		return
	}

	tokens, changed, err := s.client.RefreshIfNeeded(ctx, tokens)
	if err != nil {
		log.Printf("gphotos: refresh tokens user=%s: %v", userID, err)
		return
	}
	if changed {
		if err := s.repo.Save(ctx, tokens); err != nil {
			log.Printf("gphotos: save refreshed tokens user=%s: %v", userID, err)
		}
	}

	albumID, err := s.ensureAlbum(ctx, tokens)
	if err != nil {
		log.Printf("gphotos: ensure album user=%s: %v", userID, err)
		return
	}

	description := buildDescription(cp)
	for i, imgURL := range cp.ImageURLs {
		filename := buildFilename(cp.Name, cp.CreatedAt, i)
		if _, err := s.client.UploadImage(ctx, tokens.AccessToken, albumID, filename, imgURL, description); err != nil {
			log.Printf("gphotos: upload image checkpoint=%s url=%s: %v", cp.ID, imgURL, err)
		}
	}
}

func (s *Syncer) ensureAlbum(ctx context.Context, tokens *Tokens) (string, error) {
	if tokens.AlbumID != nil && *tokens.AlbumID != "" {
		return *tokens.AlbumID, nil
	}
	albumID, err := s.client.EnsureAlbum(ctx, tokens.AccessToken)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdateAlbumID(ctx, tokens.UserID, albumID); err != nil {
		log.Printf("gphotos: cache album id user=%s: %v", tokens.UserID, err)
	}
	return albumID, nil
}

func buildFilename(name string, t time.Time, idx int) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ' ' {
			return r
		}
		return '_'
	}, name)
	date := t.UTC().Format("2006-01-02")
	suffix := ""
	if idx > 0 {
		suffix = "_" + strconv.Itoa(idx+1)
	}
	return fmt.Sprintf("%s_%s%s.jpg", safe, date, suffix)
}

func buildDescription(cp CheckpointMeta) string {
	parts := []string{cp.Name}
	if cp.Address != nil {
		parts = append(parts, *cp.Address)
	}
	parts = append(parts,
		fmt.Sprintf("%.6f, %.6f", cp.Lat, cp.Lng),
		cp.Category,
		cp.CreatedAt.UTC().Format("2006-01-02"),
	)
	if cp.Rating != nil {
		parts = append(parts, fmt.Sprintf("Rating: %d/5", *cp.Rating))
	}
	if cp.Note != nil && *cp.Note != "" {
		parts = append(parts, *cp.Note)
	}
	return strings.Join(parts, " · ")
}
