package gdrive

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Syncer uploads checkpoint media to Google Drive in the background.
// All public methods are nil-safe — call them even when Drive is not configured.
type Syncer struct {
	repo   *Repository
	client *Client
}

// NewSyncer builds a Drive syncer.
func NewSyncer(repo *Repository, client *Client) *Syncer {
	return &Syncer{repo: repo, client: client}
}

// SyncCheckpoint uploads all media in cp to the user's Drive in a goroutine.
// No-op if the user has not connected Drive or if the syncer is nil.
func (s *Syncer) SyncCheckpoint(userID string, cp CheckpointMeta) {
	if s == nil || len(cp.Media) == 0 {
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
		log.Printf("gdrive: get tokens user=%s: %v", userID, err)
		return
	}

	tokens, changed, err := s.client.RefreshIfNeeded(ctx, tokens)
	if err != nil {
		log.Printf("gdrive: refresh tokens user=%s: %v", userID, err)
		return
	}
	if changed {
		if err := s.repo.Save(ctx, tokens); err != nil {
			log.Printf("gdrive: save refreshed tokens user=%s: %v", userID, err)
		}
	}

	folderID, err := s.ensureFolder(ctx, tokens)
	if err != nil {
		log.Printf("gdrive: ensure folder user=%s: %v", userID, err)
		return
	}

	meta := buildMeta(cp)
	for i, media := range cp.Media {
		filename := buildFilename(cp.Name, cp.CreatedAt, i, media.Type)
		if _, err := s.client.UploadMedia(ctx, tokens.AccessToken, folderID, filename, media.URL, media.Type, meta); err != nil {
			log.Printf("gdrive: upload media checkpoint=%s url=%s: %v", cp.ID, media.URL, err)
		}
	}
}

func (s *Syncer) ensureFolder(ctx context.Context, tokens *Tokens) (string, error) {
	if tokens.FolderID != nil && *tokens.FolderID != "" {
		return *tokens.FolderID, nil
	}
	folderID, err := s.client.EnsureFolder(ctx, tokens.AccessToken)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdateFolderID(ctx, tokens.UserID, folderID); err != nil {
		log.Printf("gdrive: cache folder id user=%s: %v", tokens.UserID, err)
	}
	return folderID, nil
}

func buildFilename(name string, t time.Time, idx int, mediaType string) string {
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
	ext := ".jpg"
	if mediaType == "video" {
		ext = ".mp4"
	}
	return fmt.Sprintf("%s_%s%s%s", safe, date, suffix, ext)
}

func buildMeta(cp CheckpointMeta) map[string]string {
	m := map[string]string{
		"checkpoint_id": cp.ID,
		"name":          cp.Name,
		"lat":           fmt.Sprintf("%.6f", cp.Lat),
		"lng":           fmt.Sprintf("%.6f", cp.Lng),
		"category":      cp.Category,
		"created_at":    cp.CreatedAt.UTC().Format(time.RFC3339),
	}
	if cp.Address != nil {
		m["address"] = *cp.Address
	}
	if cp.Rating != nil {
		m["rating"] = strconv.Itoa(int(*cp.Rating))
	}
	if cp.Note != nil {
		m["note"] = *cp.Note
	}
	return m
}
