package share

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShareRendersSelfReferentialOGURL(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("GET", "/share/checkpoint/abc-123", nil)
	r.Host = "share.checkpoint-app.space"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("User-Agent", "facebookexternalhit/1.1")
	w := httptest.NewRecorder()

	h.respondShare(w, r, &MetaData{
		Title: "T", Description: "D",
		Image:       "https://media.example/x.jpg",
		Type:        "article",
		URL:         "https://checkpoint-app.space/c/abc-123",
		RedirectURL: "https://checkpoint-app.space/c/abc-123",
	})

	body := w.Body.String()
	want := `<meta property="og:url" content="https://share.checkpoint-app.space/share/checkpoint/abc-123">`
	if !strings.Contains(body, want) {
		t.Errorf("og:url not self-referential.\nwant: %s\ngot:\n%s", want, body)
	}
	if !strings.Contains(body, `content="summary_large_image"`) {
		t.Error("twitter:card fallback missing")
	}
	if !strings.Contains(body, `<meta property="og:image" content="https://media.example/x.jpg">`) {
		t.Error("og:image missing")
	}
}
