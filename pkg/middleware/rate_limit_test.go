package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
)

func TestRateLimiterByIP(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 2})
	handler := limiter.ByIP(okHandler())

	assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	rec := assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusTooManyRequests)

	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 429 body: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Fatalf("error = %q, want rate limit exceeded", body["error"])
	}

	assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.11:1234", ""), http.StatusOK)
}

func TestRateLimiterUsesFirstValidForwardedIP(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1})
	handler := limiter.ByIP(okHandler())

	assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.10:1234", "invalid, 203.0.113.5"), http.StatusOK)
	assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.11:1234", "203.0.113.5"), http.StatusTooManyRequests)
}

func TestRateLimiterByUser(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1})
	handler := limiter.ByUser(okHandler())

	assertStatus(t, handler, userRequest("user-1"), http.StatusOK)
	assertStatus(t, handler, userRequest("user-1"), http.StatusTooManyRequests)
	assertStatus(t, handler, userRequest("user-2"), http.StatusOK)
}

func TestAuthenticatedRequestUsesIPAndUserLimits(t *testing.T) {
	t.Parallel()

	ipLimiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 2})
	userLimiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1})
	handler := ipLimiter.ByIP(userLimiter.ByUser(okHandler()))

	assertStatus(t, handler, userRequestFrom("user-1", "198.51.100.10:1234"), http.StatusOK)
	assertStatus(t, handler, userRequestFrom("user-1", "198.51.100.10:1234"), http.StatusTooManyRequests)
	assertStatus(t, handler, userRequestFrom("user-2", "198.51.100.10:1234"), http.StatusTooManyRequests)
}

func TestRateLimiterExemptions(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1})
	handler := limiter.ByIP(okHandler())

	for range 3 {
		assertStatus(t, handler, request(http.MethodOptions, "/", "198.51.100.10:1234", ""), http.StatusOK)
		assertStatus(t, handler, request(http.MethodGet, "/health", "198.51.100.10:1234", ""), http.StatusOK)
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter(RateLimitConfig{Enabled: false, RequestsPerMinute: 1, Burst: 1})
	handler := limiter.ByIP(okHandler())

	for range 3 {
		assertStatus(t, handler, request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	}
}

func TestRateLimiterCleansInactiveEntries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.June, 11, 0, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1})
	limiter.now = func() time.Time { return now }
	limiter.entryTTL = time.Minute
	limiter.cleanupInterval = time.Minute
	limiter.lastCleanup = now

	if !limiter.allowLocal("old") {
		t.Fatal("first old request should be allowed")
	}
	now = now.Add(2 * time.Minute)
	if !limiter.allowLocal("new") {
		t.Fatal("first new request should be allowed")
	}

	if _, ok := limiter.entries["old"]; ok {
		t.Fatal("inactive entry was not cleaned up")
	}
	if _, ok := limiter.entries["new"]; !ok {
		t.Fatal("active entry was unexpectedly removed")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func request(method, path, remoteAddr, forwardedFor string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return req
}

func userRequest(userID string) *http.Request {
	return userRequestFrom(userID, "198.51.100.10:1234")
}

func userRequestFrom(userID, remoteAddr string) *http.Request {
	req := request(http.MethodGet, "/", remoteAddr, "")
	return req.WithContext(authctx.With(req.Context(), authctx.Principal{UserID: userID}))
}

func assertStatus(t *testing.T, handler http.Handler, req *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s status = %d, want %d; body=%s", req.Method, req.URL.Path, rec.Code, want, rec.Body.String())
	}
	return rec
}
