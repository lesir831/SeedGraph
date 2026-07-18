package domain

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	ErrInvalidPath          = errors.New("invalid content path")
	ErrInvalidStorageID     = errors.New("invalid storage id")
	ErrNoPathMapping        = errors.New("no path mapping matched")
	ErrAmbiguousPathMapping = errors.New("ambiguous path mapping")
)

// PathMapping translates the downloader-visible SourcePrefix into a path on a
// named physical storage. DownloaderID may be empty for a global fallback.
type PathMapping struct {
	ID              string `json:"id,omitempty"`
	DownloaderID    string `json:"downloader_id,omitempty"`
	StorageID       string `json:"storage_id"`
	SourcePrefix    string `json:"source_prefix"`
	TargetPrefix    string `json:"target_prefix"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
}

// CanonicalLocation is safe to compare across downloaders only when both its
// StorageID and Path are equal.
type CanonicalLocation struct {
	StorageID string `json:"storage_id"`
	Path      string `json:"path"`
}

// CanonicalizeContentPath associates an already storage-relative path with a
// storage ID without applying prefix translation.
func CanonicalizeContentPath(storageID, rawPath string) (CanonicalLocation, error) {
	if strings.TrimSpace(storageID) == "" {
		return CanonicalLocation{}, ErrInvalidStorageID
	}
	cleaned, err := cleanAbsolutePath(rawPath)
	if err != nil {
		return CanonicalLocation{}, err
	}
	return CanonicalLocation{StorageID: storageID, Path: cleaned}, nil
}

// ApplyPathMappings uses the longest directory-boundary prefix that applies to
// downloaderID. A downloader-specific mapping wins over a global mapping at
// the same prefix length. Conflicting equally specific mappings are rejected.
func ApplyPathMappings(downloaderID, rawPath string, mappings []PathMapping) (CanonicalLocation, error) {
	cleaned, err := cleanAbsolutePath(rawPath)
	if err != nil {
		return CanonicalLocation{}, err
	}

	type match struct {
		mapping     PathMapping
		source      string
		target      string
		specificity int
		location    CanonicalLocation
	}
	var best *match

	for _, candidate := range mappings {
		if candidate.DownloaderID != "" && candidate.DownloaderID != downloaderID {
			continue
		}
		if strings.TrimSpace(candidate.StorageID) == "" {
			return CanonicalLocation{}, fmt.Errorf("mapping %q: %w", candidate.ID, ErrInvalidStorageID)
		}

		source, sourceErr := cleanAbsolutePath(candidate.SourcePrefix)
		if sourceErr != nil {
			return CanonicalLocation{}, fmt.Errorf("mapping %q source prefix: %w", candidate.ID, sourceErr)
		}
		target, targetErr := cleanAbsolutePath(candidate.TargetPrefix)
		if targetErr != nil {
			return CanonicalLocation{}, fmt.Errorf("mapping %q target prefix: %w", candidate.ID, targetErr)
		}
		if !directoryPrefix(cleaned, source, candidate.CaseInsensitive) {
			continue
		}

		mapped := mapRemainder(cleaned, source, target)
		current := &match{
			mapping: candidate,
			source:  source,
			target:  target,
			location: CanonicalLocation{
				StorageID: candidate.StorageID,
				Path:      mapped,
			},
		}
		if candidate.DownloaderID != "" {
			current.specificity = 1
		}

		if best == nil || len(current.source) > len(best.source) ||
			(len(current.source) == len(best.source) && current.specificity > best.specificity) {
			best = current
			continue
		}
		if len(current.source) != len(best.source) || current.specificity != best.specificity {
			continue
		}
		if current.location != best.location {
			return CanonicalLocation{}, fmt.Errorf(
				"%w: mappings %q and %q both match %q",
				ErrAmbiguousPathMapping,
				best.mapping.ID,
				current.mapping.ID,
				cleaned,
			)
		}
	}

	if best == nil {
		return CanonicalLocation{}, fmt.Errorf("%w: %q", ErrNoPathMapping, cleaned)
	}
	return best.location, nil
}

func cleanAbsolutePath(value string) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", ErrInvalidPath
	}
	value = strings.ReplaceAll(value, `\`, "/")
	cleaned := path.Clean(value)
	if cleaned == "." || (!strings.HasPrefix(cleaned, "/") && !isWindowsDriveAbsolute(cleaned)) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrInvalidPath, value)
	}
	return cleaned, nil
}

func isWindowsDriveAbsolute(value string) bool {
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && value[2] == '/'
}

func directoryPrefix(value, prefix string, caseInsensitive bool) bool {
	comparisonValue, comparisonPrefix := value, prefix
	if caseInsensitive {
		comparisonValue = strings.ToLower(comparisonValue)
		comparisonPrefix = strings.ToLower(comparisonPrefix)
	}
	if comparisonValue == comparisonPrefix {
		return true
	}
	if comparisonPrefix == "/" || (len(comparisonPrefix) == 3 && isWindowsDriveAbsolute(comparisonPrefix)) {
		return strings.HasPrefix(comparisonValue, comparisonPrefix)
	}
	return strings.HasPrefix(comparisonValue, comparisonPrefix+"/")
}

func mapRemainder(value, source, target string) string {
	remainder := strings.TrimPrefix(value[len(source):], "/")
	if remainder == "" {
		return target
	}
	return path.Clean(strings.TrimSuffix(target, "/") + "/" + remainder)
}
