package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quanluon/checkpoint-app/pkg/supabase"
)

// Service errors surfaced to handlers.
var (
	ErrUnauthorized    = errors.New("unauthorized")
	ErrUnsupportedFile = errors.New("unsupported file type")
)

// repository is the data layer the service depends on (defined here, on the
// consumer side, for easy mocking in tests).
type repository interface {
	GetByID(ctx context.Context, id string) (*User, error)
	GetBySupabaseID(ctx context.Context, supabaseID string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	CreateUser(ctx context.Context, p CreateUserParams) (*User, error)
	UpdateProfile(ctx context.Context, id string, displayName, bio *string) (*User, error)
	UpdateAvatar(ctx context.Context, id, url string) (*User, error)
	UpdateAuthIdentity(ctx context.Context, id string, email *string, isAnonymous bool) (*User, error)
	CreateRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, hash string) (*RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, hash string) error
}

// supabaseVerifier verifies Supabase tokens during /auth/exchange.
type supabaseVerifier interface {
	Verify(token string) (*supabase.Claims, error)
}

// fileStorage uploads an object and returns its public URL.
type fileStorage interface {
	Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error)
}

// Service holds the identity business logic.
type Service struct {
	repo     repository
	tokens   *TokenManager
	supabase supabaseVerifier
	storage  fileStorage
}

// NewService wires the identity service.
func NewService(repo repository, tokens *TokenManager, verifier supabaseVerifier, storage fileStorage) *Service {
	return &Service{repo: repo, tokens: tokens, supabase: verifier, storage: storage}
}

// Exchange verifies a Supabase token, provisions the user on first sight, and
// returns our own token pair.
func (s *Service) Exchange(ctx context.Context, supabaseToken string) (*TokenPair, *User, error) {
	claims, err := s.supabase.Verify(supabaseToken)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}

	user, err := s.repo.GetBySupabaseID(ctx, claims.Subject)
	switch {
	case errors.Is(err, ErrUserNotFound):
		user, err = s.provisionUser(ctx, claims)
	case err == nil:
		user, err = s.reconcile(ctx, user, claims)
	}
	if err != nil {
		return nil, nil, err
	}

	pair, err := s.issueTokens(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return pair, user, nil
}

// Refresh rotates a refresh token: the old one is revoked and a new pair issued.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	hash := HashToken(refreshToken)
	rt, err := s.repo.GetRefreshToken(ctx, hash)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if rt.RevokedAt != nil || time.Now().After(rt.ExpiresAt) {
		return nil, ErrUnauthorized
	}
	if err := s.repo.RevokeRefreshToken(ctx, hash); err != nil {
		return nil, err
	}
	user, err := s.repo.GetByID(ctx, rt.UserID)
	if err != nil {
		return nil, ErrUnauthorized
	}
	return s.issueTokens(ctx, user)
}

// Logout revokes a refresh token. Revoking an unknown token is a no-op.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return s.repo.RevokeRefreshToken(ctx, HashToken(refreshToken))
}

// GetByID returns a user by id.
func (s *Service) GetByID(ctx context.Context, id string) (*User, error) {
	return s.repo.GetByID(ctx, id)
}

// GetByUsername returns a public profile by username.
func (s *Service) GetByUsername(ctx context.Context, username string) (*User, error) {
	return s.repo.GetByUsername(ctx, username)
}

// UpdateProfile updates the caller's display name and/or bio.
func (s *Service) UpdateProfile(ctx context.Context, id string, displayName, bio *string) (*User, error) {
	return s.repo.UpdateProfile(ctx, id, displayName, bio)
}

// UpdateAvatar uploads a new avatar image and stores its URL on the user.
func (s *Service) UpdateAvatar(ctx context.Context, id, contentType string, body io.Reader) (*User, error) {
	ext := imageExt(contentType)
	if ext == "" {
		return nil, ErrUnsupportedFile
	}
	key := fmt.Sprintf("avatars/%s/%s%s", id, randHex(8), ext)
	url, err := s.storage.Upload(ctx, key, contentType, body)
	if err != nil {
		return nil, fmt.Errorf("upload avatar: %w", err)
	}
	return s.repo.UpdateAvatar(ctx, id, url)
}

func (s *Service) issueTokens(ctx context.Context, u *User) (*TokenPair, error) {
	access, err := s.tokens.GenerateAccess(u)
	if err != nil {
		return nil, err
	}
	refresh, expiresAt, err := s.tokens.GenerateRefresh()
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateRefreshToken(ctx, u.ID, HashToken(refresh), expiresAt); err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh}, nil
}

// provisionUser creates a user for a first-time Supabase login, retrying on the
// rare username collision and tolerating a concurrent provision of the same
// Supabase id.
func (s *Service) provisionUser(ctx context.Context, claims *supabase.Claims) (*User, error) {
	email := claimsEmail(claims)
	for attempt := 0; attempt < 5; attempt++ {
		u, err := s.repo.CreateUser(ctx, CreateUserParams{
			SupabaseID:  claims.Subject,
			Email:       email,
			Username:    generateUsername(claims.Email),
			Role:        "user",
			IsAnonymous: claims.IsAnonymous,
		})
		if err == nil {
			return u, nil
		}
		if !isUniqueViolation(err) {
			return nil, err
		}
		// Lost a race on supabase_id? The row now exists — use it.
		if existing, ferr := s.repo.GetBySupabaseID(ctx, claims.Subject); ferr == nil {
			return existing, nil
		}
		// Otherwise it was a username clash: loop and regenerate.
	}
	return nil, errors.New("could not provision user after retries")
}

// reconcile syncs an existing user's email/anonymity from fresh Supabase claims.
// This is what makes the anonymous → real account upgrade stick: Supabase keeps
// the same `sub`, so the local user (and all their data) is reused — we just
// flip is_anonymous and store the new email.
func (s *Service) reconcile(ctx context.Context, user *User, claims *supabase.Claims) (*User, error) {
	email := claimsEmail(claims)
	if user.IsAnonymous == claims.IsAnonymous && samePtr(user.Email, email) {
		return user, nil // nothing changed
	}
	updated, err := s.repo.UpdateAuthIdentity(ctx, user.ID, email, claims.IsAnonymous)
	if err != nil {
		if isUniqueViolation(err) {
			return user, nil // email already linked elsewhere — keep login working
		}
		return nil, err
	}
	return updated, nil
}

func claimsEmail(c *supabase.Claims) *string {
	if c.Email == "" {
		return nil
	}
	return &c.Email
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// generateUsername derives a unique-ish handle from an email local part.
func generateUsername(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	local = sanitizeUsername(local)
	if local == "" {
		local = "traveler"
	}
	return local + "_" + randHex(3)
}

func sanitizeUsername(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func imageExt(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}
