// file: internal/plugin/plugin.go
// version: 1.1.1
// last-edited: 2026-07-12

package plugin

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Capability identifies what a plugin can do.
type Capability string

const (
	CapMetadataSource  Capability = "metadata_source"
	CapDownloadClient  Capability = "download_client"
	CapMediaPlayer     Capability = "media_player"
	CapNotifier        Capability = "notifier"
	CapEventSubscriber Capability = "event_subscriber"
)

// Plugin is the base interface every plugin implements.
type Plugin interface {
	ID() string
	Name() string
	Version() string
	Capabilities() []Capability
	Init(ctx context.Context, deps Deps) error
	Shutdown(ctx context.Context) error
	HealthCheck() error
}

// Deps is the dependency bag passed to plugins during Init.
// Plugins use this to interact with the host. They never import
// internal/server.
type Deps struct {
	Store  database.Store
	Events *EventBus
	Config map[string]string
	Logger logger.Logger
	Router PluginRouter
}

// DownloadClient is VESTIGIAL — it has zero implementors and is NOT the seam
// for torrent-client work. It embeds this package's Plugin base
// (Capabilities/Init/Shutdown/HealthCheck), but the real download plugin
// (internal/plugins/deluge) implements a DIFFERENT base — pkg/plugin/sdk.Plugin
// — so a compile-assert against this interface can never hold without bridging
// two plugin frameworks. New torrent-client work extends
// internal/download.TorrentClient (which has real implementors and a config
// factory) instead of this interface.
type DownloadClient interface {
	Plugin
	TestConnection() error
	ListTorrents() ([]TorrentInfo, error)
	MoveStorage(torrentHash, newPath string) error
}

// TorrentInfo describes a torrent known to the download client.
type TorrentInfo struct {
	Hash      string  `json:"hash"`
	Name      string  `json:"name"`
	SavePath  string  `json:"save_path"`
	TotalSize int64   `json:"total_size"`
	Progress  float64 `json:"progress"`
	State     string  `json:"state"`
}

// MediaPlayer is implemented by plugins that sync with a media server.
type MediaPlayer interface {
	Plugin
	SyncLibrary(books []BookInfo) error
	GetPlaybackState(bookID string) (*PlaybackState, error)
	UpdatePlaybackState(bookID string, state PlaybackState) error
}

// BookInfo is a serializable summary of a book for media player sync.
type BookInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Path   string `json:"path"`
}

// PlaybackState represents playback position in a media player.
type PlaybackState struct {
	PositionSeconds float64 `json:"position_seconds"`
	Finished        bool    `json:"finished"`
}

// Notifier is implemented by plugins that send notifications.
type Notifier interface {
	Plugin
	Notify(ctx context.Context, event Event) error
	SupportedEvents() []EventType
}
