package lfs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"entire.io/entire/git-sync/internal/lfs/lfsapi"
)

// SyncOptions configures the LFS sync process.
type SyncOptions struct {
	// SourceEndpoint is the LFS API base URL for the source repository.
	SourceEndpoint string
	// TargetEndpoint is the LFS API base URL for the target repository.
	TargetEndpoint string
	// SourceAuth is the authentication for the source LFS server.
	SourceAuth lfsapi.Auth
	// TargetAuth is the authentication for the target LFS server.
	TargetAuth lfsapi.Auth
	// SourceHTTPClient is the HTTP client used for source LFS operations.
	// Falls back to http.DefaultClient if nil.
	SourceHTTPClient *http.Client
	// TargetHTTPClient is the HTTP client used for target LFS operations.
	// Falls back to http.DefaultClient if nil.
	TargetHTTPClient *http.Client
	// Concurrency is the number of parallel object transfers (default: 4).
	Concurrency int
}

// SyncStats holds the outcome of an LFS sync operation.
type SyncStats struct {
	// Objects is the total number of LFS objects found.
	Objects int `json:"objects"`
	// Transferred is the number of objects transferred to the target.
	Transferred int `json:"transferred"`
	// Skipped is the number of objects the target already had.
	Skipped int `json:"skipped"`
	// Errored is the number of objects that failed to transfer.
	Errored int `json:"errored"`
	// BytesTransferred is the total bytes streamed to the target.
	BytesTransferred int64 `json:"bytesTransferred"`
}

// Sync transfers LFS objects from source to target. It uses the batch API
// to determine which objects the target needs, then streams each object
// from source to target without materializing the full content in memory.
func Sync(ctx context.Context, pointers []Pointer, opts SyncOptions) (SyncStats, error) {
	if len(pointers) == 0 {
		return SyncStats{}, nil
	}

	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	sourceHTTPClient := opts.SourceHTTPClient
	if sourceHTTPClient == nil {
		sourceHTTPClient = http.DefaultClient
	}

	targetHTTPClient := opts.TargetHTTPClient
	if targetHTTPClient == nil {
		targetHTTPClient = http.DefaultClient
	}

	sourceClient := lfsapi.NewClient(sourceHTTPClient, opts.SourceEndpoint, opts.SourceAuth)
	targetClient := lfsapi.NewClient(targetHTTPClient, opts.TargetEndpoint, opts.TargetAuth)

	pointers = DeduplicatePointers(pointers)
	stats := SyncStats{Objects: len(pointers)}

	// Ask target which objects it needs (upload batch tells us which need actions).
	specs := pointersToSpecs(pointers)
	needsTransfer, err := findObjectsToTransfer(ctx, targetClient, specs)
	if err != nil {
		return stats, fmt.Errorf("lfs sync: check target: %w", err)
	}

	stats.Skipped = len(pointers) - len(needsTransfer)
	if len(needsTransfer) == 0 {
		return stats, nil
	}

	// Get download URLs from source.
	downloadSpecs := make([]lfsapi.ObjectSpec, 0, len(needsTransfer))
	for _, spec := range needsTransfer {
		downloadSpecs = append(downloadSpecs, lfsapi.ObjectSpec{OID: spec.OID, Size: spec.Size})
	}

	downloadResp, err := batchDownloadAll(ctx, sourceClient, downloadSpecs)
	if err != nil {
		return stats, fmt.Errorf("lfs sync: source batch download: %w", err)
	}

	// Build a map of OID → download link.
	downloadLinks := make(map[string]*lfsapi.Link, len(downloadResp))
	for i := range downloadResp {
		obj := &downloadResp[i]
		if obj.Actions != nil && obj.Actions.Download != nil {
			downloadLinks[obj.OID] = obj.Actions.Download
		}
	}

	// Get upload URLs from target.
	uploadResp, err := batchUploadAll(ctx, targetClient, downloadSpecs)
	if err != nil {
		return stats, fmt.Errorf("lfs sync: target batch upload: %w", err)
	}

	// Stream objects from source to target concurrently.
	type transferJob struct {
		oid      string
		size     int64
		download *lfsapi.Link
		upload   *lfsapi.Link
		verify   *lfsapi.Link
	}

	var jobs []transferJob
	var preflightErrors int
	for i := range uploadResp {
		obj := &uploadResp[i]
		// Handle per-object errors from the batch response.
		if obj.Error != nil {
			slog.Warn("lfs sync: target batch error for object", "oid", obj.OID, "code", obj.Error.Code, "message", obj.Error.Message)
			preflightErrors++
			continue
		}
		if obj.Actions == nil || obj.Actions.Upload == nil {
			// Target confirmed it already has this object (no upload action
			// in second batch call). This is an edge case where the target
			// accepted the object between our first check and the upload
			// batch. Not counted as an error.
			continue
		}
		dl, ok := downloadLinks[obj.OID]
		if !ok {
			slog.Warn("lfs sync: no download link for object", "oid", obj.OID)
			preflightErrors++
			continue
		}
		job := transferJob{
			oid:      obj.OID,
			size:     obj.Size,
			download: dl,
			upload:   obj.Actions.Upload,
		}
		if obj.Actions.Verify != nil {
			job.verify = obj.Actions.Verify
		}
		jobs = append(jobs, job)
	}

	// Execute transfers with bounded concurrency.
	var (
		wg          sync.WaitGroup
		transferred atomic.Int64
		bytesTotal  atomic.Int64
		errored     atomic.Int64
		sem         = make(chan struct{}, concurrency)
	)

	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(j transferJob) {
			defer wg.Done()
			defer func() { <-sem }()

			err := streamObject(ctx, sourceHTTPClient, targetHTTPClient, j.oid, j.size, j.download, j.upload, j.verify)
			if err != nil {
				slog.Warn("lfs sync: transfer failed", "oid", j.oid, "err", err)
				errored.Add(1)
				return
			}
			transferred.Add(1)
			bytesTotal.Add(j.size)
		}(job)
	}
	wg.Wait()

	stats.Transferred = int(transferred.Load())
	stats.BytesTransferred = bytesTotal.Load()
	stats.Errored = preflightErrors + int(errored.Load())
	return stats, nil
}

