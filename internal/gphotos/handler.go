package gphotos

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes Photos OAuth endpoints.
type Handler struct {
	repo                *Repository
	client              *Client
	jwtSecret           []byte
	frontendURL         string
	redirectURIOverride string
}

// NewHandler builds a Photos HTTP handler.
func NewHandler(repo *Repository, client *Client, jwtSecret []byte, frontendURL, redirectURIOverride string) *Handler {
	return &Handler{
		repo:                repo,
		client:              client,
		jwtSecret:           jwtSecret,
		frontendURL:         frontendURL,
		redirectURIOverride: redirectURIOverride,
	}
}

// Register mounts Photos integration routes.
func (h *Handler) Register(r chi.Router, auth func(http.Handler) http.Handler) {
	r.Route("/integrations/photos", func(r chi.Router) {
		r.With(auth).Post("/init", h.Init)
		r.With(auth).Get("/status", h.Status)
		r.With(auth).Delete("/disconnect", h.Disconnect)
		r.Get("/callback", h.Callback)
	})
}

type stateClaims struct {
	jwt.RegisteredClaims
}

func (h *Handler) callbackURI(r *http.Request) string {
	if h.redirectURIOverride != "" {
		return h.redirectURIOverride
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/integrations/photos/callback"
}

// Init returns the Google OAuth2 authorization URL for Photos.
func (h *Handler) Init(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   p.UserID,
			Issuer:    "checkpoint-photos",
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
		"auth_url": h.client.BuildAuthURL(state, h.callbackURI(r)),
	})
}

// Callback handles the Google OAuth2 redirect for Photos.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" || code == "" || state == "" {
		http.Redirect(w, r, h.frontendURL+"/profile?photos=error", http.StatusFound)
		return
	}

	var claims stateClaims
	tok, err := jwt.ParseWithClaims(state, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return h.jwtSecret, nil
	})
	if err != nil || !tok.Valid || claims.Issuer != "checkpoint-photos" {
		log.Printf("gphotos: invalid state JWT: %v", err)
		http.Redirect(w, r, h.frontendURL+"/profile?photos=error", http.StatusFound)
		return
	}
	userID := claims.Subject

	tokens, err := h.client.Exchange(r.Context(), userID, code, h.callbackURI(r))
	if err != nil {
		log.Printf("gphotos: exchange code user=%s: %v", userID, err)
		http.Redirect(w, r, h.frontendURL+"/profile?photos=error", http.StatusFound)
		return
	}

	if err := h.repo.Save(r.Context(), tokens); err != nil {
		log.Printf("gphotos: save tokens user=%s: %v", userID, err)
		http.Redirect(w, r, h.frontendURL+"/profile?photos=error", http.StatusFound)
		return
	}

	http.Redirect(w, r, h.frontendURL+"/profile?photos=connected", http.StatusFound)
}

// Status returns the current Photos connection status for the authenticated user.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	_, err := h.repo.Get(r.Context(), p.UserID)
	if errors.Is(err, ErrNotConnected) {
		respond.JSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load photos status")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"connected": true})
}

// Disconnect removes the user's Photos tokens.
func (h *Handler) Disconnect(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	if err := h.repo.Delete(r.Context(), p.UserID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not disconnect photos")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}
