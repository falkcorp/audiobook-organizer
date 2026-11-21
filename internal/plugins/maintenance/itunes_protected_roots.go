// file: internal/plugins/maintenance/itunes_protected_roots.go
// version: 0.1.0
// guid: 6e1a9d47-2c8b-4f35-b9d0-73a5e4c1f826
// last-edited: 2026-11-21

package maintenance

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// itunesProtectedRoots resolves the iTunes library roots the path-moving ops
// must stay outside, from the running config's iTunes section. Every op that
// fails closed on an unresolvable root reads it here, so the package reads
// that section through one config.Snapshot (internal/arch's AppConfig
// ratchet counts each one).
func itunesProtectedRoots() ([]string, error) {
	return merge.ITunesProtectedRoots(config.Snapshot().ITunes)
}
