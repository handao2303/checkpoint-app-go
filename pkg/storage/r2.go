// Package storage provides a Cloudflare R2 object-storage client.
//
// R2 is S3-compatible, so this is a minimal AWS Signature V4 implementation
// (PutObject + DeleteObject) using only the standard library — no AWS SDK.
package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	r2Region  = "auto"
	r2Service = "s3"
)

// R2Config holds the credentials and bucket settings for an R2 client.
type R2Config struct {
	AccountID string
	AccessKey string
	SecretKey string
	Bucket    string
	PublicURL string // public base URL objects are served from
}

// R2 uploads objects to a Cloudflare R2 bucket.
type R2 struct {
	cfg    R2Config
	host   string
	client *http.Client
}

// NewR2 builds an R2 client.
func NewR2(cfg R2Config) *R2 {
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return &R2{
		cfg:    cfg,
		host:   cfg.AccountID + ".r2.cloudflarestorage.com",
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// PublicURL joins a public base URL and an object key. If key is already an
// absolute URL (e.g. legacy rows that stored the full URL), it's returned as-is.
func PublicURL(base, key string) string {
	if key == "" || strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
		return key
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(key, "/")
}

// Upload PUTs body under key and returns the stored object key (not the full
// URL — callers build the public URL from R2_PUBLIC_URL at read time). The
// whole body is buffered because SigV4 signs a hash of the payload.
func (c *R2) Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read upload body: %w", err)
	}

	canonicalURI := "/" + c.cfg.Bucket + "/" + uriEncodePath(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"https://"+c.host+canonicalURI, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build upload request: %w", err)
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := hexSHA256(payload)

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.ContentLength = int64(len(payload))
	req.Header.Set("Authorization",
		c.authorization(http.MethodPut, canonicalURI, contentType, payloadHash, amzDate, dateStamp))

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload to r2: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("r2 upload failed: status %d: %s", resp.StatusCode, msg)
	}
	return key, nil
}

// Delete removes the object stored under key. Deleting a missing object is not
// an error (S3 DELETE is idempotent and returns 204 either way).
func (c *R2) Delete(ctx context.Context, key string) error {
	canonicalURI := "/" + c.cfg.Bucket + "/" + uriEncodePath(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		"https://"+c.host+canonicalURI, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := hexSHA256(nil) // empty body

	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization",
		c.authorization(http.MethodDelete, canonicalURI, "", payloadHash, amzDate, dateStamp))

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete from r2: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("r2 delete failed: status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// PresignUpload returns a presigned PUT URL valid for ttlSeconds. The browser
// can PUT any body directly to R2 without an Authorization header — the
// signature is embedded in the query string. SignedHeaders = host only, so the
// browser may send any Content-Type it likes (R2 stores it verbatim).
func (c *R2) PresignUpload(key string, ttlSeconds int) string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"
	canonicalURI := "/" + c.cfg.Bucket + "/" + uriEncodePath(key)

	// url.Values.Encode() sorts keys alphabetically — required for canonical form.
	q := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {c.cfg.AccessKey + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.Itoa(ttlSeconds)},
		"X-Amz-SignedHeaders": {"host"},
	}
	canonicalQueryString := q.Encode()

	canonicalRequest := strings.Join([]string{
		"PUT", canonicalURI, canonicalQueryString,
		"host:" + c.host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	sigKey := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), []byte(dateStamp))
	sigKey = hmacSHA256(sigKey, []byte(r2Region))
	sigKey = hmacSHA256(sigKey, []byte(r2Service))
	sigKey = hmacSHA256(sigKey, []byte("aws4_request"))
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(sigKey, []byte(stringToSign))))

	return "https://" + c.host + canonicalURI + "?" + q.Encode()
}

// authorization builds the SigV4 Authorization header value. contentType may be
// empty (e.g. DELETE requests), in which case it's left out of the signature.
func (c *R2) authorization(method, canonicalURI, contentType, payloadHash, amzDate, dateStamp string) string {
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + c.host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if contentType != "" {
		signedHeaders = "content-type;" + signedHeaders
		canonicalHeaders = "content-type:" + contentType + "\n" + canonicalHeaders
	}

	canonicalRequest := strings.Join([]string{
		method, canonicalURI, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	key := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), []byte(dateStamp))
	key = hmacSHA256(key, []byte(r2Region))
	key = hmacSHA256(key, []byte(r2Service))
	key = hmacSHA256(key, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	return "AWS4-HMAC-SHA256 " +
		"Credential=" + c.cfg.AccessKey + "/" + scope + ", " +
		"SignedHeaders=" + signedHeaders + ", " +
		"Signature=" + signature
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// uriEncodePath percent-encodes each path segment per RFC 3986 while keeping
// the "/" separators, as required by S3's canonical URI.
func uriEncodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
