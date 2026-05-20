package lfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// TransferResult holds the outcome of a single object transfer.
type TransferResult struct {
	OID     string
	Size    int64
	Skipped bool  // true if target already had the object
	Err     error // non-nil if the transfer failed
}

// Download retrieves an LFS object from the given URL, streaming it to the
// writer. It verifies the SHA-256 hash matches the expected OID.
func Download(ctx context.Context, httpClient *http.Client, href string, headers map[string]string, w io.Writer, expectedOID string, expectedSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return fmt.Errorf("lfs download: create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lfs download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lfs download: HTTP %d for %s", resp.StatusCode, href)
	}

	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)

	n, err := io.Copy(w, tee)
	if err != nil {
		return fmt.Errorf("lfs download: stream: %w", err)
	}
	if expectedSize > 0 && n != expectedSize {
		return fmt.Errorf("lfs download: size mismatch: got %d, want %d", n, expectedSize)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, expectedOID) {
		return fmt.Errorf("lfs download: OID mismatch: got %s, want %s", got, expectedOID)
	}
	return nil
}

// Upload sends an LFS object to the given URL, reading content from the reader.
func Upload(ctx context.Context, httpClient *http.Client, href string, headers map[string]string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, body)
	if err != nil {
		return fmt.Errorf("lfs upload: create request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lfs upload: %w", err)
	}
	defer resp.Body.Close()

	// Accept 200-299 range as success.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("lfs upload: HTTP %d for %s", resp.StatusCode, href)
	}
	// Drain body to allow connection reuse.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		// Log but don't fail since upload succeeded
		_ = err
	}
	return nil
}

// Verify confirms an uploaded object with the LFS server (optional step).
func Verify(ctx context.Context, httpClient *http.Client, href string, headers map[string]string, oid string, size int64) error {
	payload := struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}{OID: oid, Size: size}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("lfs verify: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, href, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("lfs verify: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lfs verify: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		// Log but don't fail if drain fails
		_ = err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("lfs verify: HTTP %d for %s", resp.StatusCode, href)
	}
	return nil
}
