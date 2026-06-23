package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quanluon/checkpoint-app/pkg/idref"
	"github.com/quanluon/checkpoint-app/pkg/supabase"
)

// Service errors surfaced to handlers.
var (
	ErrUnauthorized    = errors.New("unauthorized")
	ErrUnsupportedFile = errors.New("unsupported file type")
	ErrSelfFollow      = errors.New("cannot follow yourself")
	ErrInvalidUsername = errors.New("invalid username")
	ErrUsernameTaken   = errors.New("username taken")
)

// usernameRe matches an acceptable user-chosen handle: lowercase letters,
// digits and underscore, 3–20 chars.
var usernameRe = regexp.MustCompile(`^[a-z0-9_]{3,20}$`)

// reservedUsernames are handles users may not claim (route names / impersonation).
var reservedUsernames = map[string]bool{
	"admin": true, "api": true, "me": true, "null": true, "undefined": true,
	"support": true, "checkpoint": true, "auth": true, "users": true,
	"user": true, "following": true, "privacy": true, "terms": true,
}

// repository is the data layer the service depends on (defined here, on the
// consumer side, for easy mocking in tests).
type repository interface {
	GetByID(ctx context.Context, id string) (*User, error)
	GetByIDs(ctx context.Context, ids []string) ([]*User, error)
	GetBySupabaseID(ctx context.Context, supabaseID string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	CreateUser(ctx context.Context, p CreateUserParams) (*User, error)
	UpdateProfile(ctx context.Context, id string, displayName, bio, username *string) (*User, error)
	UpdateAvatar(ctx context.Context, id, url string) (*User, error)
	UpdateAuthIdentity(ctx context.Context, id string, email *string, isAnonymous bool) (*User, error)
	CreateRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, hash string) (*RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, hash string) error
	Follow(ctx context.Context, followerID, followeeID string) error
	Unfollow(ctx context.Context, followerID, followeeID string) error
	GetFolloweeIDs(ctx context.Context, followerID string) ([]string, error)
	GetFollowers(ctx context.Context, userID string) ([]PublicUserSummary, error)
	GetFollowingUsers(ctx context.Context, userID string) ([]PublicUserSummary, error)
	SearchUsers(ctx context.Context, prefix string, limit int) ([]PublicUserSummary, error)
}

// supabaseVerifier verifies Supabase tokens during /auth/exchange.
type supabaseVerifier interface {
	Verify(token string) (*supabase.Claims, error)
}

// passwordAuthenticator exchanges email+password for a Supabase access token,
// powering username/email login server-side (nil → password login disabled).
type passwordAuthenticator interface {
	PasswordGrant(ctx context.Context, email, password string) (string, error)
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
	auth     passwordAuthenticator // nil → username/password login disabled
}

// NewService wires the identity service.
func NewService(repo repository, tokens *TokenManager, verifier supabaseVerifier, storage fileStorage) *Service {
	return &Service{repo: repo, tokens: tokens, supabase: verifier, storage: storage}
}

// WithPasswordLogin enables username/email + password login via Supabase's
// password grant. Without it, LoginWithPassword returns ErrUnauthorized.
func (s *Service) WithPasswordLogin(a passwordAuthenticator) *Service {
	s.auth = a
	return s
}

