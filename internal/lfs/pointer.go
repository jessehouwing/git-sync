// Package lfs provides Git LFS streaming support for git-sync.
// It handles detection, download, and upload of LFS objects between
// source and target remotes without materializing content to disk.
package lfs

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Pointer represents a parsed Git LFS pointer file.
type Pointer struct {
	OID  string // SHA-256 hash of the LFS object
	Size int64  // Size in bytes of the LFS object
}

const (
	// pointerVersionLine is the first line of every LFS pointer file.
	pointerVersionLine = "version https://git-lfs.github.com/spec/v1"
	// oidPrefix is the prefix for the OID line.
	oidPrefix = "oid sha256:"
	// sizePrefix is the prefix for the size line.
	sizePrefix = "size "
	// MaxPointerSize is the maximum size of a valid LFS pointer file.
	// Pointer files are small text files; anything larger is not a pointer.
	MaxPointerSize = 200
)

// ParsePointer attempts to parse a Git LFS pointer from a reader.
// Returns the pointer if valid, or an error if the content is not a valid pointer.
func ParsePointer(r io.Reader) (Pointer, error) {
	scanner := bufio.NewScanner(r)

	// Line 1: version
	if !scanner.Scan() {
		return Pointer{}, fmt.Errorf("lfs pointer: missing version line")
	}
	if strings.TrimSpace(scanner.Text()) != pointerVersionLine {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid version line: %q", scanner.Text())
	}

	// Line 2: oid sha256:<hash>
	if !scanner.Scan() {
		return Pointer{}, fmt.Errorf("lfs pointer: missing oid line")
	}
	oidLine := strings.TrimSpace(scanner.Text())
	if !strings.HasPrefix(oidLine, oidPrefix) {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid oid line: %q", oidLine)
	}
	oid := strings.TrimPrefix(oidLine, oidPrefix)
	if len(oid) != 64 {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid oid hash length: %d", len(oid))
	}

	// Line 3: size <bytes>
	if !scanner.Scan() {
		return Pointer{}, fmt.Errorf("lfs pointer: missing size line")
	}
	sizeLine := strings.TrimSpace(scanner.Text())
	if !strings.HasPrefix(sizeLine, sizePrefix) {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid size line: %q", sizeLine)
	}
	size, err := strconv.ParseInt(strings.TrimPrefix(sizeLine, sizePrefix), 10, 64)
	if err != nil {
		return Pointer{}, fmt.Errorf("lfs pointer: invalid size: %w", err)
	}
	if size < 0 {
		return Pointer{}, fmt.Errorf("lfs pointer: negative size: %d", size)
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
