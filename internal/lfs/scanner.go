package lfs

import (
	"fmt"
	"log/slog"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// ScanStore scans all blobs in the given object store and returns any
// that are valid LFS pointer files. This is used after fetching objects
// into a memory store to find LFS objects that need to be transferred.
// Blob read errors are logged and counted; if any occur, an error is
// returned alongside any pointers found so callers can surface a warning.
func ScanStore(store storer.EncodedObjectStorer) ([]Pointer, error) {
	iter, err := store.IterEncodedObjects(plumbing.BlobObject)
	if err != nil {
		return nil, fmt.Errorf("lfs scanner: iterate blobs: %w", err)
	}
	defer iter.Close()

	var pointers []Pointer
	var readErrors int
	err = iter.ForEach(func(obj plumbing.EncodedObject) error {
		if obj.Size() > MaxPointerSize {
			return nil
		}
		reader, err := obj.Reader()
		if err != nil {
			readErrors++
			slog.Warn("lfs scanner: failed to read blob",
				slog.String("hash", obj.Hash().String()),
				slog.Any("err", err))
			return nil
		}
		defer reader.Close()

		p, parseErr := ParsePointer(reader)
		if parseErr != nil {
			// Not a pointer file, skip.
			return nil
		}
		pointers = append(pointers, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("lfs scanner: scan blobs: %w", err)
	}
	if readErrors > 0 {
		return pointers, fmt.Errorf("lfs scanner: %d blob(s) could not be read (scan may be incomplete)", readErrors)
	}
	return pointers, nil
}

// ScanBlobs examines a slice of raw blob contents and returns any that
// are valid LFS pointer files. Useful for scanning blobs from a pack
// stream without materializing to a full object store.
func ScanBlobs(blobs [][]byte) []Pointer {
	var pointers []Pointer
	for _, data := range blobs {
		if p, ok := ParsePointerBytes(data); ok {
			pointers = append(pointers, p)
		}
	}
	return pointers
}

// DeduplicatePointers removes duplicate pointers by OID.
func DeduplicatePointers(pointers []Pointer) []Pointer {
	seen := make(map[string]struct{}, len(pointers))
	result := make([]Pointer, 0, len(pointers))
	for _, p := range pointers {
		if _, ok := seen[p.OID]; ok {
			continue
		}
		seen[p.OID] = struct{}{}
		result = append(result, p)
	}
	return result
}
