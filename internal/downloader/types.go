// Package downloader provides protocol adapters for supported torrent clients.
package downloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

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
// torrent snapshot omits selected-file sizes. qBittorrent 5.2 requires a
// per-torrent files request, while Transmission includes this data in
// torrent_get.
type FileManifestClient interface {
	SelectedFileSizes(ctx context.Context, torrent TorrentRef) ([]int64, error)
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
	State              string
	Progress           float64
	Ratio              float64
	UploadedBytes      int64
	DownloadedBytes    int64
	UploadSpeed        int64
	DownloadSpeed      int64
	TrackerURLs        []string
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
