// Package supabase verifies Supabase-issued JWTs using the project's public
// JWKS (asymmetric signing keys), so the backend can trust the identity behind
// a /auth/exchange request without holding any signing secret.
package supabase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Claims are the subset of Supabase JWT claims we use. Subject (sub) is the
// Supabase user id.
type Claims struct {
	Email       string `json:"email"`
	Role        string `json:"role"`
	IsAnonymous bool   `json:"is_anonymous"`
	jwt.RegisteredClaims
}

// Verifier validates Supabase access tokens against the project's JWKS. It
// transparently handles key rotation (current / standby / previous keys) by
// matching each token's `kid` to the published public keys.
type Verifier struct {
	keyfunc keyfunc.Keyfunc
}

// JWKSURL builds the JWKS endpoint from a Supabase project URL
// (e.g. https://<ref>.supabase.co).
func JWKSURL(supabaseURL string) string {
	return strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json"
}

// NewVerifier fetches the JWKS and starts background refresh tied to ctx.
func NewVerifier(ctx context.Context, jwksURL string) (*Verifier, error) {
	k, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("init supabase jwks (%s): %w", jwksURL, err)
	}
	return &Verifier{keyfunc: k}, nil
}

// Verify parses and validates a Supabase token, returning its claims. Only
// asymmetric algorithms are accepted — rejecting HS256 prevents algorithm-
// confusion attacks where a public key is abused as an HMAC secret.
func (v *Verifier) Verify(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, v.keyfunc.Keyfunc,
		jwt.WithValidMethods([]string{"ES256", "RS256"}))
	if err != nil {
		return nil, fmt.Errorf("verify supabase token: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("invalid supabase token")
	}
	if claims.Subject == "" {
		return nil, errors.New("supabase token missing sub claim")
	}
	return claims, nil
}
