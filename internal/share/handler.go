package share

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
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
    <script>
        window.location.replace("{{.RedirectURL}}");
    </script>
</head>
<body>
    <p>Đang chuyển hướng tới ứng dụng Checkpoint... Nếu không tự động chuyển hướng, vui lòng <a href="{{.RedirectURL}}">bấm vào đây</a>.</p>
</body>
</html>`))

// Handler serves dynamic Open Graph share links.
type Handler struct {
	svc         *Service
	frontendURL string
	redis       redis.Cmdable
}

func isNilCmdable(c redis.Cmdable) bool {
	if c == nil {
		return true
	}
	val := reflect.ValueOf(c)
	switch val.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.UnsafePointer, reflect.Interface, reflect.Slice:
		return val.IsNil()
	}
	return false
}

// NewHandler constructs a new Share Handler.
func NewHandler(svc *Service, frontendURL string, rClient redis.Cmdable) *Handler {
	var cmdable redis.Cmdable
	if !isNilCmdable(rClient) {
		cmdable = rClient
	}
	return &Handler{
		svc:         svc,
		frontendURL: frontendURL,
		redis:       cmdable,
	}
}

// Register mounts the share routes. These are public, unauthenticated routes.
func (h *Handler) Register(r chi.Router) {
	r.Get("/share/checkpoint/{id}", h.ShareCheckpoint)
	r.Get("/share/journey/{id}", h.ShareJourney)
	r.Get("/share/user/{username}", h.ShareUser)
}

const shareCacheTTL = 10 * time.Minute

func (h *Handler) getCachedMeta(ctx context.Context, key string, fetch func() (*MetaData, error)) (*MetaData, error) {
	if h.redis != nil {
		val, err := h.redis.Get(ctx, key).Result()
		if err == nil {
			var meta MetaData
			if err := json.Unmarshal([]byte(val), &meta); err == nil {
				return &meta, nil
			}
		}
	}

	meta, err := fetch()
	if err != nil {
		return nil, err
	}

	if h.redis != nil {
		data, err := json.Marshal(meta)
		if err == nil {
			_ = h.redis.Set(ctx, key, data, shareCacheTTL).Err()
		}
	}

	return meta, nil
}

// ShareCheckpoint handles checkpoint share links.
func (h *Handler) ShareCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key := "share:meta:checkpoint:" + id
	meta, err := h.getCachedMeta(r.Context(), key, func() (*MetaData, error) {
		return h.svc.GetCheckpointMeta(r.Context(), id, h.frontendURL)
	})
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
	key := "share:meta:journey:" + id
	meta, err := h.getCachedMeta(r.Context(), key, func() (*MetaData, error) {
		return h.svc.GetJourneyMeta(r.Context(), id, h.frontendURL)
	})
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
	key := "share:meta:user:" + username
	meta, err := h.getCachedMeta(r.Context(), key, func() (*MetaData, error) {
		return h.svc.GetUserMeta(r.Context(), username, h.frontendURL)
	})
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
		"facebook",
		"facebot",
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
