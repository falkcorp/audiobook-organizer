// file: internal/database/iface_assert.go
// version: 1.3.0
// guid: 2b9b0aba-e44f-43f0-a40b-56de5e95ab8e

package database

// Compile-time proof that PebbleStore satisfies every sub-interface
// defined in iface_*.go. If a method is ever removed from PebbleStore
// (or renamed) the compile fails here — long before any caller does.

var (
	_ Store               = (*PebbleStore)(nil)
	_ LifecycleStore      = (*PebbleStore)(nil)
	_ BookStore           = (*PebbleStore)(nil)
	_ AuthorStore         = (*PebbleStore)(nil)
	_ SeriesStore         = (*PebbleStore)(nil)
	_ UserStore           = (*PebbleStore)(nil)
	_ NarratorStore       = (*PebbleStore)(nil)
	_ WorkStore           = (*PebbleStore)(nil)
	_ SessionStore        = (*PebbleStore)(nil)
	_ RoleStore           = (*PebbleStore)(nil)
	_ APIKeyStore         = (*PebbleStore)(nil)
	_ InviteStore         = (*PebbleStore)(nil)
	_ UserPreferenceStore = (*PebbleStore)(nil)
	_ UserPositionStore   = (*PebbleStore)(nil)
	_ BookVersionStore    = (*PebbleStore)(nil)
	_ BookFileStore       = (*PebbleStore)(nil)
	_ BookSegmentStore    = (*PebbleStore)(nil)
	_ PlaylistStore       = (*PebbleStore)(nil)
	_ UserPlaylistStore   = (*PebbleStore)(nil)
	_ CollectionStore     = (*PebbleStore)(nil)
	_ ImportPathStore     = (*PebbleStore)(nil)
	_ OperationStore      = (*PebbleStore)(nil)
	_ TagStore            = (*PebbleStore)(nil)
	_ UserTagStore        = (*PebbleStore)(nil)
	_ MetadataStore       = (*PebbleStore)(nil)
	_ HashBlocklistStore  = (*PebbleStore)(nil)
	_ ITunesStateStore    = (*PebbleStore)(nil)
	_ PathHistoryStore    = (*PebbleStore)(nil)
	_ ExternalIDStore     = (*PebbleStore)(nil)
	_ RawKVStore          = (*PebbleStore)(nil)
	_ PlaybackStore       = (*PebbleStore)(nil)
	_ SettingsStore       = (*PebbleStore)(nil)
	_ StatsStore          = (*PebbleStore)(nil)
	_ MaintenanceStore    = (*PebbleStore)(nil)
	_ SystemActivityStore = (*PebbleStore)(nil)
	_ AIJobsStore         = (*PebbleStore)(nil)
	_ OpsV2Store          = (*PebbleStore)(nil)
)