// streamObject downloads from source and uploads to target using io.Pipe
// for streaming without buffering the entire object in memory.
func streamObject(ctx context.Context, sourceHTTPClient, targetHTTPClient *http.Client, oid string, size int64, download, upload, verify *lfsapi.Link) error {
	pr, pw := io.Pipe()

	var downloadErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		downloadErr = Download(ctx, sourceHTTPClient, download.Href, download.Header, pw, oid, size)
		pw.CloseWithError(downloadErr)
	}()

	uploadErr := Upload(ctx, targetHTTPClient, upload.Href, upload.Header, pr, size)
	// If upload fails, close the pipe to unblock download goroutine.
	if uploadErr != nil {
		pr.CloseWithError(uploadErr)
	}
	wg.Wait()

	if downloadErr != nil {
		return fmt.Errorf("download: %w", downloadErr)
	}
	if uploadErr != nil {
		return fmt.Errorf("upload: %w", uploadErr)
	}

	// Optional verify step.
	if verify != nil {
		if err := Verify(ctx, targetHTTPClient, verify.Href, verify.Header, oid, size); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
	}
	return nil
}

// findObjectsToTransfer queries the target batch API with upload operation
// to discover which objects the target does NOT already have.
func findObjectsToTransfer(ctx context.Context, client *lfsapi.Client, specs []lfsapi.ObjectSpec) ([]lfsapi.ObjectSpec, error) {
	resp, err := batchUploadAll(ctx, client, specs)
	if err != nil {
		return nil, err
	}

	var needed []lfsapi.ObjectSpec
	for i := range resp {
		obj := &resp[i]
		// If the server returns an upload action, the target needs this object.
		if obj.Actions != nil && obj.Actions.Upload != nil {
			needed = append(needed, lfsapi.ObjectSpec{OID: obj.OID, Size: obj.Size})
		}
	}
	return needed, nil
}

// batchDownloadAll handles pagination of batch requests.
func batchDownloadAll(ctx context.Context, client *lfsapi.Client, specs []lfsapi.ObjectSpec) ([]lfsapi.ObjectResponse, error) {
	return batchAll(ctx, client, specs, lfsapi.OperationDownload)
}

// batchUploadAll handles pagination of batch requests.
func batchUploadAll(ctx context.Context, client *lfsapi.Client, specs []lfsapi.ObjectSpec) ([]lfsapi.ObjectResponse, error) {
	return batchAll(ctx, client, specs, lfsapi.OperationUpload)
}

func batchAll(ctx context.Context, client *lfsapi.Client, specs []lfsapi.ObjectSpec, op lfsapi.Operation) ([]lfsapi.ObjectResponse, error) {
	batchSize := lfsapi.MaxBatchSize()
	var all []lfsapi.ObjectResponse

	for i := 0; i < len(specs); i += batchSize {
		end := i + batchSize
		if end > len(specs) {
			end = len(specs)
		}
		chunk := specs[i:end]

		resp, err := client.Batch(ctx, lfsapi.BatchRequest{
			Operation: op,
			Transfers: []string{"basic"},
			Objects:   chunk,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Objects...)
	}
	return all, nil
}

func pointersToSpecs(pointers []Pointer) []lfsapi.ObjectSpec {
	specs := make([]lfsapi.ObjectSpec, len(pointers))
	for i, p := range pointers {
		specs[i] = lfsapi.ObjectSpec{OID: p.OID, Size: p.Size}
	}
	return specs
}
