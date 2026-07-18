package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

var ErrNegativeFileSize = errors.New("selected file size cannot be negative")

// SelectedFileSizeFingerprint hashes the sorted selected-file sizes. It is
// independent of file names and original ordering, which allows cross-seeds
// that rename files to be verified without reading their contents.
func SelectedFileSizeFingerprint(sizes []int64) (string, error) {
	ordered := append([]int64(nil), sizes...)
	for _, size := range ordered {
		if size < 0 {
			return "", fmt.Errorf("%w: %d", ErrNegativeFileSize, size)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	h := sha256.New()
	_, _ = h.Write([]byte("seedgraph:selected-file-sizes:v1\x00"))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(len(ordered)))
	_, _ = h.Write(encoded[:])
	for _, size := range ordered {
		binary.BigEndian.PutUint64(encoded[:], uint64(size))
		_, _ = h.Write(encoded[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
