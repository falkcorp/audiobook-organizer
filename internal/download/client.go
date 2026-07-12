// file: internal/download/client.go
// version: 1.2.0
// guid: 404055b4-a238-453f-80a7-f6303ab23ec1
// last-edited: 2026-07-12

// Package download provides torrent and Usenet client integrations.
package download

import (
	"context"
	"errors"
	"time"
)

// ErrRePointUnsupported is returned by TorrentClient.UpdateStoragePath
// implementations that have not yet implemented re-point-only relocation.
// It is fail-closed: callers must treat it as "the old path is still the one
// registered with the client" and must NOT assume the storage path changed.
var ErrRePointUnsupported = errors.New("re-point-only relocation not supported by this client")

// TorrentInfo is the read-only view of a single torrent that the organizer
// needs. Fields map directly to the native API responses of each client; the
// concrete adapters translate.
type TorrentInfo struct {
	ID              string        // Client-opaque identifier (hash or numeric ID)
	Name            string        // User-visible name / directory name
	DownloadDir     string        // Current download path on disk
	Status          TorrentStatus // Normalized state
	Progress        float64       // 0.0 – 1.0, download completion
	TotalUploaded   int64         // Lifetime bytes uploaded (for activity tracking)
	TotalDownloaded int64         // Lifetime bytes downloaded
	Files           []TorrentFile // Individual files inside this torrent
	CreatedAt       time.Time     // When the torrent was added to the client
	IsPaused        bool
}

// TorrentFile represents a file inside a torrent.
type TorrentFile struct {
	Path string // Relative path inside the torrent
	Size int64  // File size in bytes
}

// TorrentStatus is the normalized torrent state.
type TorrentStatus int

const (
	StatusDownloading TorrentStatus = iota
	StatusSeeding
	StatusPaused
	StatusStopped // Finished but not seeding (client-specific)
	StatusNotFound
)

// UploadStats is a lightweight snapshot for the cleanup job.
type UploadStats struct {
	TotalUploaded int64
	IsPaused      bool
	Exists        bool // False when the torrent has been removed from the client.
}

// TorrentClient abstracts a download client.
type TorrentClient interface {
	// Connect validates credentials and returns an error if the client
	// is unreachable. Called once at startup and on config change.
	Connect(ctx context.Context) error

	// GetTorrent returns full info for a single torrent by its client ID.
	// Returns nil, nil when the torrent does not exist (not an error).
	GetTorrent(ctx context.Context, id string) (*TorrentInfo, error)

	// GetUploadStats is a lightweight poll used by the shadow cleanup job.
	// It returns only the fields the cleanup loop needs.
	GetUploadStats(ctx context.Context, id string) (*UploadStats, error)

	// SetDownloadPath performs a PHYSICAL relocation of a torrent: it asks the
	// client to move the data to a new directory on disk (Deluge
	// core.move_storage / qBittorrent setLocation). Use UpdateStoragePath
	// instead when the caller has ALREADY moved the data and only needs the
	// client's registered path re-pointed.
	SetDownloadPath(ctx context.Context, id, newPath string) error

	// UpdateStoragePath is a RE-POINT-ONLY relocation: the caller has already
	// moved the data on disk and only needs the client's registered storage
	// path updated to match. Implementations MUST be fail-closed on RPC errors
	// — on any RPC failure the OLD path must stay registered (return the error;
	// never leave the client pointed at a path whose move was not confirmed).
	// A process crash inside a remove-before-re-add window (mechanism-A
	// residual) is documented in the spec's T2 spike protocol and is not
	// promisable away here. Implementors that have not yet landed a real
	// mechanism MUST return ErrRePointUnsupported (fail-closed).
	UpdateStoragePath(ctx context.Context, id, newPath string) error

	// RemoveTorrent removes the torrent from the client.
	RemoveTorrent(ctx context.Context, id string, deleteFiles bool) error

	// ListCompleted returns all torrents that have reached 100% download completion.
	ListCompleted(ctx context.Context) ([]TorrentInfo, error)

	// ClientType returns a human-readable label for logging and config disambiguation.
	ClientType() string
}

// NZBInfo is the read-only view of a single Usenet job (NZB download).
type NZBInfo struct {
	ID          string       // Client-opaque identifier
	Name        string       // User-visible name
	DownloadDir string       // Current download path on disk
	Status      UsenetStatus // Normalized state
	Progress    float64      // 0.0 – 1.0, download completion
	TotalBytes  int64        // Total bytes expected (if known)
	Files       []NZBFile    // Files included in the NZB
	CreatedAt   time.Time    // When the job was added to the client
	IsPaused    bool
}

// NZBFile represents a file inside a Usenet job.
type NZBFile struct {
	Path string // Relative path inside the job
	Size int64  // File size in bytes
}

// UsenetStatus is the normalized Usenet job state.
type UsenetStatus int

const (
	UsenetStatusQueued UsenetStatus = iota
	UsenetStatusDownloading
	UsenetStatusCompleted
	UsenetStatusPaused
	UsenetStatusFailed
	UsenetStatusNotFound
)

// UsenetStats is a lightweight snapshot for cleanup or monitoring jobs.
type UsenetStats struct {
	TotalDownloaded int64
	IsPaused        bool
	Exists          bool // False when the job has been removed from the client.
}

// UsenetClient abstracts a Usenet download client (NZB-based).
type UsenetClient interface {
	// Connect validates credentials and returns an error if the client
	// is unreachable. Called once at startup and on config change.
	Connect(ctx context.Context) error

	// GetJob returns full info for a single Usenet job by its client ID.
	// Returns nil, nil when the job does not exist (not an error).
	GetJob(ctx context.Context, id string) (*NZBInfo, error)

	// GetQueueStats is a lightweight poll used by cleanup or monitoring jobs.
	GetQueueStats(ctx context.Context, id string) (*UsenetStats, error)

	// SetDownloadPath relocates a job to a new directory on disk.
	SetDownloadPath(ctx context.Context, id, newPath string) error

	// RemoveJob removes the job from the client.
	RemoveJob(ctx context.Context, id string, deleteFiles bool) error

	// ListCompleted returns all jobs that have reached 100% completion.
	ListCompleted(ctx context.Context) ([]NZBInfo, error)

	// ClientType returns a human-readable label for logging and config disambiguation.
	ClientType() string
}
