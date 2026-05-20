package lfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownload(t *testing.T) {
	content := []byte("hello world lfs content")
	hash := sha256.Sum256(content)
	oid := hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "custom-value", r.Header.Get("X-Custom"))
		if _, err := w.Write(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	var buf bytes.Buffer
	err := Download(context.Background(), server.Client(), server.URL, map[string]string{"X-Custom": "custom-value"}, &buf, oid, int64(len(content)))
	require.NoError(t, err)
	assert.Equal(t, content, buf.Bytes())
}

func TestDownload_OIDMismatch(t *testing.T) {
	content := []byte("hello world lfs content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	var buf bytes.Buffer
	err := Download(context.Background(), server.Client(), server.URL, nil, &buf, "0000000000000000000000000000000000000000000000000000000000000000", int64(len(content)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OID mismatch")
}

func TestDownload_SizeMismatch(t *testing.T) {
	content := []byte("hello")
	hash := sha256.Sum256(content)
	oid := hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	var buf bytes.Buffer
	err := Download(context.Background(), server.Client(), server.URL, nil, &buf, oid, 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size mismatch")
}

func TestUpload(t *testing.T) {
	content := []byte("upload this content")
	var received []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		received = buf.Bytes()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := Upload(context.Background(), server.Client(), server.URL, nil, bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)
	assert.Equal(t, content, received)
}

func TestUpload_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := Upload(context.Background(), server.Client(), server.URL, nil, bytes.NewReader([]byte("data")), 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestVerify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/vnd.git-lfs+json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := Verify(context.Background(), server.Client(), server.URL, nil, "someoid", 123)
	require.NoError(t, err)
}
