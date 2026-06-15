package gdrive

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes Drive OAuth endpoints.
type Handler struct {
	repo        *Repository
	client      *Client
	jwtSecret   []byte
	frontendURL string // where to redirect after OAuth callback
}

// NewHandler builds a Drive HTTP handler.
func NewHandler(repo *Repository, client *Client, jwtSecret []byte, frontendURL string) *Handler {
	return &Handler{repo: repo, client: client, jwtSecret: jwtSecret, frontendURL: frontendURL}
}

// Register mounts Drive integration routes.
func (h *Handler) Register(r chi.Router, auth func(http.Handler) http.Handler) {
	r.Route("/integrations/drive", func(r chi.Router) {
		r.With(auth).Post("/init", h.Init)
		r.With(auth).Get("/status", h.Status)
		r.With(auth).Delete("/disconnect", h.Disconnect)
		// callback has no auth — Google redirects here after consent
		r.Get("/callback", h.Callback)
	})
}

// stateClaims is the JWT payload embedded in the OAuth state parameter.
type stateClaims struct {
	jwt.RegisteredClaims
}

// Init returns the Google OAuth2 authorization URL. The caller redirects the
// browser to auth_url.
func (h *Handler) Init(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())

	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   p.UserID,
			Issuer:    "checkpoint-drive",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	state, err := token.SignedString(h.jwtSecret)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not generate state")
		return
	}

	respond.JSON(w, http.StatusOK, map[string]string{
		"auth_url": h.client.BuildAuthURL(state),
	})
}

// Callback is the Google OAuth2 redirect target. It validates the state JWT,
// exchanges the code for tokens, and redirects the browser back to the FE.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" || code == "" || state == "" {
		http.Redirect(w, r, h.frontendURL+"/profile?drive=error", http.StatusFound)
		return
	}

	// Verify state JWT and extract userID.
	var claims stateClaims
	tok, err := jwt.ParseWithClaims(state, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return h.jwtSecret, nil
	})
	if err != nil || !tok.Valid || claims.Issuer != "checkpoint-drive" {
		log.Printf("gdrive: invalid state JWT: %v", err)
		http.Redirect(w, r, h.frontendURL+"/profile?drive=error", http.StatusFound)
		return
	}
	userID := claims.Subject

	tokens, err := h.client.Exchange(r.Context(), userID, code)
	if err != nil {
		log.Printf("gdrive: exchange code user=%s: %v", userID, err)
		http.Redirect(w, r, h.frontendURL+"/profile?drive=error", http.StatusFound)
		return
	}

	if err := h.repo.Save(r.Context(), tokens); err != nil {
		log.Printf("gdrive: save tokens user=%s: %v", userID, err)
		http.Redirect(w, r, h.frontendURL+"/profile?drive=error", http.StatusFound)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/profile?drive=connected", http.StatusFound)
}

// Status returns the current Drive connection status for the authenticated user.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	tokens, err := h.repo.Get(r.Context(), p.UserID)
	if errors.Is(err, ErrNotConnected) {
		respond.JSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load drive status")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"connected":    true,
		"email":        tokens.Email,
		"connected_at": tokens.ConnectedAt,
	})
}

// Disconnect removes the user's Drive tokens.
func (h *Handler) Disconnect(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	if err := h.repo.Delete(r.Context(), p.UserID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not disconnect drive")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}
