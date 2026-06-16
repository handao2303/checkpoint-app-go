package share

import (
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

var shareTemplate = template.Must(template.New("share").Parse(`<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Title}}</title>
    <meta property="og:title" content="{{.Title}}">
    <meta property="og:description" content="{{.Description}}">
    <meta property="og:image" content="{{.Image}}">
    <meta property="og:type" content="{{.Type}}">
    <meta property="og:url" content="{{.URL}}">
    <meta property="og:site_name" content="Checkpoint">
    <!-- Twitter -->
    <meta name="twitter:card" content="summary_large_image">
    <meta name="twitter:title" content="{{.Title}}">
    <meta name="twitter:description" content="{{.Description}}">
    <meta name="twitter:image" content="{{.Image}}">
    <!-- Meta refresh fallback -->
    <meta http-equiv="refresh" content="0;url={{.RedirectURL}}">
    <script>
        window.location.href = "{{.RedirectURL}}";
    </script>
</head>
<body>
    <p>Đang chuyển hướng tới ứng dụng Checkpoint...</p>
</body>
</html>`))

// Handler serves dynamic Open Graph share links.
type Handler struct {
	svc         *Service
	frontendURL string
}

// NewHandler constructs a new Share Handler.
func NewHandler(svc *Service, frontendURL string) *Handler {
	return &Handler{
		svc:         svc,
		frontendURL: frontendURL,
	}
}

// Register mounts the share routes. These are public, unauthenticated routes.
func (h *Handler) Register(r chi.Router) {
	r.Get("/share/checkpoint/{id}", h.ShareCheckpoint)
	r.Get("/share/journey/{id}", h.ShareJourney)
	r.Get("/share/user/{username}", h.ShareUser)
}

// ShareCheckpoint handles checkpoint share links.
func (h *Handler) ShareCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := h.svc.GetCheckpointMeta(r.Context(), id, h.frontendURL)
	if err != nil {
		log.Printf("share: error getting checkpoint meta: %v", err)
		http.Redirect(w, r, h.frontendURL+"/c/"+id, http.StatusFound)
		return
	}

	h.respondShare(w, r, meta)
}

// ShareJourney handles journey share links.
func (h *Handler) ShareJourney(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := h.svc.GetJourneyMeta(r.Context(), id, h.frontendURL)
	if err != nil {
		log.Printf("share: error getting journey meta: %v", err)
		http.Redirect(w, r, h.frontendURL+"/j/"+id, http.StatusFound)
		return
	}

	h.respondShare(w, r, meta)
}

// ShareUser handles user profile share links.
func (h *Handler) ShareUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	meta, err := h.svc.GetUserMeta(r.Context(), username, h.frontendURL)
	if err != nil {
		log.Printf("share: error getting user profile meta: %v", err)
		http.Redirect(w, r, h.frontendURL+"/u/"+username, http.StatusFound)
		return
	}

	h.respondShare(w, r, meta)
}

func (h *Handler) respondShare(w http.ResponseWriter, r *http.Request, meta *MetaData) {
	ua := r.Header.Get("User-Agent")
	if isCrawler(ua) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := shareTemplate.Execute(w, meta); err != nil {
			log.Printf("share: template execution error: %v", err)
		}
		return
	}

	http.Redirect(w, r, meta.RedirectURL, http.StatusFound)
}

func isCrawler(ua string) bool {
	ua = strings.ToLower(ua)
	crawlers := []string{
		"facebookexternalhit",
		"twitterbot",
		"linkedinbot",
		"telegrambot",
		"slackbot",
		"discordbot",
		"whatsapp",
		"googlebot",
		"bingbot",
		"yandexbot",
		"baiduspider",
		"vkshare",
		"embedly",
		"zalo",
		"pinterest",
		"skype",
		"applebot",
	}
	for _, c := range crawlers {
		if strings.Contains(ua, c) {
			return true
		}
	}
	return false
}
