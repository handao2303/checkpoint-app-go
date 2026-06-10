package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
)

const tokenIssuer = "checkpoint-app"

// TokenManager mints and verifies our backend access tokens, and mints opaque
// refresh tokens.
type TokenManager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokenManager builds a TokenManager. secret signs the HS256 access tokens.
func NewTokenManager(secret string, accessTTL, refreshTTL time.Duration) *TokenManager {
	return &TokenManager{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// GenerateAccess issues a short-lived signed access token for u.
func (m *TokenManager) GenerateAccess(u *User) (string, error) {
	now := time.Now()
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	claims := Claims{
		UserID:   u.ID,
		Email:    email,
		Username: u.Username,
		Role:     u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return signed, nil
}

// ParseAccess validates a token string and returns its claims.
func (m *TokenManager) ParseAccess(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse access token: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("invalid access token")
	}
	return claims, nil
}

// Authenticate satisfies middleware.Authenticator: it maps a raw token to the
// principal carried through the request context.
func (m *TokenManager) Authenticate(token string) (authctx.Principal, error) {
	claims, err := m.ParseAccess(token)
	if err != nil {
		return authctx.Principal{}, err
	}
	return authctx.Principal{
		UserID:   claims.UserID,
		Email:    claims.Email,
		Username: claims.Username,
		Role:     claims.Role,
	}, nil
}

// GenerateRefresh returns a high-entropy opaque refresh token and its expiry.
// The token is random (not a JWT) so it is only ever valid by matching the
// stored hash, which lets us revoke it.
func (m *TokenManager) GenerateRefresh() (token string, expiresAt time.Time, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), time.Now().Add(m.refreshTTL), nil
}

// HashToken returns the SHA-256 hex digest used to store/look up refresh tokens.
// SHA-256 (not bcrypt) is correct here: the token is already 256 bits of
// entropy, and we need a deterministic value to query by.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randHex returns n random bytes hex-encoded (2n chars). Used for username
// suffixes and object keys.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