// LoginWithPassword logs a user in by username OR email + password. The username
// is resolved to its email and the credentials are checked against Supabase
// server-side, so the client never sees the email. All failure modes return
// ErrUnauthorized (generic) to avoid username/email enumeration.
func (s *Service) LoginWithPassword(ctx context.Context, identifier, password string) (*TokenPair, *User, error) {
	if s.auth == nil {
		return nil, nil, ErrUnauthorized
	}
	identifier = strings.TrimSpace(identifier)
	email := identifier
	if !strings.Contains(identifier, "@") {
		u, err := s.repo.GetByUsername(ctx, strings.ToLower(identifier))
		if err != nil || u.Email == nil || u.IsAnonymous {
			return nil, nil, ErrUnauthorized
		}
		email = *u.Email
	}
	token, err := s.auth.PasswordGrant(ctx, email, password)
	if err != nil {
		return nil, nil, ErrUnauthorized
	}
	return s.Exchange(ctx, token)
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

// GetByIDOrUsername resolves a user by id when ref is a UUID, otherwise by
// username. id is the stable reference (usernames can change); the username
// path stays for backward-compatible links.
func (s *Service) GetByIDOrUsername(ctx context.Context, ref string) (*User, error) {
	if idref.IsUUID(ref) {
		return s.repo.GetByID(ctx, ref)
	}
	return s.repo.GetByUsername(ctx, ref)
}

// GetByIDs returns a list of users by their IDs.
func (s *Service) GetByIDs(ctx context.Context, ids []string) ([]*User, error) {
	return s.repo.GetByIDs(ctx, ids)
}

// Follow makes followerID follow the user identified by followeeRef (id or
// username). Returns ErrUserNotFound if the target doesn't exist, ErrSelfFollow
// on self-follow.
func (s *Service) Follow(ctx context.Context, followerID, followeeRef string) error {
	followee, err := s.GetByIDOrUsername(ctx, followeeRef)
	if err != nil {
		return err
	}
	if followee.ID == followerID {
		return ErrSelfFollow
	}
	return s.repo.Follow(ctx, followerID, followee.ID)
}

// Unfollow removes the follow edge from followerID to the user identified by
// followeeRef (id or username).
func (s *Service) Unfollow(ctx context.Context, followerID, followeeRef string) error {
	followee, err := s.GetByIDOrUsername(ctx, followeeRef)
	if err != nil {
		return err
	}
	return s.repo.Unfollow(ctx, followerID, followee.ID)
}

// GetFolloweeIDs returns the ids of users the caller follows.
func (s *Service) GetFolloweeIDs(ctx context.Context, followerID string) ([]string, error) {
	return s.repo.GetFolloweeIDs(ctx, followerID)
}

// GetFollowers returns compact public profiles for users following ref.
func (s *Service) GetFollowers(ctx context.Context, ref string) ([]PublicUserSummary, error) {
	user, err := s.GetByIDOrUsername(ctx, ref)
	if err != nil {
		return nil, err
	}
	return s.repo.GetFollowers(ctx, user.ID)
}

// GetFollowingUsers returns compact public profiles followed by ref.
func (s *Service) GetFollowingUsers(ctx context.Context, ref string) ([]PublicUserSummary, error) {
	user, err := s.GetByIDOrUsername(ctx, ref)
	if err != nil {
		return nil, err
	}
	return s.repo.GetFollowingUsers(ctx, user.ID)
}

// SearchUsers returns up to `limit` users matching the prefix, for the @-mention
// picker. A blank query returns no results; limit is clamped to [1, 10].
func (s *Service) SearchUsers(ctx context.Context, query string, limit int) ([]PublicUserSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []PublicUserSummary{}, nil
	}
	if limit <= 0 || limit > 10 {
		limit = 8
	}
	return s.repo.SearchUsers(ctx, query, limit)
}

// UpdateProfile updates the caller's display name, bio and/or username. A
// non-nil username is normalized (lowercased/trimmed) and validated; a clash
// surfaces as ErrUsernameTaken.
func (s *Service) UpdateProfile(ctx context.Context, id string, displayName, bio, username *string) (*User, error) {
	if username != nil {
		u := strings.ToLower(strings.TrimSpace(*username))
		if !usernameRe.MatchString(u) || reservedUsernames[u] {
			return nil, ErrInvalidUsername
		}
		username = &u
	}
	user, err := s.repo.UpdateProfile(ctx, id, displayName, bio, username)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	return user, nil
}

// UpdateAvatar uploads a new avatar image and stores its URL on the user.
func (s *Service) UpdateAvatar(ctx context.Context, id, contentType string, body io.Reader) (*User, error) {
	ext := imageExt(contentType)
	if ext == "" {
		return nil, ErrUnsupportedFile
	}
	key := fmt.Sprintf("avatars/%s/%s%s", id, randHex(8), ext)
	storedKey, err := s.storage.Upload(ctx, key, contentType, body)
	if err != nil {
		return nil, fmt.Errorf("upload avatar: %w", err)
	}
	return s.repo.UpdateAvatar(ctx, id, storedKey)
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
			Username:    generateUsername(claims.Email, attempt),
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

// maxUsernameBase caps the generated handle's base length so suffixes still fit
// within the 20-char limit users are held to when changing their username.
const maxUsernameBase = 18

// generateUsername derives a handle from an email local part. The first attempt
// returns a clean handle (e.g. "lhongquan1998"); only on a collision does it
// append a suffix ("..._2", "..._3"), falling back to random on the last try.
func generateUsername(email string, attempt int) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	base := sanitizeUsername(local)
	if base == "" {
		base = "traveler"
	}
	if len(base) > maxUsernameBase {
		base = base[:maxUsernameBase]
	}
	switch {
	case attempt <= 0:
		return base
	case attempt < 4:
		return base + "_" + strconv.Itoa(attempt+1)
	default:
		return base + "_" + randHex(2)
	}
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
