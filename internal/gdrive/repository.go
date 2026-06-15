package gdrive

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotConnected is returned when a user has not connected Google Drive.
var ErrNotConnected = errors.New("drive not connected")

// Repository stores and retrieves Drive tokens per user.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository builds a Drive token repository.
func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Save upserts the user's Drive tokens (insert or update all mutable fields).
func (r *Repository) Save(ctx context.Context, t *Tokens) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO user_drive_tokens
		    (user_id, email, access_token, refresh_token, token_type, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (user_id) DO UPDATE SET
		    email         = EXCLUDED.email,
		    access_token  = EXCLUDED.access_token,
		    refresh_token = EXCLUDED.refresh_token,
		    token_type    = EXCLUDED.token_type,
		    expires_at    = EXCLUDED.expires_at,
		    updated_at    = NOW()`,
		t.UserID, t.Email, t.AccessToken, t.RefreshToken, t.TokenType, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save drive tokens: %w", err)
	}
	return nil
}

// Get retrieves the user's Drive tokens. Returns ErrNotConnected if not found.
func (r *Repository) Get(ctx context.Context, userID string) (*Tokens, error) {
	var t Tokens
	err := r.db.QueryRow(ctx,
		`SELECT user_id, email, access_token, refresh_token, token_type, expires_at, drive_folder_id, connected_at
		 FROM user_drive_tokens WHERE user_id = $1`, userID).
		Scan(&t.UserID, &t.Email, &t.AccessToken, &t.RefreshToken, &t.TokenType, &t.ExpiresAt, &t.FolderID, &t.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("get drive tokens: %w", err)
	}
	return &t, nil
}

// UpdateFolderID caches the "Checkpoint App" Drive folder ID after first creation.
func (r *Repository) UpdateFolderID(ctx context.Context, userID, folderID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE user_drive_tokens SET drive_folder_id = $2, updated_at = NOW() WHERE user_id = $1`,
		userID, folderID)
	if err != nil {
		return fmt.Errorf("update drive folder id: %w", err)
	}
	return nil
}

// Delete removes the user's Drive tokens (disconnect).
func (r *Repository) Delete(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM user_drive_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete drive tokens: %w", err)
	}
	return nil
}
