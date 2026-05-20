package lfsapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"ssh://git@github.com/user/repo.git", true},
		{"git+ssh://git@github.com/user/repo.git", true},
		{"git@github.com:user/repo.git", true},
		{"example.com:path/to/repo.git", true},
		{"https://github.com/user/repo.git", false},
		{"http://github.com/user/repo.git", false},
		{"git://github.com/user/repo.git", false},
		{"C:\\path\\to\\repo", false}, // Windows drive letter
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsSSHURL(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseSSHURL(t *testing.T) {
	tests := []struct {
		input   string
		want    *sshEndpointInfo
		wantErr bool
	}{
		{
			input: "ssh://git@github.com/user/repo.git",
			want:  &sshEndpointInfo{User: "git", Host: "github.com", Path: "/user/repo.git"},
		},
		{
			input: "ssh://github.com:2222/user/repo.git",
			want:  &sshEndpointInfo{Host: "github.com", Port: "2222", Path: "/user/repo.git"},
		},
		{
			input: "git+ssh://git@example.com/repo.git",
			want:  &sshEndpointInfo{User: "git", Host: "example.com", Path: "/repo.git"},
		},
		{
			input: "git@github.com:user/repo.git",
			want:  &sshEndpointInfo{User: "git", Host: "github.com", Path: "user/repo.git"},
		},
		{
			input: "example.com:path/repo.git",
			want:  &sshEndpointInfo{Host: "example.com", Path: "path/repo.git"},
		},
		{
			input:   "https://github.com/user/repo.git",
			wantErr: true,
		},
		{
			input:   "ssh://",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseSSHURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.User, got.User)
			assert.Equal(t, tt.want.Host, got.Host)
			assert.Equal(t, tt.want.Port, got.Port)
			assert.Equal(t, tt.want.Path, got.Path)
		})
	}
}

func TestSSHEndpointAuth(t *testing.T) {
	t.Run("bearer token", func(t *testing.T) {
		ep := &SSHEndpoint{
			Href:   "https://lfs.example.com/user/repo.git/info/lfs",
			Header: map[string]string{"Authorization": "Bearer abc123"},
		}
		auth := ep.Auth()
		assert.Equal(t, "abc123", auth.BearerToken)
		assert.Empty(t, auth.Username)
		assert.Empty(t, auth.Password)
	})

	t.Run("no auth header", func(t *testing.T) {
		ep := &SSHEndpoint{
			Href:   "https://lfs.example.com/user/repo.git/info/lfs",
			Header: map[string]string{},
		}
		auth := ep.Auth()
		assert.Empty(t, auth.BearerToken)
	})

	t.Run("nil endpoint", func(t *testing.T) {
		var ep *SSHEndpoint
		auth := ep.Auth()
		assert.Empty(t, auth.BearerToken)
	})
}

func TestSSHAuthenticate(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "ssh.log")
	script := filepath.Join(dir, "ssh-lfs-shim.sh")

	// This shim simulates an SSH connection that runs git-lfs-authenticate.
	// It captures args for verification and returns a JSON response.
	shimContent := strings.Join([]string{
		"#!/bin/sh",
		"# Log all arguments for verification",
		"printf '%s\\n' \"$@\" >>" + shellQuote(logFile),
		"# The last argument is the remote command",
		"# Extract the remote command args",
		"for last; do true; done",
		"# Return a valid JSON LFS authenticate response",
		`printf '{"href":"https://lfs.example.com/user/repo.git/info/lfs","header":{"Authorization":"Bearer ssh-token-123"}}'`,
	}, "\n")
	if err := os.WriteFile(script, []byte(shimContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	orig := SSHLookPath
	t.Cleanup(func() { SSHLookPath = orig })
	SSHLookPath = func(string) (string, error) { return script, nil }

	ep, err := SSHAuthenticate(context.Background(), "ssh://git@github.com/user/repo.git", "download")
	require.NoError(t, err)
	require.NotNil(t, ep)
	assert.Equal(t, "https://lfs.example.com/user/repo.git/info/lfs", ep.Href)
	assert.Equal(t, "Bearer ssh-token-123", ep.Header["Authorization"])

	// Verify auth extraction
	auth := ep.Auth()
	assert.Equal(t, "ssh-token-123", auth.BearerToken)

	// Verify the SSH invocation args
	data, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	// Should contain: -o, BatchMode=yes, git@github.com, remote command
	assert.Contains(t, lines, "-o")
	assert.Contains(t, lines, "BatchMode=yes")
	assert.Contains(t, lines, "git@github.com")
	// The last arg should be the remote command
	lastArg := lines[len(lines)-1]
	assert.Contains(t, lastArg, "git-lfs-authenticate")
	assert.Contains(t, lastArg, "/user/repo.git")
	assert.Contains(t, lastArg, "download")
}

func TestSSHAuthenticateSCPStyle(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh-lfs-shim.sh")

	shimContent := strings.Join([]string{
		"#!/bin/sh",
		`printf '{"href":"https://lfs.example.com/info/lfs","header":{"Authorization":"Bearer scp-token"}}'`,
	}, "\n")
	if err := os.WriteFile(script, []byte(shimContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	orig := SSHLookPath
	t.Cleanup(func() { SSHLookPath = orig })
	SSHLookPath = func(string) (string, error) { return script, nil }

	ep, err := SSHAuthenticate(context.Background(), "git@github.com:user/repo.git", "upload")
	require.NoError(t, err)
	require.NotNil(t, ep)
	assert.Equal(t, "https://lfs.example.com/info/lfs", ep.Href)
	assert.Equal(t, "scp-token", ep.Auth().BearerToken)
}

func TestSSHAuthenticateError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh-fail.sh")

	shimContent := strings.Join([]string{
		"#!/bin/sh",
		"echo 'connection refused' >&2",
		"exit 1",
	}, "\n")
	if err := os.WriteFile(script, []byte(shimContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	orig := SSHLookPath
	t.Cleanup(func() { SSHLookPath = orig })
	SSHLookPath = func(string) (string, error) { return script, nil }

	_, err := SSHAuthenticate(context.Background(), "ssh://git@github.com/user/repo.git", "download")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestSSHAuthenticateInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh-bad-json.sh")

	shimContent := strings.Join([]string{
		"#!/bin/sh",
		"echo 'not json'",
	}, "\n")
	if err := os.WriteFile(script, []byte(shimContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	orig := SSHLookPath
	t.Cleanup(func() { SSHLookPath = orig })
	SSHLookPath = func(string) (string, error) { return script, nil }

	_, err := SSHAuthenticate(context.Background(), "ssh://git@github.com/user/repo.git", "download")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}

func TestSSHAuthenticateEmptyHref(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh-empty-href.sh")

	resp, _ := json.Marshal(SSHEndpoint{Href: "", Header: map[string]string{}})
	shimContent := strings.Join([]string{
		"#!/bin/sh",
		"printf '" + string(resp) + "'",
	}, "\n")
	if err := os.WriteFile(script, []byte(shimContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	orig := SSHLookPath
	t.Cleanup(func() { SSHLookPath = orig })
	SSHLookPath = func(string) (string, error) { return script, nil }

	_, err := SSHAuthenticate(context.Background(), "ssh://git@github.com/user/repo.git", "download")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty href")
}
