// file: internal/serviceregistry/keys.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-06-23

package serviceregistry

// Known service key constants for use with Get[T].
// Using these constants instead of bare string literals prevents typos and
// enables IDE navigation to the registration site via "Find Usages".
const (
	KeyActivity       = "activity"
	KeyActivityStore  = "activitystore"
	KeyAudiobook      = "audiobook"
	KeyBatch          = "batch"
	KeyConfig         = "config"
	KeyConfigUpdate   = "configupdate"
	KeyDashboard      = "dashboard"
	KeyDedup          = "dedup"
	KeyEmbeddingStore = "embeddingstore"
	KeyEventBus       = "eventbus"
	KeyFilesystem     = "filesystem"
	KeyImportPath     = "importpath"
	KeyITunes         = "itunes"
	KeyMerge          = "merge"
	KeyMetadataState  = "metadatastate"
	KeyMetaFetch      = "metafetch"
	KeyOpHub          = "ophub"
	KeyOrganize       = "organize"
	KeyQuarantine     = "quarantine"
	KeyScan           = "scan"
	KeyStore          = "store"
	KeySystem         = "system"
	KeyUpdater        = "updater"
	KeyWork           = "work"
)
