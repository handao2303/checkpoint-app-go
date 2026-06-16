package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

const maxAvatarBytes = 8 << 20 // 8 MiB

// Handler exposes the identity domain over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts the identity routes. auth is the middleware that guards
// authenticated endpoints.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/exchange", h.Exchange)
		r.Post("/refresh", h.Refresh)
		r.Post("/logout", h.Logout)
	})
	r.Route("/users", func(r chi.Router) {
		r.With(auth).Get("/me", h.GetMe)
		r.With(auth).Patch("/me", h.UpdateProfile)
		r.With(auth).Post("/me/avatar", h.UploadAvatar)
		// Static /following must precede /{username} so it isn't read as a username.
		r.With(auth).Get("/following", h.GetFollowing)
		r.With(optionalAuth).Get("/by-ids", h.GetByIDs)
		// Public profile (shared) — optional auth.
		r.With(optionalAuth).Get("/{username}", h.GetByUsername)
		r.With(auth).Post("/{username}/follow", h.Follow)
		r.With(auth).Delete("/{username}/follow", h.Unfollow)
	})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         *User  `json:"user,omitempty"`
}

// Exchange swaps a Supabase token for our own token pair.
func (h *Handler) Exchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SupabaseToken string `json:"supabase_token"`
	}
	if err := decodeJSON(r, &req); err != nil || req.SupabaseToken == "" {
		respond.Error(w, http.StatusBadRequest, "supabase_token is required")
		return
	}
	pair, user, err := h.svc.Exchange(r.Context(), req.SupabaseToken)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			respond.Error(w, http.StatusUnauthorized, "invalid supabase token")
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not exchange token")
		return
	}
	respond.JSON(w, http.StatusOK, tokenResponse{pair.AccessToken, pair.RefreshToken, user})
}

// Refresh rotates a refresh token into a new pair.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		respond.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}
	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	respond.JSON(w, http.StatusOK, tokenResponse{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken})
}

// Logout revokes a refresh token.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		respond.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}
	if err := h.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not log out")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// GetMe returns the authenticated user's profile.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	user, err := h.svc.GetByID(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, user)
}

// UpdateProfile updates display name and/or bio.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		DisplayName *string `json:"display_name"`
		Bio         *string `json:"bio"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := h.svc.UpdateProfile(r.Context(), p.UserID, req.DisplayName, req.Bio)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not update profile")
		return
	}
	respond.JSON(w, http.StatusOK, user)
}

// UploadAvatar accepts a multipart "avatar" file and stores it.
func (h *Handler) UploadAvatar(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "avatar file is required")
		return
	}
	defer func() { _ = file.Close() }()

	user, err := h.svc.UpdateAvatar(r.Context(), p.UserID, header.Header.Get("Content-Type"), file)
	if err != nil {
		if errors.Is(err, ErrUnsupportedFile) {
			respond.Error(w, http.StatusUnsupportedMediaType, "avatar must be jpeg, png, webp or gif")
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not upload avatar")
		return
	}
	respond.JSON(w, http.StatusOK, user)
}

// GetByUsername returns a public profile.
func (h *Handler) GetByUsername(w http.ResponseWriter, r *http.Request) {
	user, err := h.svc.GetByUsername(r.Context(), chi.URLParam(r, "username"))
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, user)
}

// GetByIDs returns a list of public user profiles by their IDs.
func (h *Handler) GetByIDs(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		respond.JSON(w, http.StatusOK, []*User{})
		return
	}
	ids := strings.Split(idsParam, ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	users, err := h.svc.GetByIDs(r.Context(), ids)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not fetch users")
		return
	}
	respond.JSON(w, http.StatusOK, users)
}

// GetFollowing returns the ids of users the caller follows.
func (h *Handler) GetFollowing(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	ids, err := h.svc.GetFolloweeIDs(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load following")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ids": ids})
}

// Follow makes the caller follow {username}.
func (h *Handler) Follow(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	err := h.svc.Follow(r.Context(), p.UserID, chi.URLParam(r, "username"))
	switch {
	case err == nil:
		respond.JSON(w, http.StatusNoContent, nil)
	case errors.Is(err, ErrUserNotFound):
		respond.Error(w, http.StatusNotFound, "user not found")
	case errors.Is(err, ErrSelfFollow):
		respond.Error(w, http.StatusBadRequest, "cannot follow yourself")
	default:
		respond.Error(w, http.StatusInternalServerError, "could not follow")
	}
}

// Unfollow makes the caller stop following {username}.
func (h *Handler) Unfollow(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	err := h.svc.Unfollow(r.Context(), p.UserID, chi.URLParam(r, "username"))
	switch {
	case err == nil:
		respond.JSON(w, http.StatusNoContent, nil)
	case errors.Is(err, ErrUserNotFound):
		respond.Error(w, http.StatusNotFound, "user not found")
	default:
		respond.Error(w, http.StatusInternalServerError, "could not unfollow")
	}
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
