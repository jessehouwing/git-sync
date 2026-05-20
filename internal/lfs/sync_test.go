package lfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"entire.io/entire/git-sync/internal/lfs/lfsapi"
)

func TestSync_NoPointers(t *testing.T) {
	stats, err := Sync(context.Background(), nil, SyncOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Objects)
	assert.Equal(t, 0, stats.Transferred)
}

func TestSync_AllSkipped(t *testing.T) {
	// Target says it already has the object (no upload action).
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req lfsapi.BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := lfsapi.BatchResponse{
			Objects: make([]lfsapi.ObjectResponse, len(req.Objects)),
		}
		for i, obj := range req.Objects {
			resp.Objects[i] = lfsapi.ObjectResponse{
				OID:  obj.OID,
				Size: obj.Size,
				// No Actions means target already has it.
			}
		}
		w.Header().Set("Content-Type", lfsapi.MediaType)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer targetServer.Close()

	pointers := []Pointer{
		{OID: "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393", Size: 100},
	}

	stats, err := Sync(context.Background(), pointers, SyncOptions{
		SourceEndpoint: "http://unused",
		TargetEndpoint: targetServer.URL,
		HTTPClient:     targetServer.Client(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Objects)
	assert.Equal(t, 0, stats.Transferred)
	assert.Equal(t, 1, stats.Skipped)
}

func TestSync_TransferOneObject(t *testing.T) {
	// Create test content.
	content := []byte("test lfs object content for streaming")
	hash := sha256.Sum256(content)
	oid := hex.EncodeToString(hash[:])
	size := int64(len(content))

	var uploadCount atomic.Int64

	// Source LFS server: returns download links.
	var sourceURL string
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/objects/batch" {
			var req lfsapi.BatchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := lfsapi.BatchResponse{
				Objects: []lfsapi.ObjectResponse{
					{
						OID:  oid,
						Size: size,
						Actions: &lfsapi.ObjectActions{
							Download: &lfsapi.Link{Href: sourceURL + "/download/" + oid},
						},
					},
				},
			}
			w.Header().Set("Content-Type", lfsapi.MediaType)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		// Serve the actual download.
		if _, err := w.Write(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer sourceServer.Close()
	sourceURL = sourceServer.URL

	// Target LFS server: accepts uploads.
	var targetURL string
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/objects/batch" {
			var req lfsapi.BatchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := lfsapi.BatchResponse{
				Objects: []lfsapi.ObjectResponse{
					{
						OID:  oid,
						Size: size,
						Actions: &lfsapi.ObjectActions{
							Upload: &lfsapi.Link{Href: targetURL + "/upload/" + oid},
						},
					},
				},
			}
			w.Header().Set("Content-Type", lfsapi.MediaType)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		// Accept uploads.
		uploadCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()
	targetURL = targetServer.URL

	pointers := []Pointer{{OID: oid, Size: size}}

	stats, err := Sync(context.Background(), pointers, SyncOptions{
		SourceEndpoint: sourceServer.URL,
		TargetEndpoint: targetServer.URL,
		HTTPClient:     http.DefaultClient,
		Concurrency:    2,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Objects)
	assert.Equal(t, 1, stats.Transferred)
	assert.Equal(t, size, stats.BytesTransferred)
	assert.Equal(t, int64(1), uploadCount.Load())
}

func TestSync_DeduplicatesPointers(t *testing.T) {
	var batchCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batchCalls.Add(1)
		var req lfsapi.BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Return no upload actions (all skipped).
		resp := lfsapi.BatchResponse{
			Objects: make([]lfsapi.ObjectResponse, len(req.Objects)),
		}
		for i, obj := range req.Objects {
			resp.Objects[i] = lfsapi.ObjectResponse{OID: obj.OID, Size: obj.Size}
		}
		w.Header().Set("Content-Type", lfsapi.MediaType)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	// Duplicate pointers should be deduplicated.
	pointers := []Pointer{
		{OID: "aaaa" + "0000000000000000000000000000000000000000000000000000000000aa", Size: 100},
		{OID: "aaaa" + "0000000000000000000000000000000000000000000000000000000000aa", Size: 100},
		{OID: "bbbb" + "0000000000000000000000000000000000000000000000000000000000bb", Size: 200},
	}

	stats, err := Sync(context.Background(), pointers, SyncOptions{
		SourceEndpoint: "http://unused",
		TargetEndpoint: server.URL,
		HTTPClient:     server.Client(),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Objects) // Deduplicated from 3 to 2.
}
