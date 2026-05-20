package lfsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// SSHLookPath is replaceable in tests.
var SSHLookPath = exec.LookPath

// SSHEndpoint holds the LFS API endpoint and authentication obtained
// from the remote via git-lfs-authenticate over SSH.
type SSHEndpoint struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

// Auth returns an Auth value built from the SSH authenticate response headers.
func (e *SSHEndpoint) Auth() Auth {
	if e == nil || len(e.Header) == 0 {
		return Auth{}
	}
	authHeader := e.Header["Authorization"]
	if authHeader == "" {
		// No Authorization header — pass through all headers as raw headers.
		if len(e.Header) > 0 {
			return Auth{Header: e.Header}
		}
		return Auth{}
	}
	if strings.HasPrefix(authHeader, "Bearer ") {
		return Auth{BearerToken: strings.TrimPrefix(authHeader, "Bearer ")}
	}
	// Non-Bearer auth (e.g., RemoteAuth, Basic) — pass the full header map.
	return Auth{Header: e.Header}
}

// SSHAuthenticate runs `git-lfs-authenticate <path> <operation>` over SSH
// and returns the LFS endpoint and credentials. This is the standard mechanism
// for Git LFS to obtain credentials when the remote uses SSH transport.
//
// The operation should be "download" or "upload".
func SSHAuthenticate(ctx context.Context, repoURL string, operation string) (*SSHEndpoint, error) {
	ep, err := parseSSHURL(repoURL)
	if err != nil {
		return nil, fmt.Errorf("lfs ssh authenticate: %w", err)
	}

	sshPath, err := SSHLookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("lfs ssh authenticate: locate ssh binary: %w", err)
	}

	path := ep.Path
	// Absolute and relative paths are handled identically for git-lfs-authenticate.
	// The remote Git LFS server will interpret the path according to its configuration.

	remoteCmd := "git-lfs-authenticate " + shellQuote(path) + " " + operation

	args := []string{"-o", "BatchMode=yes"}
	if ep.Port != "" {
		args = append(args, "-p", ep.Port)
	}
	args = append(args, ep.Destination(), remoteCmd)

	cmd := exec.CommandContext(ctx, sshPath, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return nil, fmt.Errorf("lfs ssh authenticate: %w: %s", err, stderr)
			}
		}
		return nil, fmt.Errorf("lfs ssh authenticate: %w", err)
	}

	var endpoint SSHEndpoint
	if err := json.Unmarshal(output, &endpoint); err != nil {
		return nil, fmt.Errorf("lfs ssh authenticate: parse response: %w", err)
	}
	if endpoint.Href == "" {
		return nil, errors.New("lfs ssh authenticate: empty href in response")
	}
	return &endpoint, nil
}

// sshEndpointInfo holds the parsed SSH URL components needed to invoke ssh.
type sshEndpointInfo struct {
	User string
	Host string
	Port string
	Path string
}

// Destination returns the ssh destination in user@host format.
func (e *sshEndpointInfo) Destination() string {
	if e.User != "" {
		return e.User + "@" + e.Host
	}
	return e.Host
}

// parseSSHURL parses an SSH URL into its components.
// Supports:
//   - ssh://[user@]host[:port]/path
//   - git+ssh://[user@]host[:port]/path
//   - [user@]host:path (SCP-style)
func parseSSHURL(rawURL string) (*sshEndpointInfo, error) {
	// Try SCP-style first: [user@]host:path (no scheme, has colon before path)
	if !strings.Contains(rawURL, "://") {
		return parseSCPStyle(rawURL)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH URL %q: %w", rawURL, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "ssh", "git+ssh":
		// OK
	default:
		return nil, fmt.Errorf("unsupported scheme %q in %q for SSH LFS", parsed.Scheme, rawURL)
	}

	info := &sshEndpointInfo{
		Host: parsed.Hostname(),
		Port: parsed.Port(),
		Path: parsed.Path,
	}
	if parsed.User != nil {
		info.User = parsed.User.Username()
	}
	if info.Host == "" {
		return nil, fmt.Errorf("missing host in SSH URL %q", rawURL)
	}
	if info.Path == "" {
		return nil, fmt.Errorf("missing path in SSH URL %q", rawURL)
	}
	return info, nil
}

// parseSCPStyle parses [user@]host:path.
func parseSCPStyle(rawURL string) (*sshEndpointInfo, error) {
	// Must have a colon that separates host from path.
	colonIdx := strings.IndexByte(rawURL, ':')
	if colonIdx < 0 {
		return nil, fmt.Errorf("invalid SCP-style URL %q: missing colon", rawURL)
	}

	hostPart := rawURL[:colonIdx]
	path := rawURL[colonIdx+1:]

	if path == "" {
		return nil, fmt.Errorf("invalid SCP-style URL %q: empty path", rawURL)
	}

	info := &sshEndpointInfo{Path: path}
	if atIdx := strings.LastIndexByte(hostPart, '@'); atIdx >= 0 {
		info.User = hostPart[:atIdx]
		info.Host = hostPart[atIdx+1:]
	} else {
		info.Host = hostPart
	}

	if info.Host == "" {
		return nil, fmt.Errorf("invalid SCP-style URL %q: empty host", rawURL)
	}
	return info, nil
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// IsSSHURL returns true if the URL uses an SSH-based scheme (ssh://, git+ssh://)
// or SCP-style syntax (user@host:path).
func IsSSHURL(rawURL string) bool {
	if !strings.Contains(rawURL, "://") {
		// SCP-style: [user@]host:path — must have colon and non-empty host
		colonIdx := strings.IndexByte(rawURL, ':')
		if colonIdx > 0 {
			hostPart := rawURL[:colonIdx]
			// Exclude Windows drive letters like C:\...
			if len(hostPart) == 1 && hostPart[0] >= 'A' && hostPart[0] <= 'Z' {
				return false
			}
			if len(hostPart) == 1 && hostPart[0] >= 'a' && hostPart[0] <= 'z' {
				return false
			}
			return true
		}
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "ssh" || scheme == "git+ssh"
}
