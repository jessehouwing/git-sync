package lfs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePointer(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Pointer
		wantErr bool
	}{
		{
			name: "valid pointer",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
				"size 12345\n",
			want: Pointer{
				OID:  "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393",
				Size: 12345,
			},
		},
		{
			name: "valid pointer without trailing newline",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n" +
				"size 0",
			want: Pointer{
				OID:  "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				Size: 0,
			},
		},
		{
			name: "pointer with extension lines",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"ext-0-foo sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
				"size 12345\n",
			want: Pointer{
				OID:  "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393",
				Size: 12345,
			},
		},
		{
			name: "pointer with oid and size in reversed order",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"size 999\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n",
			want: Pointer{
				OID:  "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393",
				Size: 999,
			},
		},
		{
			name: "uppercase oid is normalized to lowercase",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:4D7A214614AB2935C943F9E0FF69D22EADBB8F32B1258DAAA5E2CA24D17E2393\n" +
				"size 100\n",
			want: Pointer{
				OID:  "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393",
				Size: 100,
			},
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "wrong version",
			input:   "version https://other.com/spec/v1\noid sha256:abc\nsize 123\n",
			wantErr: true,
		},
		{
			name: "missing oid",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"size 123\n",
			wantErr: true,
		},
		{
			name: "short hash",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:abc123\n" +
				"size 123\n",
			wantErr: true,
		},
		{
			name: "non-hex characters in oid",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz\n" +
				"size 123\n",
			wantErr: true,
		},
		{
			name: "missing size",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n",
			wantErr: true,
		},
		{
			name: "negative size",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
				"size -1\n",
			wantErr: true,
		},
		{
			name: "invalid size",
			input: "version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
				"size abc\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePointer(strings.NewReader(tt.input))
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParsePointerBytes(t *testing.T) {
	valid := []byte("version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
		"size 12345\n")

	t.Run("valid", func(t *testing.T) {
		p, ok := ParsePointerBytes(valid)
		require.True(t, ok)
		assert.Equal(t, "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393", p.OID)
		assert.Equal(t, int64(12345), p.Size)
	})

	t.Run("too large", func(t *testing.T) {
		large := make([]byte, MaxPointerSize+1)
		_, ok := ParsePointerBytes(large)
		assert.False(t, ok)
	})

	t.Run("not a pointer", func(t *testing.T) {
		_, ok := ParsePointerBytes([]byte("hello world"))
		assert.False(t, ok)
	})
}

func TestDeduplicatePointers(t *testing.T) {
	pointers := []Pointer{
		{OID: "aaa", Size: 100},
		{OID: "bbb", Size: 200},
		{OID: "aaa", Size: 100},
		{OID: "ccc", Size: 300},
		{OID: "bbb", Size: 200},
	}

	result := DeduplicatePointers(pointers)
	assert.Len(t, result, 3)
	assert.Equal(t, "aaa", result[0].OID)
	assert.Equal(t, "bbb", result[1].OID)
	assert.Equal(t, "ccc", result[2].OID)
}

func TestScanBlobs(t *testing.T) {
	blobs := [][]byte{
		[]byte("version https://git-lfs.github.com/spec/v1\n" +
			"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
			"size 12345\n"),
		[]byte("not a pointer file"),
		[]byte("version https://git-lfs.github.com/spec/v1\n" +
			"oid sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n" +
			"size 67890\n"),
		[]byte(""), // empty blob
	}

	pointers := ScanBlobs(blobs)
	require.Len(t, pointers, 2)
	assert.Equal(t, "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393", pointers[0].OID)
	assert.Equal(t, int64(12345), pointers[0].Size)
	assert.Equal(t, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", pointers[1].OID)
	assert.Equal(t, int64(67890), pointers[1].Size)
}
