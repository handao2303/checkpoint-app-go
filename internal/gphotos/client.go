package gphotos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL    = "https://oauth2.googleapis.com/token"
	albumsURL   = "https://photoslibrary.googleapis.com/v1/albums"
	uploadURL   = "https://uploadphotos.googleapis.com/v1/uploads"
	batchURL    = "https://photoslibrary.googleapis.com/v1/mediaItems:batchCreate"
	albumName   = "Checkpoint App"
	photosScope = "https://www.googleapis.com/auth/photoslibrary.appendonly"

	refreshBuffer = time.Minute
)

// Client handles Google OAuth2 and Photos Library API calls.
type Client struct {
	clientID     string
	clientSecret string
	http         *http.Client
}

// NewClient builds a Photos API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 60 * time.Second},
	}
}

// BuildAuthURL returns the Google OAuth2 authorization URL.
func (c *Client) BuildAuthURL(state, redirectURI string) string {
	params := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"openid email " + photosScope},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {state},
	}
	return authURL + "?" + params.Encode()
}

type rawTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// Exchange exchanges an authorization code for tokens.
func (c *Client) Exchange(ctx context.Context, userID, code, redirectURI string) (*Tokens, error) {
	raw, err := c.postToken(ctx, url.Values{
		"code":          {code},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return nil, err
	}
	t := &Tokens{
		UserID:       userID,
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		TokenType:    raw.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
	}
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	return t, nil
}

// RefreshIfNeeded refreshes the access token when it is about to expire.
func (c *Client) RefreshIfNeeded(ctx context.Context, t *Tokens) (*Tokens, bool, error) {
	if time.Now().Before(t.ExpiresAt.Add(-refreshBuffer)) {
		return t, false, nil
	}
	raw, err := c.postToken(ctx, url.Values{
		"refresh_token": {t.RefreshToken},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return t, false, fmt.Errorf("refresh token: %w", err)
	}
	updated := *t
	updated.AccessToken = raw.AccessToken
	updated.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	if raw.RefreshToken != "" {
		updated.RefreshToken = raw.RefreshToken
	}
	return &updated, true, nil
}

func (c *Client) postToken(ctx context.Context, params url.Values) (*rawTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var result rawTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("google token error: %s — %s", result.Error, result.ErrorDesc)
	}
	return &result, nil
}

// EnsureAlbum finds the "Checkpoint App" Photos album (app-created only), creating
// it if not found. Only albums created by this app are visible with appendonly scope.
func (c *Client) EnsureAlbum(ctx context.Context, accessToken string) (string, error) {
	// Paginate through app-created albums to find "Checkpoint App".
	pageToken := ""
	for {
		u := albumsURL + "?pageSize=50&excludeNonAppCreatedData=true"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := c.http.Do(req)
		if err != nil {
			return "", fmt.Errorf("list albums: %w", err)
		}
		var listResp struct {
			Albums []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"albums"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return "", fmt.Errorf("decode album list: %w", err)
		}
		resp.Body.Close()

		for _, a := range listResp.Albums {
			if a.Title == albumName {
				return a.ID, nil
			}
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}

	// Create the album.
	body, _ := json.Marshal(map[string]any{
		"album": map[string]string{"title": albumName},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, albumsURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("create album: %w", err)
	}
	defer resp.Body.Close()

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("decode create album: %w", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("create album: empty id in response")
	}
	return created.ID, nil
}

// UploadMedia fetches the media from mediaURL, uploads it to Google Photos
// under albumID, and returns the created media item ID.
// description is embedded as the media item description (max 1000 chars).
func (c *Client) UploadMedia(ctx context.Context, accessToken, albumID, filename, mediaURL, mediaType, description string) (string, error) {
	// Fetch media from R2.
	imgReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", err
	}
	imgResp, err := c.http.Do(imgReq)
	if err != nil {
		return "", fmt.Errorf("fetch media: %w", err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch media status %d", imgResp.StatusCode)
	}

	mediaBytes, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return "", fmt.Errorf("read media: %w", err)
	}

	// Step 1: upload raw bytes to get an upload token.
	mimeType := imgResp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
		if mediaType == "video" {
			mimeType = "video/mp4"
		}
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(mediaBytes))
	if err != nil {
		return "", err
	}
	upReq.Header.Set("Authorization", "Bearer "+accessToken)
	upReq.Header.Set("Content-Type", mimeType)
	upReq.Header.Set("X-Goog-Upload-Protocol", "raw")
	upReq.Header.Set("X-Goog-Upload-File-Name", filename)

	upResp, err := c.http.Do(upReq)
	if err != nil {
		return "", fmt.Errorf("upload bytes: %w", err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		b, _ := io.ReadAll(upResp.Body)
		return "", fmt.Errorf("upload bytes status %d: %s", upResp.StatusCode, b)
	}
	tokenBytes, err := io.ReadAll(upResp.Body)
	if err != nil {
		return "", fmt.Errorf("read upload token: %w", err)
	}
	uploadToken := strings.TrimSpace(string(tokenBytes))
	if uploadToken == "" {
		return "", fmt.Errorf("empty upload token")
	}

	// Truncate description to Photos API limit.
	if len(description) > 1000 {
		description = description[:1000]
	}

	// Step 2: create media item in the album.
	batchBody, _ := json.Marshal(map[string]any{
		"albumId": albumID,
		"newMediaItems": []map[string]any{
			{
				"description": description,
				"simpleMediaItem": map[string]string{
					"uploadToken": uploadToken,
					"fileName":    filename,
				},
			},
		},
	})
	batchReq, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, bytes.NewReader(batchBody))
	if err != nil {
		return "", err
	}
	batchReq.Header.Set("Authorization", "Bearer "+accessToken)
	batchReq.Header.Set("Content-Type", "application/json")

	batchResp, err := c.http.Do(batchReq)
	if err != nil {
		return "", fmt.Errorf("create media item: %w", err)
	}
	defer batchResp.Body.Close()
	if batchResp.StatusCode < 200 || batchResp.StatusCode >= 300 {
		b, _ := io.ReadAll(batchResp.Body)
		return "", fmt.Errorf("create media item status %d: %s", batchResp.StatusCode, b)
	}

	var batchResult struct {
		NewMediaItemResults []struct {
			MediaItem struct {
				ID string `json:"id"`
			} `json:"mediaItem"`
			Status struct {
				Message string `json:"message"`
			} `json:"status"`
		} `json:"newMediaItemResults"`
	}
	if err := json.NewDecoder(batchResp.Body).Decode(&batchResult); err != nil {
		return "", fmt.Errorf("decode batch create: %w", err)
	}
	if len(batchResult.NewMediaItemResults) == 0 {
		return "", fmt.Errorf("no media item in response")
	}
	return batchResult.NewMediaItemResults[0].MediaItem.ID, nil
}
