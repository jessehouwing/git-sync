package lfsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// MediaType is the content type for LFS API requests.
	MediaType = "application/vnd.git-lfs+json"
	// batchPath is appended to the LFS server URL.
	batchPath = "/objects/batch"
	// maxBatchSize is the maximum number of objects per batch request.
	maxBatchSize = 100
)

// Client communicates with a Git LFS server's batch API.
type Client struct {
	httpClient *http.Client
	endpoint   string // LFS API base URL (e.g., https://host/repo.git/info/lfs)
	auth       Auth
}

// Auth holds authentication for LFS API requests.
type Auth struct {
	// Username and Password for Basic auth.
	Username string
	Password string
	// BearerToken for Bearer auth (takes precedence over Basic).
	BearerToken string
	// Header contains additional headers to set on each request.
	// Used by SSH-based LFS authentication to pass through arbitrary
	// auth headers from git-lfs-authenticate responses.
	Header map[string]string
}

// NewClient creates an LFS batch API client.
func NewClient(httpClient *http.Client, endpoint string, auth Auth) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
		endpoint:   strings.TrimSuffix(endpoint, "/"),
		auth:       auth,
	}
}

// Batch sends a batch request to the LFS server.
func (c *Client) Batch(ctx context.Context, req BatchRequest) (*BatchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("lfs batch: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+batchPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("lfs batch: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", MediaType)
	httpReq.Header.Set("Accept", MediaType)
	c.applyAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lfs batch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var batchResp BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&batchResp); err != nil {
		return nil, fmt.Errorf("lfs batch: decode response: %w", err)
	}
	return &batchResp, nil
}

// BatchDownload sends a download batch request.
func (c *Client) BatchDownload(ctx context.Context, objects []ObjectSpec) (*BatchResponse, error) {
	return c.Batch(ctx, BatchRequest{
		Operation: OperationDownload,
		Transfers: []string{"basic"},
		Objects:   objects,
	})
}

// BatchUpload sends an upload batch request.
func (c *Client) BatchUpload(ctx context.Context, objects []ObjectSpec) (*BatchResponse, error) {
	return c.Batch(ctx, BatchRequest{
		Operation: OperationUpload,
		Transfers: []string{"basic"},
		Objects:   objects,
	})
}

// MaxBatchSize returns the maximum number of objects per batch request.
func MaxBatchSize() int {
	return maxBatchSize
}

func (c *Client) applyAuth(req *http.Request) {
	if c.auth.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.auth.BearerToken)
	} else if c.auth.Username != "" || c.auth.Password != "" {
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	}
	for k, v := range c.auth.Header {
		req.Header.Set(k, v)
	}
}

func (c *Client) handleErrorResponse(resp *http.Response) error {
	limited := io.LimitReader(resp.Body, 64*1024)
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("lfs batch: read error response: %w", err)
	}

	var errResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
		return fmt.Errorf("lfs batch: HTTP %d: %s", resp.StatusCode, errResp.Message)
	}
	if len(body) > 0 {
		return fmt.Errorf("lfs batch: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return fmt.Errorf("lfs batch: HTTP %d", resp.StatusCode)
}

// EndpointFromRepoURL derives the LFS API endpoint URL from a git remote URL.
// It handles both URLs ending in .git and those without.
// Returns an error for non-HTTP(S) URLs (e.g., ssh://, git://) since LFS
// over those protocols requires a different auth flow.
func EndpointFromRepoURL(repoURL string) (string, error) {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("lfs endpoint: invalid URL %q: %w", repoURL, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		// OK
	case "":
		return "", fmt.Errorf("lfs endpoint: missing URL scheme in %q", repoURL)
	default:
		return "", fmt.Errorf("lfs endpoint: unsupported scheme %q in %q; LFS requires HTTP(S)", parsed.Scheme, repoURL)
	}

	u := strings.TrimSuffix(repoURL, "/")
	if !strings.HasSuffix(u, ".git") {
		u += ".git"
	}
	return u + "/info/lfs", nil
}
