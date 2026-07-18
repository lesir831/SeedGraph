package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// DeterministicID returns a stable, collision-resistant identifier. Inputs are
// length-prefixed so different part boundaries cannot produce the same digest.
// The namespace is also included in the digest and used as the readable prefix.
func DeterministicID(namespace string, parts ...string) string {
	namespace = normalizeNamespace(namespace)
	h := sha256.New()
	writeDigestPart(h, namespace)
	for _, part := range parts {
		writeDigestPart(h, part)
	}
	return namespace + "_" + hex.EncodeToString(h.Sum(nil))
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestPart(w digestWriter, part string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(part))
}

func normalizeNamespace(namespace string) string {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	var b strings.Builder
	for _, r := range namespace {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "id"
	}
	return b.String()
}
