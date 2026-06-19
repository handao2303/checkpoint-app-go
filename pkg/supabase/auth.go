package supabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidCredentials is returned when Supabase rejects the email/password.
var ErrInvalidCredentials = errors.New("invalid credentials")

// AuthClient calls Supabase GoTrue auth endpoints (password grant) using the
// project's anon/publishable key. Used to support username/email + password
// login server-side without exposing the user's email to the client.
type AuthClient struct {
	baseURL string
	anonKey string
	http    *http.Client
}

// NewAuthClient builds an auth client from the project URL and anon key.
func NewAuthClient(supabaseURL, anonKey string) *AuthClient {
	return &AuthClient{
		baseURL: strings.TrimRight(supabaseURL, "/"),
		anonKey: anonKey,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// PasswordGrant exchanges an email + password for a Supabase access token via
// the GoTrue password grant. Wrong credentials surface as ErrInvalidCredentials.
func (c *AuthClient) PasswordGrant(ctx context.Context, email, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/auth/v1/token?grant_type=password", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.anonKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("supabase password grant: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("supabase password grant: status %d", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode supabase token: %w", err)
	}
	if out.AccessToken == "" {
		return "", ErrInvalidCredentials
	}
	return out.AccessToken, nil
}
