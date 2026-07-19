// Package downloader provides protocol adapters for supported torrent clients.
package downloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// time.Time's JSON encoding only supports years 0 through 9999. Treat larger
// downloader timestamps as unknown so one corrupt remote value cannot break
// every aggregate API response.
const maxJSONUnixTimestamp int64 = 253402300799

func parseTorrentAddedAt(unixSeconds int64) (time.Time, error) {
	if unixSeconds < 0 {
		return time.Time{}, errors.New("invalid added time")
	}
	if unixSeconds == 0 || unixSeconds > maxJSONUnixTimestamp {
		return time.Time{}, nil
	}
	return time.Unix(unixSeconds, 0).UTC(), nil
}

// Kind identifies a supported downloader protocol.
type Kind string

const (
	KindQBittorrent  Kind = "qbittorrent"
	KindTransmission Kind = "transmission"
)

var (
	ErrAuthentication    = errors.New("downloader authentication failed")
	ErrInvalidConfig     = errors.New("invalid downloader configuration")
	ErrInvalidStableHash = errors.New("invalid stable torrent hash")
	ErrUnsupportedKind   = errors.New("unsupported downloader kind")
)

// Config contains the connection details used by New. HTTPClient is optional;
// when omitted, an isolated client with a finite timeout is created.
type Config struct {
	Kind       Kind
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
}

// Client is the common protocol boundary used by synchronization and deletion
// services. FullSnapshot does not advance a protocol cursor. Delta returns the
// next cursor when the remote protocol has one.
type Client interface {
	Version(ctx context.Context) (string, error)
	FullSnapshot(ctx context.Context) (Snapshot, error)
	Delta(ctx context.Context, cursor string) (Snapshot, error)
	Delete(ctx context.Context, torrent TorrentRef, deleteFiles bool) error
}

// FileManifestClient is an optional capability for clients whose ordinary
// torrent snapshot omits its file manifest. qBittorrent requires a per-torrent
// files request, while Transmission includes this data in torrent_get.
type FileManifestClient interface {
	FileManifest(ctx context.Context, torrent Torrent) ([]TorrentFile, error)
}

// TorrentFile is one downloader-visible file owned by a torrent. Path is an
// absolute path in that downloader's filesystem namespace.
type TorrentFile struct {
	Path     string
	Size     int64
	Selected bool
}

// TorrentRef identifies one torrent for a remote mutation. StableHash is the
// authoritative identity. RemoteID is informational and must never replace a
// missing hash because Transmission numeric IDs can be reused after restart.
type TorrentRef struct {
	StableHash string
	RemoteID   string
}

// RemovedTorrent describes a remote deletion observed during a delta. qBittorrent
// supplies StableHash; Transmission's recently_active response only supplies a
// numeric RemoteID, which the caller must resolve against its stored snapshot.
type RemovedTorrent struct {
	StableHash string
	RemoteID   string
}

// Torrent is the protocol-neutral representation returned by both adapters.
// TrackerURLs may contain passkeys and must be treated as secret-bearing data:
// adapters return them for classification, but never include them in errors.
type Torrent struct {
	StableHash         string
	RemoteID           string
	Name               string
	SavePath           string
	ContentPath        string
	WantedBytes        int64
	SelectedFilesKnown bool
	SelectedFileCount  int
	SelectedFileSizes  []int64
	FileManifestKnown  bool
	Files              []TorrentFile
	State              string
	Progress           float64
	Ratio              float64
	UploadedBytes      int64
	DownloadedBytes    int64
	UploadSpeed        int64
	DownloadSpeed      int64
	AddedAt            time.Time
	TrackerURLs        []string
}

func torrentFilePath(directory, name string) (string, error) {
	if strings.TrimSpace(directory) == "" || strings.TrimSpace(name) == "" ||
		strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("invalid torrent file path")
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		strings.HasPrefix(cleaned, "/") || isWindowsAbsolutePath(cleaned) {
		return "", errors.New("invalid torrent file path")
	}
	return joinRemotePath(directory, cleaned), nil
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && value[2] == '/'
}

// Ref returns the mutation reference for t.
func (t Torrent) Ref() TorrentRef {
	return TorrentRef{StableHash: t.StableHash, RemoteID: t.RemoteID}
}

// Snapshot contains either a complete view or a protocol delta.
type Snapshot struct {
	Full     bool
	Cursor   string
	Torrents []Torrent
	Removed  []RemovedTorrent
}

// New constructs a downloader adapter without making a network request.
func New(config Config) (Client, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(string(config.Kind)))) {
	case KindQBittorrent:
		return newQBittorrent(config)
	case KindTransmission:
		return newTransmission(config)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, config.Kind)
	}
}

// NewClient is an explicit alias for New for call sites that prefer a factory
// name over a package constructor.
func NewClient(config Config) (Client, error) {
	return New(config)
}

func normalizeStableHash(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if (len(value) != 40 && len(value) != 64) || value == "all" || strings.ContainsAny(value, "|\r\n\t ") {
		return "", ErrInvalidStableHash
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", ErrInvalidStableHash
	}
	return value, nil
}
