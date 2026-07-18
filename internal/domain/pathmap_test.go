package domain

import (
	"errors"
	"reflect"
	"testing"
)

func TestApplyPathMappingsLongestDirectoryBoundary(t *testing.T) {
	t.Parallel()
	mappings := []PathMapping{
		{ID: "root", DownloaderID: "qb", StorageID: "media", SourcePrefix: "/downloads", TargetPrefix: "/"},
		{ID: "movies", DownloaderID: "qb", StorageID: "movies", SourcePrefix: "/downloads/movies", TargetPrefix: "/library"},
		{ID: "not-a-boundary", DownloaderID: "qb", StorageID: "wrong", SourcePrefix: "/download", TargetPrefix: "/wrong"},
	}

	got, err := ApplyPathMappings("qb", "/downloads/movies/Film/file.mkv", mappings)
	if err != nil {
		t.Fatalf("ApplyPathMappings() error = %v", err)
	}
	want := CanonicalLocation{StorageID: "movies", Path: "/library/Film/file.mkv"}
	if got != want {
		t.Fatalf("ApplyPathMappings() = %#v, want %#v", got, want)
	}

	_, err = ApplyPathMappings("qb", "/downloads2/file.mkv", mappings)
	if !errors.Is(err, ErrNoPathMapping) {
		t.Fatalf("boundary mismatch error = %v, want ErrNoPathMapping", err)
	}
}

func TestApplyPathMappingsDownloaderSpecificWinsTie(t *testing.T) {
	t.Parallel()
	mappings := []PathMapping{
		{ID: "global", StorageID: "fallback", SourcePrefix: "/data", TargetPrefix: "/"},
		{ID: "tr", DownloaderID: "tr", StorageID: "shared", SourcePrefix: "/data", TargetPrefix: "/torrents"},
	}

	got, err := ApplyPathMappings("tr", "/data/example", mappings)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CanonicalLocation{StorageID: "shared", Path: "/torrents/example"}); got != want {
		t.Fatalf("specific mapping = %#v, want %#v", got, want)
	}

	got, err = ApplyPathMappings("qb", "/data/example", mappings)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CanonicalLocation{StorageID: "fallback", Path: "/example"}); got != want {
		t.Fatalf("global mapping = %#v, want %#v", got, want)
	}
}

func TestApplyPathMappingsRejectsAmbiguousEqualMappings(t *testing.T) {
	t.Parallel()
	_, err := ApplyPathMappings("qb", "/data/file", []PathMapping{
		{ID: "a", DownloaderID: "qb", StorageID: "one", SourcePrefix: "/data", TargetPrefix: "/"},
		{ID: "b", DownloaderID: "qb", StorageID: "two", SourcePrefix: "/data", TargetPrefix: "/"},
	})
	if !errors.Is(err, ErrAmbiguousPathMapping) {
		t.Fatalf("error = %v, want ErrAmbiguousPathMapping", err)
	}
}

func TestApplyPathMappingsNormalizesWindowsPaths(t *testing.T) {
	t.Parallel()
	got, err := ApplyPathMappings("qb", `c:\Downloads\Movies\Film.mkv`, []PathMapping{
		{
			ID:              "windows",
			DownloaderID:    "qb",
			StorageID:       "media",
			SourcePrefix:    `C:\DOWNLOADS`,
			TargetPrefix:    "/library",
			CaseInsensitive: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := (CanonicalLocation{StorageID: "media", Path: "/library/Movies/Film.mkv"}); got != want {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}

func TestCanonicalizeContentPathValidation(t *testing.T) {
	t.Parallel()
	got, err := CanonicalizeContentPath("media", "/a/../b//file")
	if err != nil {
		t.Fatal(err)
	}
	if want := (CanonicalLocation{StorageID: "media", Path: "/b/file"}); got != want {
		t.Fatalf("result = %#v, want %#v", got, want)
	}

	if _, err := CanonicalizeContentPath("", "/data"); !errors.Is(err, ErrInvalidStorageID) {
		t.Fatalf("empty storage error = %v", err)
	}
	if _, err := CanonicalizeContentPath("media", "relative/path"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("relative path error = %v", err)
	}
}

func TestSelectedFileSizeFingerprintIsOrderIndependentAndNonMutating(t *testing.T) {
	t.Parallel()
	input := []int64{30, 10, 20, 10}
	original := append([]int64(nil), input...)
	first, err := SelectedFileSizeFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SelectedFileSizeFingerprint([]int64{10, 20, 10, 30})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("fingerprints differ: %s != %s", first, second)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("input mutated: got %v, want %v", input, original)
	}

	different, err := SelectedFileSizeFingerprint([]int64{10, 20, 30})
	if err != nil {
		t.Fatal(err)
	}
	if first == different {
		t.Fatal("different multisets produced the same fingerprint")
	}
}

func TestSelectedFileSizeFingerprintRejectsNegativeSize(t *testing.T) {
	t.Parallel()
	if _, err := SelectedFileSizeFingerprint([]int64{1, -1}); !errors.Is(err, ErrNegativeFileSize) {
		t.Fatalf("error = %v, want ErrNegativeFileSize", err)
	}
}

func TestDeterministicIDUsesPartBoundaries(t *testing.T) {
	t.Parallel()
	first := DeterministicID("cg", "ab", "c")
	second := DeterministicID("cg", "a", "bc")
	if first == second {
		t.Fatal("length-delimited IDs collided across part boundaries")
	}
	if first != DeterministicID("cg", "ab", "c") {
		t.Fatal("DeterministicID is not stable")
	}
	if first == DeterministicID("dg", "ab", "c") {
		t.Fatal("namespace was not included in the digest")
	}
}
