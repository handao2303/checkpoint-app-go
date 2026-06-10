package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// Sentinel errors returned by the repository.
var (
	ErrUserNotFound         = errors.New("user not found")
	ErrRefreshTokenNotFound = errors.New("refresh token not found")
)

// Repository is the Postgres-backed data layer for the identity domain.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to avatar keys on read
}

// NewRepository builds a Repository over a pgx pool. mediaBase is the public
// base URL used to turn stored avatar keys into full URLs.
func NewRepository(db *pgxpool.Pool, mediaBase string) *Repository {
	return &Repository{db: db, mediaBase: mediaBase}
}

const userColumns = `id, supabase_id, email, username, display_name, avatar_key, bio,
	role, is_anonymous, total_xp, level, checkpoint_count, journey_count, created_at, updated_at`

// withAvatarURL turns the stored avatar key into a full public URL.
func (r *Repository) withAvatarURL(u *User) *User {
	if u != nil && u.AvatarURL != nil {
		url := storage.PublicURL(r.mediaBase, *u.AvatarURL)
		u.AvatarURL = &url
	}
	return u
}

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.SupabaseID, &u.Email, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Bio,
		&u.Role, &u.IsAnonymous, &u.TotalXP, &u.Level, &u.CheckpointCount, &u.JourneyCount,
		&u.CreatedAt, &u.UpdatedAt,
	)
	return &u, err
}

// GetByID returns the active user with the given id.
func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1 AND deleted_at IS NULL`, id)
	return r.getUser(row, "get user by id")
}

// GetBySupabaseID returns the active user linked to a Supabase account.
func (r *Repository) GetBySupabaseID(ctx context.Context, supabaseID string) (*User, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE supabase_id = $1 AND deleted_at IS NULL`, supabaseID)
	return r.getUser(row, "get user by supabase id")
}

// GetByUsername returns the active user with the given username.
func (r *Repository) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1 AND deleted_at IS NULL`, username)
	return r.getUser(row, "get user by username")
}

func (r *Repository) getUser(row pgx.Row, op string) (*User, error) {
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return r.withAvatarURL(u), nil
}

// CreateUser inserts a new user and returns the stored row.
func (r *Repository) CreateUser(ctx context.Context, p CreateUserParams) (*User, error) {
	row := r.db.QueryRow(ctx,
		`INSERT INTO users (supabase_id, email, username, role, is_anonymous)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+userColumns,
		p.SupabaseID, p.Email, p.Username, p.Role, p.IsAnonymous)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// UpdateProfile updates the mutable profile fields. Nil pointers leave the
// existing value untouched (COALESCE).
func (r *Repository) UpdateProfile(ctx context.Context, id string, displayName, bio *string) (*User, error) {
	row := r.db.QueryRow(ctx,
		`UPDATE users SET
		   display_name = COALESCE($2, display_name),
		   bio          = COALESCE($3, bio),
		   updated_at   = NOW()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING `+userColumns,
		id, displayName, bio)
	return r.getUser(row, "update profile")
}

// UpdateAvatar stores the avatar's object key and returns the updated user.
func (r *Repository) UpdateAvatar(ctx context.Context, id, key string) (*User, error) {
	row := r.db.QueryRow(ctx,
		`UPDATE users SET avatar_key = $2, updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING `+userColumns,
		id, key)
	return r.getUser(row, "update avatar")
}

// UpdateAuthIdentity sets the email and anonymity flag — used when an anonymous
// account is upgraded to a permanent one (same Supabase id).
func (r *Repository) UpdateAuthIdentity(ctx context.Context, id string, email *string, isAnonymous bool) (*User, error) {
	row := r.db.QueryRow(ctx,
		`UPDATE users SET email = $2, is_anonymous = $3, updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING `+userColumns,
		id, email, isAnonymous)
	return r.getUser(row, "update auth identity")
}

// CreateRefreshToken stores the hash of a freshly issued refresh token.
func (r *Repository) CreateRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, expiresAt)
	if err != nil {
		return fmt.Errorf("create refresh token: %w", err)
	}
	return nil
}

// GetRefreshToken looks up a refresh token by its hash.
func (r *Repository) GetRefreshToken(ctx context.Context, hash string) (*RefreshToken, error) {
	var rt RefreshToken
	err := r.db.QueryRow(ctx,
		`SELECT user_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = $1`, hash).
		Scan(&rt.UserID, &rt.ExpiresAt, &rt.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefreshTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get refresh token: %w", err)
	}
	return &rt, nil
}

// RevokeRefreshToken marks a still-valid refresh token as revoked.
func (r *Repository) RevokeRefreshToken(ctx context.Context, hash string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW()
		 WHERE token_hash = $1 AND revoked_at IS NULL`, hash)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
