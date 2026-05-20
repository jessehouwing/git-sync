// Package lfs provides Git LFS streaming support for git-sync.
// It handles detection, download, and upload of LFS objects between
// source and target remotes without materializing content to disk.
package lfs

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Pointer represents a parsed Git LFS pointer file.
type Pointer struct {
	OID  string // SHA-256 hash of the LFS object (lowercase hex)
	Size int64  // Size in bytes of the LFS object
}

const (
	// pointerVersionLine is the first line of every LFS pointer file.
	pointerVersionLine = "version https://git-lfs.github.com/spec/v1"
	// oidPrefix is the prefix for the OID value.
	oidPrefix = "sha256:"
	// MaxPointerSize is the maximum size of a valid LFS pointer file.
	// Pointer files are small text files; anything larger is not a pointer.
	MaxPointerSize = 200
)

// ParsePointer attempts to parse a Git LFS pointer from a reader.
// It parses all lines as key/value pairs, requiring "version", "oid", and
// "size" to be present. Unknown keys (e.g., "ext-*") are ignored.
// Returns the pointer if valid, or an error if the content is not a valid pointer.
func ParsePointer(r io.Reader) (Pointer, error) {
	scanner := bufio.NewScanner(r)

	// First line must be the version header.
	if !scanner.Scan() {
		return Pointer{}, fmt.Errorf("lfs pointer: missing version line")
	}
	if strings.TrimSpace(scanner.Text()) != pointerVersionLine {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid version line: %q", scanner.Text())
	}

	// Parse remaining lines as key/value pairs.
	var oid string
	var size int64
	var hasOID, hasSize bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue // ignore malformed lines
		}
		switch key {
		case "oid":
			if !strings.HasPrefix(value, oidPrefix) {
				return Pointer{}, fmt.Errorf("lfs pointer: invalid oid value: %q", value)
			}
			oid = strings.TrimPrefix(value, oidPrefix)
			if len(oid) != 64 {
				return Pointer{}, fmt.Errorf("lfs pointer: invalid oid hash length: %d", len(oid))
			}
			// Validate hex characters.
			if _, err := hex.DecodeString(oid); err != nil {
				return Pointer{}, fmt.Errorf("lfs pointer: oid contains invalid hex characters: %q", oid)
			}
			// Normalize to lowercase.
			oid = strings.ToLower(oid)
			hasOID = true
		case "size":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Pointer{}, fmt.Errorf("lfs pointer: invalid size: %w", err)
			}
			if parsed < 0 {
				return Pointer{}, fmt.Errorf("lfs pointer: negative size: %d", parsed)
			}
			size = parsed
			hasSize = true
		default:
			// Unknown keys (e.g., ext-*) are ignored per LFS spec.
		}
	}

	if !hasOID {
		return Pointer{}, fmt.Errorf("lfs pointer: missing oid")
	}
	if !hasSize {
		return Pointer{}, fmt.Errorf("lfs pointer: missing size")
	}

	return Pointer{OID: oid, Size: size}, nil
}

// ParsePointerBytes attempts to parse a Git LFS pointer from a byte slice.
// Returns the pointer and true if valid, or zero value and false if not.
func ParsePointerBytes(data []byte) (Pointer, bool) {
	if len(data) > MaxPointerSize {
		return Pointer{}, false
	}
	p, err := ParsePointer(strings.NewReader(string(data)))
	if err != nil {
		return Pointer{}, false
	}
	return p, true
}
