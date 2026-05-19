// Package lfsapi provides types and a client for the Git LFS Batch API.
// See https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
package lfsapi

import "time"

// Operation specifies whether objects should be downloaded or uploaded.
type Operation string

const (
	OperationDownload Operation = "download"
	OperationUpload   Operation = "upload"
)

// ObjectSpec identifies an LFS object by its OID and size.
type ObjectSpec struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// BatchRequest is the JSON body sent to the LFS batch endpoint.
type BatchRequest struct {
	Operation Operation    `json:"operation"`
	Transfers []string     `json:"transfers,omitempty"`
	Ref       *Ref         `json:"ref,omitempty"`
	Objects   []ObjectSpec `json:"objects"`
	HashAlgo  string       `json:"hash_algo,omitempty"`
}

// Ref optionally scopes a batch request to a specific git ref.
type Ref struct {
	Name string `json:"name"`
}

// BatchResponse is the JSON response from the LFS batch endpoint.
type BatchResponse struct {
	Transfer string           `json:"transfer,omitempty"`
	Objects  []ObjectResponse `json:"objects"`
	HashAlgo string           `json:"hash_algo,omitempty"`
	Message  string           `json:"message,omitempty"`
}

// ObjectResponse describes the server's response for a single object
// in a batch request.
type ObjectResponse struct {
	OID           string          `json:"oid"`
	Size          int64           `json:"size"`
	Authenticated bool           `json:"authenticated,omitempty"`
	Actions       *ObjectActions  `json:"actions,omitempty"`
	Error         *ObjectError    `json:"error,omitempty"`
}

// ObjectActions contains download/upload action links for an object.
type ObjectActions struct {
	Download *Link `json:"download,omitempty"`
	Upload   *Link `json:"upload,omitempty"`
	Verify   *Link `json:"verify,omitempty"`
}

// Link describes an HTTP endpoint for a transfer action.
type Link struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}

// ObjectError is returned when the server cannot fulfill a request
// for a specific object.
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
