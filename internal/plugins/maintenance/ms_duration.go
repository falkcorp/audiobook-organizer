// file: internal/plugins/maintenance/ms_duration.go
// version: 1.0.0
// guid: 3f9a1d47-58c2-4e6b-9a03-7c41bd2e8f56
// last-edited: 2026-09-21

package maintenance

import "github.com/falkcorp/audiobook-organizer/internal/database"

// durationLooksLikeMillis reports whether a stored duration is a millisecond
// value that was never divided by 1000 (a legacy iTunes import path stored
// TotalTime raw). Such a row inflates every duration-derived number by ~1000x.
//
// Detection is by implied bitrate — a value is only treated as milliseconds
// when reading it as seconds implies an impossible file — so a genuine
// duration is never touched.
//
// This used to back two whole operations, maintenance.duration-backfill and
// maintenance.purge-millisecond-durations, which were the same check written
// twice and neither of which could fill in a MISSING duration. Both were
// retired on 2026-09-21 and the check now runs as one per-segment step inside
// maintenance.duration-backfill (see duration_reextract.go).
//
// The predicate itself lives in the database package so the store's write gate
// (CONS-18) and this check share one implementation.
func durationLooksLikeMillis(fileSize int64, durationSec int) bool {
	return database.DurationLooksLikeMillis(fileSize, durationSec)
}
