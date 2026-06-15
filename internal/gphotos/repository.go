package gphotos

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotConnected is returned when a user has not connected Google Photos.
var ErrNotConnected = errors.New("photos not connected")

// Repository stores and retrieves Photos tokens per user.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository builds a Photos token repository.
func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Save upserts the user's Photos tokens.
func (r *Repository) Save(ctx context.Context, t *Tokens) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO user_photos_tokens
		    (user_id, access_token, refresh_token, token_type, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id) DO UPDATE SET
		    access_token  = EXCLUDED.access_token,
		    refresh_token = EXCLUDED.refresh_token,
		    token_type    = EXCLUDED.token_type,
		    expires_at    = EXCLUDED.expires_at,
		    updated_at    = NOW()`,
		t.UserID, t.AccessToken, t.RefreshToken, t.TokenType, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save photos tokens: %w", err)
	}
	return nil
}

// Get retrieves the user's Photos tokens. Returns ErrNotConnected if absent.
func (r *Repository) Get(ctx context.Context, userID string) (*Tokens, error) {
	var t Tokens
	err := r.db.QueryRow(ctx,
		`SELECT user_id, access_token, refresh_token, token_type, expires_at, album_id, connected_at
		 FROM user_photos_tokens WHERE user_id = $1`, userID).
		Scan(&t.UserID, &t.AccessToken, &t.RefreshToken, &t.TokenType, &t.ExpiresAt, &t.AlbumID, &t.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("get photos tokens: %w", err)
	}
	return &t, nil
}

// UpdateAlbumID caches the "Checkpoint App" Photos album ID after first creation.
func (r *Repository) UpdateAlbumID(ctx context.Context, userID, albumID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE user_photos_tokens SET album_id = $2, updated_at = NOW() WHERE user_id = $1`,
		userID, albumID)
	if err != nil {
		return fmt.Errorf("update photos album id: %w", err)
	}
	return nil
}

// Delete removes the user's Photos tokens (disconnect).
func (r *Repository) Delete(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM user_photos_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete photos tokens: %w", err)
	}
	return nil
}
