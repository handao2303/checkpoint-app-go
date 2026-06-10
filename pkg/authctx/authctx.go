// Package authctx carries the authenticated principal through the request
// context. It lives in its own package so both the auth middleware and the
// feature handlers can depend on it without creating an import cycle.
package authctx

import "context"

// Principal is the minimal identity extracted from a verified access token.
type Principal struct {
	UserID   string
	Email    string
	Username string
	Role     string
}

type ctxKey struct{}

// With returns a copy of ctx carrying p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From extracts the principal from ctx. ok is false when the request was not
// authenticated.
func From(ctx context.Context) (p Principal, ok bool) {
	p, ok = ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
