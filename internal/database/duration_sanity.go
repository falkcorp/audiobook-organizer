// file: internal/database/duration_sanity.go
// version: 1.0.0
// guid: 8b1f4d2c-5e69-4a30-9c71-2f8a6b0d4e95
// last-edited: 2026-06-19

package database

import "log/slog"

// CONS-18 / CONS-16: BookFile.Duration is seconds by convention, but iTunes
// TotalTime is milliseconds. A caller that forgets the /1000 stores values 1000×
// too large, which then poison Book.Duration via RecomputeBookAggregates and the
// dedup pipeline. These helpers detect that mistake from the file's implied
// bitrate (no codec/bitrate metadata required) so the store can repair it at the
// write chokepoint regardless of which caller produced the row.

// Bitrate band (bits/sec) used to judge whether a duration is plausible when read
// as seconds. The ms/seconds confusion shifts the implied bitrate by ~1000×,
// while real audiobook bitrate spans only ~20× (4 kbps spoken-word … ~1.4 Mbps
// lossless), so a single 4 kbps floor separates the two without ever flagging a
// genuine low-bitrate file.
const (
	minPlausibleBitsPerSec = 4_000     // 4 kbps — below any real audio codec
	maxPlausibleBitsPerSec = 3_000_000 // 3 Mbps — above lossless; upper sanity bound
)

// DurationLooksLikeMillis reports whether durationSec (stored in the seconds
// field) is actually a milliseconds value, judged by the bitrate it would imply
// for a file of fileSize bytes.
//
//   - Implied bitrate within a plausible audio range → legitimate seconds.
//   - Implied bitrate impossibly low (< 4 kbps) → the duration is too large for
//     the file: milliseconds. Confirmed only if dividing by 1000 lands back inside
//     the plausible band (rejects the rare genuinely sub-4 kbps clip, whose /1000
//     would imply an absurd multi-Mbps bitrate).
func DurationLooksLikeMillis(fileSize int64, durationSec int) bool {
	if fileSize <= 0 || durationSec <= 0 {
		return false // cannot judge / nothing to fix
	}

	impliedBitsPerSec := fileSize * 8 / int64(durationSec)
	if impliedBitsPerSec >= minPlausibleBitsPerSec {
		return false // plausible as seconds
	}

	correctedSec := durationSec / 1000
	if correctedSec <= 0 {
		return false // would round to zero — refuse to corrupt
	}
	correctedBitsPerSec := fileSize * 8 / int64(correctedSec)
	return correctedBitsPerSec >= minPlausibleBitsPerSec &&
		correctedBitsPerSec <= maxPlausibleBitsPerSec
}

// normalizeBookFileDuration repairs a millisecond-valued Duration in place at the
// storage chokepoint (CreateBookFile / UpsertBookFile / BatchUpsertBookFiles).
// It only ever rewrites a value that DurationLooksLikeMillis flags, so plausible
// durations and indeterminate cases (no FileSize) pass through untouched. Returns
// true if it changed the value. The fix is one factor of 1000 (the only known
// bug); it never loops.
func normalizeBookFileDuration(file *BookFile) bool {
	if file == nil {
		return false
	}
	if !DurationLooksLikeMillis(file.FileSize, file.Duration) {
		return false
	}
	old := file.Duration
	file.Duration = file.Duration / 1000
	slog.Warn("normalizeBookFileDuration: repaired millisecond duration at write chokepoint",
		"book_id", file.BookID,
		"file_id", file.ID,
		"old_duration", old,
		"new_duration_sec", file.Duration,
		"file_size", file.FileSize,
	)
	return true
}
