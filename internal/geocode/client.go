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
	cache   map[string]string

	rateMu      sync.Mutex
	lastRequest time.Time
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
		cache:        make(map[string]string),
	}
}

// Reverse returns Nominatim's display_name for the given coordinates.
func (c *Client) Reverse(ctx context.Context, lat, lng float64) (string, error) {
	if !c.enabled {
		return "", nil
	}

	key := fmt.Sprintf("%.6f,%.6f", lat, lng)
	c.cacheMu.RLock()
	address, ok := c.cache[key]
	c.cacheMu.RUnlock()
	if ok {
		return address, nil
	}

	if err := c.waitForRateLimit(ctx); err != nil {
		return "", err
	}

	endpoint, err := url.Parse(c.baseURL + "/reverse")
	if err != nil {
		return "", fmt.Errorf("parse geocoder URL: %w", err)
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
		return "", fmt.Errorf("build geocoder request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("reverse geocode request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reverse geocode status: %s", res.Status)
	}

	var payload struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode reverse geocode response: %w", err)
	}
	address = strings.TrimSpace(payload.DisplayName)
	if address == "" {
		return "", nil
	}

	c.cacheMu.Lock()
	c.cache[key] = address
	c.cacheMu.Unlock()
	return address, nil
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
