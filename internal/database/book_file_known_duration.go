// file: internal/database/book_file_known_duration.go
// version: 1.1.0
// guid: 5c7e2a91-3d4b-4f68-a1e0-9b2c6d8f4a37
// last-edited: 2026-11-04

package database

// KnownDurationAtPath returns the measured duration, in seconds, that one of
// rows records for the file at a single path, or 0 when none does. rows are the
// book_file rows claiming ONE path (live or soft-deleted books alike: the same
// path with the same size is the same file whatever state its book is in), and
// size and hash describe the file the caller wants a duration for.
//
// A row counts only when it
//   - is not marked Missing (a stale claim on a file nobody could see),
//   - records a positive duration,
//   - records exactly size bytes (size must be positive: an unknown size
//     proves nothing about identity),
//   - does not contradict hash: when both hash and the row's FileHash are set
//     they must be equal, and
//   - is not a legacy milliseconds value read as seconds
//     (DurationLooksLikeMillis).
//
// It is read-only. The scanner, the folder-books fixer and the duration
// backfill share this one rule so a borrowed duration means the same thing in
// each.
func KnownDurationAtPath(rows []BookFileCore, size int64, hash string) int {
	if size <= 0 {
		return 0
	}
	for _, r := range rows {
		sec, known := r.DurationSec()
		if r.Missing || !known || r.FileSize != size {
			continue
		}
		if hash != "" && r.FileHash != "" && r.FileHash != hash {
			continue
		}
		if DurationLooksLikeMillis(r.FileSize, sec) {
			continue
		}
		return sec
	}
	return 0
}
