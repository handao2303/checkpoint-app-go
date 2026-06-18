// Package pubsub publishes media-processing jobs to Google Cloud Pub/Sub using
// the REST API and the Cloud Run metadata-server access token — no GCP SDK
// dependency. It is nil-safe: a nil *Publisher (no topic configured) makes
// callers fall back to inline processing for local dev.
package pubsub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// MediaJob is the payload published when a checkpoint has media to process.
type MediaJob struct {
	CheckpointID string `json:"checkpoint_id"`
	UserID       string `json:"user_id"`
}

const (
	JobTypeProcessCheckpoint = "process_checkpoint"
	JobTypeMediaCleanup      = "media_cleanup"
)

// JobEnvelope is the typed payload used by newer worker jobs. Workers also
// accept legacy MediaJob payloads for checkpoint processing.
type JobEnvelope struct {
	Type              string           `json:"type"`
	ProcessCheckpoint *MediaJob        `json:"process_checkpoint,omitempty"`
	MediaCleanup      *MediaCleanupJob `json:"media_cleanup,omitempty"`
}

// MediaCleanupJob deletes orphaned object-storage keys after DB rows are gone.
type MediaCleanupJob struct {
	Keys []string `json:"keys"`
}

const (
	metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	pubsubPublishURL = "https://pubsub.googleapis.com/v1/projects/%s/topics/%s:publish"
)

// Publisher publishes to a single Pub/Sub topic.
type Publisher struct {
	projectID string
	topicID   string
	client    *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// New returns a Publisher, or nil if projectID/topicID are empty (local dev /
// Pub/Sub disabled). A nil Publisher is safe to call.
func New(projectID, topicID string) *Publisher {
	if projectID == "" || topicID == "" {
		return nil
	}
	return &Publisher{
		projectID: projectID,
		topicID:   topicID,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether the publisher will actually publish.
func (p *Publisher) Enabled() bool { return p != nil }

// PublishMedia publishes a MediaJob for the given checkpoint. On a nil Publisher
// it returns ErrDisabled so the caller can fall back to inline processing.
func (p *Publisher) PublishMedia(ctx context.Context, checkpointID, userID string) error {
	if p == nil {
		return ErrDisabled
	}
	payload, err := json.Marshal(MediaJob{CheckpointID: checkpointID, UserID: userID})
	if err != nil {
		return fmt.Errorf("marshal media job: %w", err)
	}
	return p.publish(ctx, payload)
}

// PublishMediaCleanup publishes a best-effort cleanup job for storage objects.
func (p *Publisher) PublishMediaCleanup(ctx context.Context, keys []string) error {
	if p == nil {
		return ErrDisabled
	}
	clean := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, key)
	}
	if len(clean) == 0 {
		return nil
	}
	payload, err := json.Marshal(JobEnvelope{
		Type:         JobTypeMediaCleanup,
		MediaCleanup: &MediaCleanupJob{Keys: clean},
	})
	if err != nil {
		return fmt.Errorf("marshal media cleanup job: %w", err)
	}
	return p.publish(ctx, payload)
}

func (p *Publisher) publish(ctx context.Context, payload []byte) error {
	token, err := p.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("get access token: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"data": base64.StdEncoding.EncodeToString(payload)},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal publish body: %w", err)
	}

	url := fmt.Sprintf(pubsubPublishURL, p.projectID, p.topicID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build publish request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("publish request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("pubsub publish failed: status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// ErrDisabled is returned by PublishMedia when the publisher is nil.
var ErrDisabled = fmt.Errorf("pubsub publisher disabled")

// accessToken fetches (and caches) an OAuth token from the Cloud Run metadata
// server. Cached until ~60s before expiry.
func (p *Publisher) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExpiry) {
		return p.token, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("metadata token failed: status %d: %s", resp.StatusCode, msg)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("metadata returned empty access token")
	}
	p.token = tok.AccessToken
	p.tokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 60*time.Second)
	return p.token, nil
}
