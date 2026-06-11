// Package geocode resolves geographic coordinates into human-readable addresses.
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL  = "https://nominatim.openstreetmap.org"
	defaultTimeout  = 5 * time.Second
	defaultInterval = time.Second
)

// Config controls the Nominatim reverse-geocoding client.
type Config struct {
	Enabled      bool
	BaseURL      string
	UserAgent    string
	Timeout      time.Duration
	RateInterval time.Duration
}

// Client is a cached, rate-limited Nominatim reverse-geocoding client.
type Client struct {
	enabled      bool
	baseURL      string
	userAgent    string
	httpClient   *http.Client
	rateInterval time.Duration

	cacheMu sync.RWMutex
	cache   map[string]result

	rateMu      sync.Mutex
	lastRequest time.Time
}

// result is the cached reverse-geocode lookup for a coordinate.
type result struct {
	displayName string
	province    string
}

// NewClient builds a reverse-geocoding client.
func NewClient(cfg Config) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	interval := cfg.RateInterval
	if interval <= 0 {
		interval = defaultInterval
	}

	return &Client{
		enabled:      cfg.Enabled,
		baseURL:      baseURL,
		userAgent:    strings.TrimSpace(cfg.UserAgent),
		httpClient:   &http.Client{Timeout: timeout},
		rateInterval: interval,
		cache:        make(map[string]result),
	}
}

// Reverse returns Nominatim's display_name for the given coordinates.
func (c *Client) Reverse(ctx context.Context, lat, lng float64) (string, error) {
	r, err := c.lookup(ctx, lat, lng)
	return r.displayName, err
}

// Province returns the province/state name for the given coordinates (Vietnam
// provinces map to Nominatim's `state`, with city/county as fallbacks). Empty
// when unknown or the geocoder is disabled.
func (c *Client) Province(ctx context.Context, lat, lng float64) (string, error) {
	r, err := c.lookup(ctx, lat, lng)
	return r.province, err
}

// lookup performs (and caches) a single reverse-geocode for a coordinate,
// extracting both the display name and the province.
func (c *Client) lookup(ctx context.Context, lat, lng float64) (result, error) {
	if !c.enabled {
		return result{}, nil
	}

	key := fmt.Sprintf("%.6f,%.6f", lat, lng)
	c.cacheMu.RLock()
	cached, ok := c.cache[key]
	c.cacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	if err := c.waitForRateLimit(ctx); err != nil {
		return result{}, err
	}

	endpoint, err := url.Parse(c.baseURL + "/reverse")
	if err != nil {
		return result{}, fmt.Errorf("parse geocoder URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("format", "jsonv2")
	query.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	query.Set("lon", strconv.FormatFloat(lng, 'f', 6, 64))
	query.Set("zoom", "18")
	query.Set("addressdetails", "1")
	query.Set("layer", "address")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result{}, fmt.Errorf("build geocoder request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return result{}, fmt.Errorf("reverse geocode request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("reverse geocode status: %s", res.Status)
	}

	var payload struct {
		DisplayName string `json:"display_name"`
		Address     struct {
			State  string `json:"state"`
			City   string `json:"city"`
			County string `json:"county"`
		} `json:"address"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return result{}, fmt.Errorf("decode reverse geocode response: %w", err)
	}

	province := firstNonEmpty(payload.Address.State, payload.Address.City, payload.Address.County)

	parts := strings.Split(payload.DisplayName, ",")

	// Trim khoảng trắng thừa cho từng phần tử trong mảng
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	// 2. Tìm index của province trong mảng
	provinceIdx := -1
	for i, part := range parts {
		if part == province {
			provinceIdx = i
			break
		}
	}

	displayName := payload.DisplayName

	// 3. Nếu tìm thấy province, tiến hành bóc tách theo quy luật
	if provinceIdx != -1 {
		startIdx := provinceIdx - 1
		endIdx := provinceIdx + 1

		// Ràng buộc boundary để tránh panics lỗi out of range index
		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx >= len(parts) {
			endIdx = len(parts) - 1
		}

		// Cắt mảng từ [province - 1] đến [province + 1] và gộp lại bằng ", "
		displayName = strings.Join(parts[startIdx:endIdx+1], ", ")
	}

	r := result{
		displayName: displayName,
		province:    province,
	}

	c.cacheMu.Lock()
	c.cache[key] = r
	c.cacheMu.Unlock()
	return r, nil
}

// firstNonEmpty returns the first trimmed non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()

	wait := time.Until(c.lastRequest.Add(c.rateInterval))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.lastRequest = time.Now()
	return nil
}
