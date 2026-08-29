// file: internal/server/diskstats.go
// version: 2.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-08-29

package server

import "github.com/falkcorp/audiobook-organizer/internal/diskstats"

// getDiskStats returns total, free bytes for the given path.
//
// The build-tagged syscall implementations moved to internal/diskstats so
// internal/backup can run a pre-flight free-space check without importing
// internal/server. This stays as a thin alias because it is injected as a func
// value into the system handler (wire_handlers.go), and that seam is worth
// keeping stable.
func getDiskStats(path string) (total, free uint64, err error) {
	return diskstats.Stats(path)
}
