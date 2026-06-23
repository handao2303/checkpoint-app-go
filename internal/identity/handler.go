package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
		r.Post("/login", h.Login)
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
		r.With(optionalAuth).Get("/search", h.SearchUsers) // @-mention picker
		// Public profile (shared) — optional auth. {id} accepts a user id
		// (preferred, stable) or a username (legacy links).
		r.With(optionalAuth).Get("/{id}/followers", h.GetFollowers)
		r.With(optionalAuth).Get("/{id}/following-list", h.GetFollowingUsers)
		r.With(optionalAuth).Get("/{id}", h.GetProfile)
		r.With(auth).Post("/{id}/follow", h.Follow)
		r.With(auth).Delete("/{id}/follow", h.Unfollow)
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

// Login authenticates by username OR email + password (Supabase password grant
// server-side) and returns our own token pair.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Identifier) == "" || req.Password == "" {
		respond.Error(w, http.StatusBadRequest, "identifier and password are required")
		return
	}
	pair, user, err := h.svc.LoginWithPassword(r.Context(), req.Identifier, req.Password)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid credentials")
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

// UpdateProfile updates display name, bio and/or username.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		DisplayName *string `json:"display_name"`
		Bio         *string `json:"bio"`
		Username    *string `json:"username"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := h.svc.UpdateProfile(r.Context(), p.UserID, req.DisplayName, req.Bio, req.Username)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidUsername):
			respond.Error(w, http.StatusBadRequest, "username must be 3–20 chars: a–z, 0–9, _")
		case errors.Is(err, ErrUsernameTaken):
			respond.Error(w, http.StatusConflict, "username already taken")
		default:
			respond.Error(w, http.StatusInternalServerError, "could not update profile")
		}
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

// GetProfile returns a public profile by id (preferred) or username (legacy).
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	user, err := h.svc.GetByIDOrUsername(r.Context(), chi.URLParam(r, "id"))
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

// GetFollowers returns compact public profiles for users following {id}.
func (h *Handler) GetFollowers(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.GetFollowers(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, users)
}

// GetFollowingUsers returns compact public profiles followed by {id}.
func (h *Handler) GetFollowingUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.GetFollowingUsers(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, users)
}

// SearchUsers returns up to a handful of users matching ?q= for the @-mention
// picker. A blank query yields an empty list.
func (h *Handler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	users, err := h.svc.SearchUsers(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not search users")
		return
	}
	respond.JSON(w, http.StatusOK, users)
}

// Follow makes the caller follow the user identified by {id} (id or username).
func (h *Handler) Follow(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	err := h.svc.Follow(r.Context(), p.UserID, chi.URLParam(r, "id"))
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

// Unfollow makes the caller stop following the user identified by {id}.
func (h *Handler) Unfollow(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	err := h.svc.Unfollow(r.Context(), p.UserID, chi.URLParam(r, "id"))
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
