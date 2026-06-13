// Package geocode resolves geographic coordinates into human-readable addresses
// via Photon (https://photon.komoot.io) — an OSM-based geocoder that's much
// faster than the public Nominatim instance (~200-500ms, no hard 1/s limit).
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL  = "https://photon.komoot.io"
	defaultTimeout  = 5 * time.Second
	defaultInterval = 0 // Photon has no hard rate limit; cache + low traffic suffice.
)

// Config controls the reverse-geocoding client.
type Config struct {
	Enabled      bool
	BaseURL      string
	UserAgent    string
	Timeout      time.Duration
	RateInterval time.Duration
}

// Client is a cached reverse-geocoding client over Photon.
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
	if interval < 0 {
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

// Reverse returns a concise address for the given coordinates.
func (c *Client) Reverse(ctx context.Context, lat, lng float64) (string, error) {
	r, err := c.lookup(ctx, lat, lng)
	return r.displayName, err
}

// Province returns the province/state name for the given coordinates (Vietnam
// provinces map to Photon's `state`, with county/city as fallbacks). Empty when
// unknown or the geocoder is disabled.
func (c *Client) Province(ctx context.Context, lat, lng float64) (string, error) {
	r, err := c.lookup(ctx, lat, lng)
	return r.province, err
}

// lookup performs (and caches) a single reverse-geocode for a coordinate.
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
	query.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	query.Set("lon", strconv.FormatFloat(lng, 'f', 6, 64))
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

	r, err := parsePhoton(res.Body)
	if err != nil {
		return result{}, err
	}

	c.cacheMu.Lock()
	c.cache[key] = r
	c.cacheMu.Unlock()
	return r, nil
}

// parsePhoton extracts a concise admin address + province from a Photon GeoJSON
// reverse response. A malformed body is an error; an empty feature list is a
// valid "no result" (empty address).
func parsePhoton(body io.Reader) (result, error) {
	var payload struct {
		Features []struct {
			Properties struct {
				Name     string `json:"name"`
				Street   string `json:"street"`
				District string `json:"district"`
				Locality string `json:"locality"`
				City     string `json:"city"`
				County   string `json:"county"`
				State    string `json:"state"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return result{}, fmt.Errorf("decode reverse geocode response: %w", err)
	}
	if len(payload.Features) == 0 {
		return result{}, nil
	}
	p := payload.Features[0].Properties

	province := firstNonEmpty(p.State, p.County, p.City)
	// Concise admin address (ward → district → province), deduped.
	display := joinUnique(p.District, p.Locality, p.City, p.County, p.State)
	if display == "" {
		display = joinUnique(p.Name, p.Street)
	}
	return result{displayName: display, province: province}, nil
}

// joinUnique trims, drops empties + exact duplicates, and joins with ", ".
func joinUnique(vals ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ", ")
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
	if c.rateInterval <= 0 {
		return nil
	}
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
