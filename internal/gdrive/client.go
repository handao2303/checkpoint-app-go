package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const (
	authURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL    = "https://oauth2.googleapis.com/token"
	userInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	filesURL    = "https://www.googleapis.com/drive/v3/files"
	uploadURL   = "https://www.googleapis.com/upload/drive/v3/files"
	folderMIME  = "application/vnd.google-apps.folder"
	folderName  = "Checkpoint App"
	driveScope  = "https://www.googleapis.com/auth/drive.file"

	// refreshBuffer refreshes the token this long before it expires.
	refreshBuffer = time.Minute
)

// Client handles Google OAuth2 token exchange and Drive API calls.
type Client struct {
	clientID     string
	clientSecret string
	http         *http.Client
}

// NewClient builds a Drive API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 30 * time.Second},
	}
}

// BuildAuthURL returns the Google OAuth2 authorization URL with the given
// state (a signed JWT containing the user ID) and redirectURI.
func (c *Client) BuildAuthURL(state, redirectURI string) string {
	params := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"openid email " + driveScope},
		"access_type":   {"offline"},
		"prompt":        {"consent"}, // force refresh_token on every connect
		"state":         {state},
	}
	return authURL + "?" + params.Encode()
}

// rawTokenResponse is the token exchange / refresh response from Google.
type rawTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// Exchange exchanges an authorization code for tokens and resolves the user's
// Google account email via the userinfo endpoint.
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

	email, err := c.fetchEmail(ctx, raw.AccessToken)
	if err != nil {
		email = "" // non-fatal — still save the tokens
	}

	t := &Tokens{
		UserID:       userID,
		Email:        email,
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
// Returns (updated tokens, changed, error). If unchanged, the original pointer is returned.
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

// postToken posts to Google's token endpoint and returns the raw response.
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

// fetchEmail calls the userinfo endpoint to retrieve the user's Google email.
func (c *Client) fetchEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.Email, nil
}

// EnsureFolder finds the "Checkpoint App" Drive folder for the user, creating it
// if it doesn't exist. Returns the folder ID.
func (c *Client) EnsureFolder(ctx context.Context, accessToken string) (string, error) {
	// Search for existing folder.
	q := url.Values{
		"q":      {fmt.Sprintf("name='%s' and mimeType='%s' and trashed=false", folderName, folderMIME)},
		"fields": {"files(id)"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, filesURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("list folders: %w", err)
	}
	defer resp.Body.Close()

	var listResp struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return "", fmt.Errorf("decode folder list: %w", err)
	}
	if len(listResp.Files) > 0 {
		return listResp.Files[0].ID, nil
	}

	// Create the folder.
	body, _ := json.Marshal(map[string]any{
		"name":     folderName,
		"mimeType": folderMIME,
	})
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, filesURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("create folder: %w", err)
	}
	defer resp.Body.Close()

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("decode create folder: %w", err)
	}
	return created.ID, nil
}

// UploadImage fetches an image from imageURL and uploads it to Drive under
// folderID. metadata is embedded in the Drive file description as JSON.
func (c *Client) UploadImage(ctx context.Context, accessToken, folderID, filename, imageURL string, metadata map[string]string) (string, error) {
	// Fetch image from R2.
	imgReq, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	imgResp, err := c.http.Do(imgReq)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch image status %d", imgResp.StatusCode)
	}

	// Build multipart body: part 1 = metadata JSON, part 2 = image bytes.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Part 1: file metadata.
	metaPart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"application/json; charset=UTF-8"},
	})
	metaJSON, _ := json.Marshal(map[string]any{
		"name":          filename,
		"parents":       []string{folderID},
		"description":   jsonString(metadata),
		"appProperties": metadata,
	})
	_, _ = metaPart.Write(metaJSON)

	// Part 2: image bytes.
	contentType := imgResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	imgPart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {contentType},
	})
	if _, err := io.Copy(imgPart, imgResp.Body); err != nil {
		return "", fmt.Errorf("buffer image: %w", err)
	}
	_ = mw.Close()

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		uploadURL+"?uploadType=multipart&fields=id", &buf)
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Authorization", "Bearer "+accessToken)
	uploadReq.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())

	uploadResp, err := c.http.Do(uploadReq)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		body, _ := io.ReadAll(uploadResp.Body)
		return "", fmt.Errorf("upload status %d: %s", uploadResp.StatusCode, body)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	return result.ID, nil
}

// jsonString serialises m to a compact JSON string for Drive file description.
func jsonString(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}
