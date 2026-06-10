// Package middleware holds shared HTTP middleware.
package middleware

import (
	"net/http"
	"strings"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Authenticator turns a raw bearer token into a principal, or an error.
type Authenticator interface {
	Authenticate(token string) (authctx.Principal, error)
}

// Auth verifies the Bearer access token and injects the principal into the
// request context. Requests without a valid token get 401.
func Auth(a Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				respond.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			token := strings.TrimPrefix(header, "Bearer ")
			principal, err := a.Authenticate(token)
			if err != nil {
				respond.Error(w, http.StatusUnauthorized, "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(authctx.With(r.Context(), principal)))
		})
	}
}
