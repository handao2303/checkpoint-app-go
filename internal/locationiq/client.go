// Package locationiq provides a lightweight client for the LocationIQ Nearby
// Places API that caches results by location to minimise quota usage.
package locationiq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// PlaceSuggestion is a nearby place returned to the check-in form for auto-fill.
type PlaceSuggestion struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	PlaceID  string `json:"place_id"`
}

// Client calls LocationIQ Nearby Places API and caches results by rounded location.
type Client struct {
	apiKey string
	redis  redis.Cmdable
	cache  map[string]cacheEntry
	mu     sync.RWMutex
	http   *http.Client
}

type cacheEntry struct {
	results   []PlaceSuggestion
	expiresAt time.Time
}

const (
	cacheTTL = time.Hour
	cacheMax = 500
	// Comma-separated OSM tags to query. Covers the app's 5 categories.
	// Note: LocationIQ's "tag" filter is currently unreliable, so we fetch all types and map them client-side.
	nearbyTags = "cafe,restaurant,fast_food,bar,pub,bakery,park,viewpoint,attraction"
	apiBase    = "https://us1.locationiq.com/v1/nearby"
)

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

// NewClient creates a Client. When apiKey is empty, Search always returns [].
func NewClient(apiKey string, rClient redis.Cmdable) *Client {
	var cmdable redis.Cmdable
	if !isNilCmdable(rClient) {
		cmdable = rClient
	}
	return &Client{
		apiKey: apiKey,
		redis:  cmdable,
		cache:  make(map[string]cacheEntry),
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

// typeToCategory maps OSM place types to the app's 5-category taxonomy.
var typeToCategory = map[string]string{
	"cafe":        "cafe",
	"coffee_shop": "cafe",
	"bakery":      "cafe",
	"restaurant":  "food",
	"fast_food":   "food",
	"bar":         "food",
	"pub":         "food",
	"food_court":  "food",
	"park":        "nature",
	"viewpoint":   "view",
	"attraction":  "view",
}

func mapCategory(osmType string) string {
	if cat, ok := typeToCategory[osmType]; ok {
		return cat
	}
	return "other"
}

func cacheKey(lat, lng float64) string {
	return fmt.Sprintf("%.3f,%.3f", lat, lng)
}

// Search returns up to 5 place suggestions near lat/lng.
// Always returns a non-nil slice; returns empty on any error or when apiKey is unset.
func (c *Client) Search(ctx context.Context, lat, lng float64) []PlaceSuggestion {
	if c.apiKey == "" {
		return []PlaceSuggestion{}
	}

	key := fmt.Sprintf("places:suggest:%s", cacheKey(lat, lng))

	if c.redis != nil {
		val, err := c.redis.Get(ctx, key).Result()
		if err == nil {
			var results []PlaceSuggestion
			if err := json.Unmarshal([]byte(val), &results); err == nil {
				return results
			}
		}
	} else {
		c.mu.RLock()
		entry, hit := c.cache[key]
		c.mu.RUnlock()
		if hit && time.Now().Before(entry.expiresAt) {
			return entry.results
		}
	}

	results := c.fetchFromAPI(ctx, lat, lng)

	if c.redis != nil {
		data, err := json.Marshal(results)
		if err == nil {
			_ = c.redis.Set(ctx, key, data, cacheTTL).Err()
		}
	} else {
		c.mu.Lock()
		if len(c.cache) >= cacheMax {
			for k := range c.cache {
				delete(c.cache, k)
				break
			}
		}
		c.cache[key] = cacheEntry{results: results, expiresAt: time.Now().Add(cacheTTL)}
		c.mu.Unlock()
	}

	return results
}

type nearbyPlace struct {
	PlaceID string `json:"place_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

func (c *Client) fetchFromAPI(ctx context.Context, lat, lng float64) []PlaceSuggestion {
	params := url.Values{
		"key":    {c.apiKey},
		"lat":    {fmt.Sprintf("%f", lat)},
		"lon":    {fmt.Sprintf("%f", lng)},
		"tag":    {nearbyTags},
		"radius": {"150"},
		"limit":  {"5"},
		"format": {"json"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"?"+params.Encode(), nil)
	if err != nil {
		return []PlaceSuggestion{}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return []PlaceSuggestion{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []PlaceSuggestion{}
	}

	var places []nearbyPlace
	if err := json.NewDecoder(resp.Body).Decode(&places); err != nil {
		return []PlaceSuggestion{}
	}

	suggestions := make([]PlaceSuggestion, 0, len(places))
	for _, p := range places {
		if p.Name == "" {
			continue
		}
		suggestions = append(suggestions, PlaceSuggestion{
			Name:     p.Name,
			Category: mapCategory(p.Type),
			PlaceID:  p.PlaceID,
		})
	}
	return suggestions
}
