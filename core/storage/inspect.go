// file: core/storage/inspect.go
// version: 1.0.0
// guid: 6f3b8c14-92d5-4e07-b1a8-3c5e7d0a4f92
// last-edited: 2026-11-12

package storage

import "maps"

// Versions is each owner's applied migration version as Open read it from the
// store: what 05-backup's manifest records per keyspace and what its staging
// compares with a build's UpgradeFloor. It reads the store's own bookkeeping, so
// it is also answerable for a read-only open of an archived store. The map is a
// copy.
func (o *Opened) Versions() map[Owner]int { return maps.Clone(o.db.boot.versions) }

// PebbleFormat is the store's Pebble format major version as opened (05-backup
// §5B.3.4 step 4 compares it with the build's pin).
func (o *Opened) PebbleFormat() uint64 { return uint64(o.db.engine().FormatMajorVersion()) }
