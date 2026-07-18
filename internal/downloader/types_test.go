package downloader

import (
	"errors"
	"strings"
	"testing"
)

func TestFactoryRejectsUnsupportedKindAndUnsafeURL(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Kind: "other", BaseURL: "http://example.test"}); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("unsupported kind error = %v", err)
	}
	if _, err := New(Config{
		Kind: KindTransmission, BaseURL: "http://user:password@example.test/transmission/rpc",
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("credential-bearing URL error = %v", err)
	}
}

func TestDeleteRequiresStableHash(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "all", "abc", "abc|def", "abc def", strings.Repeat("z", 40)} {
		if _, err := normalizeStableHash(value); !errors.Is(err, ErrInvalidStableHash) {
			t.Fatalf("normalizeStableHash(%q) error = %v", value, err)
		}
	}
}
