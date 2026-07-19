package domain

import "time"

// DownloaderType identifies a supported downloader adapter.
type DownloaderType string

const (
	DownloaderQBittorrent  DownloaderType = "qbittorrent"
	DownloaderTransmission DownloaderType = "transmission"
)

// AssignmentSource records whether a content-group assignment may be changed
// by the automatic grouping engine.
type AssignmentSource string

const (
	AssignmentAuto   AssignmentSource = "auto"
	AssignmentManual AssignmentSource = "manual"
)

// GroupMode describes how a content group is managed.
type GroupMode string

const (
	GroupModeAuto   GroupMode = "auto"
	GroupModeManual GroupMode = "manual"
)

// GroupConfidence describes how strongly the members of a group are known to
// refer to the same data. Tentative groups must never authorize file deletion.
type GroupConfidence string

const (
	ConfidenceTentative GroupConfidence = "tentative"
	ConfidenceVerified  GroupConfidence = "verified"
	ConfidenceManual    GroupConfidence = "manual"
)

// TorrentInstance is one torrent task in one downloader. ExternalKey must be a
// stable torrent hash (qBittorrent hash or Transmission hashString); RemoteID
// is deliberately separate because Transmission numeric IDs may be reused.
type TorrentInstance struct {
	ID                  string           `json:"id"`
	DownloaderID        string           `json:"downloader_id"`
	DownloaderName      string           `json:"downloader_name,omitempty"`
	ExternalKey         string           `json:"external_key"`
	RemoteID            string           `json:"remote_id,omitempty"`
	Name                string           `json:"name"`
	StorageID           string           `json:"storage_id"`
	ContentPath         string           `json:"content_path"`
	CanonicalPath       string           `json:"canonical_path"`
	WantedBytes         int64            `json:"wanted_bytes"`
	SelectedFilesKnown  bool             `json:"selected_files_known"`
	SelectedFileCount   int              `json:"selected_file_count"`
	SelectedFileSizes   []int64          `json:"selected_file_sizes,omitempty"`
	FileSizeFingerprint string           `json:"file_size_fingerprint,omitempty"`
	ContentGroupID      string           `json:"content_group_id,omitempty"`
	DataGroupID         string           `json:"data_group_id,omitempty"`
	AssignmentSource    AssignmentSource `json:"assignment_source,omitempty"`
	SuggestedAutoKey    string           `json:"suggested_auto_key,omitempty"`
	DownloaderOnline    bool             `json:"downloader_online"`
	Stale               bool             `json:"stale"`
	MetadataFingerprint string           `json:"metadata_fingerprint,omitempty"`
	RuntimeFingerprint  string           `json:"runtime_fingerprint,omitempty"`
	LastSeenAt          time.Time        `json:"last_seen_at,omitempty"`
	DeletedAt           *time.Time       `json:"deleted_at,omitempty"`
}

// Active reports whether the task is currently present in its downloader's
// last complete view.
func (t TorrentInstance) Active() bool {
	return t.DeletedAt == nil
}

// TorrentRuntime contains frequently changing values that can be persisted
// independently from TorrentInstance metadata.
type TorrentRuntime struct {
	TorrentInstanceID string    `json:"torrent_instance_id"`
	State             string    `json:"state"`
	Progress          float64   `json:"progress"`
	Ratio             float64   `json:"ratio"`
	UploadedBytes     int64     `json:"uploaded_bytes"`
	DownloadedBytes   int64     `json:"downloaded_bytes"`
	UploadSpeed       int64     `json:"upload_speed"`
	DownloadSpeed     int64     `json:"download_speed"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ContentGroup is a logical resource used for display, filtering, and unique
// site counts. A manual content merge must not merge the physical DataGroups.
type ContentGroup struct {
	ID             string          `json:"id"`
	Version        uint64          `json:"version"`
	Mode           GroupMode       `json:"mode"`
	Locked         bool            `json:"locked"`
	Confidence     GroupConfidence `json:"confidence"`
	AutoKey        string          `json:"auto_key,omitempty"`
	MemberIDs      []string        `json:"member_ids"`
	TaskCount      int             `json:"task_count"`
	UniqueSites    int             `json:"unique_sites"`
	LogicalBytes   int64           `json:"logical_bytes"`
	DataGroupCount int             `json:"data_group_count"`
}

// DataGroup is a physical data reference set used exclusively for safe file
// reference counting. Content-group editing must not alter it.
type DataGroup struct {
	ID                  string          `json:"id"`
	Version             uint64          `json:"version"`
	StorageID           string          `json:"storage_id"`
	CanonicalPath       string          `json:"canonical_path"`
	WantedBytes         int64           `json:"wanted_bytes"`
	SelectedFileCount   int             `json:"selected_file_count"`
	FileSizeFingerprint string          `json:"file_size_fingerprint,omitempty"`
	Confidence          GroupConfidence `json:"confidence"`
	PhysicalKey         string          `json:"physical_key"`
	MemberIDs           []string        `json:"member_ids"`
}

// Membership keeps logical and physical membership explicit. SuggestedAutoKey
// may change after a refresh; the stable group IDs are the authoritative
// assignments persisted by the application layer.
type Membership struct {
	TorrentInstanceID string           `json:"torrent_instance_id"`
	ContentGroupID    string           `json:"content_group_id"`
	DataGroupID       string           `json:"data_group_id"`
	AssignmentSource  AssignmentSource `json:"assignment_source"`
	SuggestedAutoKey  string           `json:"suggested_auto_key"`
}

// TrackerIdentity is deliberately passkey-free. RulePath may contain only a
// non-secret static path discriminator, never a complete announce URL.
type TrackerIdentity struct {
	TorrentInstanceID string `json:"torrent_instance_id"`
	SiteID            string `json:"site_id,omitempty"`
	Host              string `json:"host"`
	RulePath          string `json:"rule_path,omitempty"`
}

// DownloaderState supplies storage-wide freshness information to destructive
// planners. Every downloader that can see a storage should be listed.
type DownloaderState struct {
	ID         string   `json:"id"`
	StorageIDs []string `json:"storage_ids"`
	Online     bool     `json:"online"`
	Stale      bool     `json:"stale"`
}
