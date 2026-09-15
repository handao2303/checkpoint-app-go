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
<html prefix="og: https://ogp.me/ns#">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Title}}</title>
    <link rel="canonical" href="{{.URL}}">
    <meta property="og:title" content="{{.Title}}">
    <meta property="og:description" content="{{.Description}}">
    <meta property="og:image" content="{{.Image}}">
    <meta property="og:image:secure_url" content="{{.Image}}">
    <meta property="og:image:alt" content="{{.Title}}">
{{- if and .ImageWidth .ImageHeight}}
    <meta property="og:image:width" content="{{.ImageWidth}}">
    <meta property="og:image:height" content="{{.ImageHeight}}">
{{- end}}
    <meta property="og:type" content="{{.Type}}">
    <meta property="og:url" content="{{.URL}}">
    <meta property="og:site_name" content="Checkpoint">
    <meta property="og:locale" content="vi_VN">
    <!-- Twitter -->
    <meta name="twitter:card" content="{{.TwitterCard}}">
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
	r.Get("/share/user/{id}", h.ShareUser)
	r.Get("/share/place/{id}", h.SharePlace)
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
	key := "share:meta:v2:checkpoint:" + id
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
	key := "share:meta:v2:journey:" + id
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

// ShareUser handles user profile share links. {id} is a user id (preferred) or
// a username (legacy links).
func (h *Handler) ShareUser(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "id")
	key := "share:meta:v2:user:" + ref
	meta, err := h.getCachedMeta(r.Context(), key, func() (*MetaData, error) {
		return h.svc.GetUserMeta(r.Context(), ref, h.frontendURL)
	})
	if err != nil {
		log.Printf("share: error getting user profile meta: %v", err)
		http.Redirect(w, r, h.frontendURL+"/u/"+ref, http.StatusFound)
		return
	}

	h.respondShare(w, r, meta)
}

// SharePlace handles place share links.
func (h *Handler) SharePlace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key := "share:meta:v2:place:" + id
	meta, err := h.getCachedMeta(r.Context(), key, func() (*MetaData, error) {
		return h.svc.GetPlaceMeta(r.Context(), id, h.frontendURL)
	})
	if err != nil {
		log.Printf("share: error getting place meta: %v", err)
		http.Redirect(w, r, h.frontendURL+"/p/"+id, http.StatusFound)
		return
	}

	h.respondShare(w, r, meta)
}

func (h *Handler) respondShare(w http.ResponseWriter, r *http.Request, meta *MetaData) {
	ua := r.Header.Get("User-Agent")
	if isCrawler(ua) {
		// og:url must be self-referential. Facebook (and every other scraper that
		// honours canonical URLs) re-scrapes whatever og:url points at, so
		// pointing it at the SPA made every card fall back to the SPA's static
		// site-wide og-image instead of the checkpoint/place/user image.
		view := *meta
		view.URL = currentURL(r)
		if view.TwitterCard == "" {
			view.TwitterCard = cardLarge
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Crawlers cache aggressively; keep it short so edits to a checkpoint
		// surface in re-scrapes without a manual purge.
		w.Header().Set("Cache-Control", "public, max-age=300")
		if err := shareTemplate.Execute(w, &view); err != nil {
			log.Printf("share: template execution error: %v", err)
		}
		return
	}

	http.Redirect(w, r, meta.RedirectURL, http.StatusFound)
}

// currentURL rebuilds the absolute URL this request was made to, honouring the
// proxy headers set by Cloudflare in front of the share domain.
func currentURL(r *http.Request) string {
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS == nil && strings.HasPrefix(r.Host, "localhost") {
		scheme = "http"
	}

	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}

	return scheme + "://" + host + r.URL.EscapedPath()
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
