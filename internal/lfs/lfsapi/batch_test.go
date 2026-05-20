package lfsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/objects/batch", r.URL.Path)
		assert.Equal(t, MediaType, r.Header.Get("Content-Type"))
		assert.Equal(t, MediaType, r.Header.Get("Accept"))

		var req BatchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, OperationDownload, req.Operation)
		assert.Len(t, req.Objects, 1)
		assert.Equal(t, "abc123", req.Objects[0].OID)

		resp := BatchResponse{
			Transfer: "basic",
			Objects: []ObjectResponse{
				{
					OID:  "abc123",
					Size: 1024,
					Actions: &ObjectActions{
						Download: &Link{
							Href:   "https://storage.example.com/abc123",
							Header: map[string]string{"Authorization": "Bearer token"},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", MediaType)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, Auth{})
	resp, err := client.BatchDownload(context.Background(), []ObjectSpec{
		{OID: "abc123", Size: 1024},
	})

	require.NoError(t, err)
	require.Len(t, resp.Objects, 1)
	assert.Equal(t, "abc123", resp.Objects[0].OID)
	assert.NotNil(t, resp.Objects[0].Actions.Download)
	assert.Equal(t, "https://storage.example.com/abc123", resp.Objects[0].Actions.Download.Href)
}

func TestBatchUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req BatchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, OperationUpload, req.Operation)

		resp := BatchResponse{
			Objects: []ObjectResponse{
				{
					OID:  req.Objects[0].OID,
					Size: req.Objects[0].Size,
					Actions: &ObjectActions{
						Upload: &Link{
							Href: "https://storage.example.com/upload/abc123",
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", MediaType)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, Auth{})
	resp, err := client.BatchUpload(context.Background(), []ObjectSpec{
		{OID: "abc123", Size: 1024},
	})

	require.NoError(t, err)
	require.Len(t, resp.Objects, 1)
	assert.NotNil(t, resp.Objects[0].Actions.Upload)
}

func TestBatchAuth(t *testing.T) {
	t.Run("bearer token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer mytoken", r.Header.Get("Authorization"))
			resp := BatchResponse{Objects: []ObjectResponse{}}
			w.Header().Set("Content-Type", MediaType)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}))
		defer server.Close()

		client := NewClient(server.Client(), server.URL, Auth{BearerToken: "mytoken"})
		_, err := client.BatchDownload(context.Background(), []ObjectSpec{{OID: "x", Size: 1}})
		require.NoError(t, err)
	})

	t.Run("basic auth", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "user", user)
			assert.Equal(t, "pass", pass)
			resp := BatchResponse{Objects: []ObjectResponse{}}
			w.Header().Set("Content-Type", MediaType)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}))
		defer server.Close()

		client := NewClient(server.Client(), server.URL, Auth{Username: "user", Password: "pass"})
		_, err := client.BatchDownload(context.Background(), []ObjectSpec{{OID: "x", Size: 1}})
		require.NoError(t, err)
	})
}

func TestBatchHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"message":"access denied"}`)); err != nil {
			return
		}
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, Auth{})
	_, err := client.BatchDownload(context.Background(), []ObjectSpec{{OID: "x", Size: 1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "access denied")
}

func TestEndpointFromRepoURL(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"https://github.com/user/repo.git", "https://github.com/user/repo.git/info/lfs", false},
		{"https://github.com/user/repo", "https://github.com/user/repo.git/info/lfs", false},
		{"https://github.com/user/repo.git/", "https://github.com/user/repo.git/info/lfs", false},
		{"https://example.com/project/", "https://example.com/project.git/info/lfs", false},
		{"http://example.com/repo", "http://example.com/repo.git/info/lfs", false},
		{"ssh://git@github.com/user/repo.git", "", true},
		{"git://github.com/user/repo.git", "", true},
		{"git+ssh://git@host/repo", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := EndpointFromRepoURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported scheme")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
