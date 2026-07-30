// file: internal/syncapi/progress/bookmarks.go
// version: 1.0.0
// guid: 5666fa81-dccd-4eb3-b60f-b3050aa10c55
// last-edited: 2026-07-30

package progress

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Bookmark is a named, server-persisted position within one (user, item)'s
// audio. "item" is opaque here, same as Progress.CurrentTime's item -- a raw
// Book ULID today, an ABS sync_item syncID once TASK-01 ships. Deliberately
// has no separate ID field: real ABS keys a bookmark by (libraryItemId, time)
// -- the create/update/delete surface addresses it by time, not by an opaque
// ID -- so this type mirrors that rather than inventing a redundant key.
type Bookmark struct {
	UserID    string
	ItemID    string
	TimeSec   float64 // the natural key within (UserID, ItemID); see CanonicalTimeKey
	Title     string
	CreatedAt int64 // ms epoch
	UpdatedAt int64 // ms epoch
}

// ParseTimeSec parses a bookmark `time` value from either an HTTP request
// body's JSON number or a URL path segment string. Uses strconv.ParseFloat
// (never ParseInt) specifically because AudioBooth sends bookmark `time` as
// an Int in some paths (including the URL path segment on delete) while
// other call sites round-trip it as a Double -- ParseFloat accepts both
// "12" and "12.5" and "12.0" and returns the same float64 for "12" and
// "12.0". This is the single normalization point; callers must not add a
// second int-vs-float parsing path elsewhere.
func ParseTimeSec(raw string) (float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("progress: invalid bookmark time %q: %w", raw, err)
	}
	return v, nil
}

// CanonicalTimeKey converts a bookmark time in seconds into a sortable,
// collision-safe string suitable for use as (part of) a storage key: rounds
// to the nearest millisecond and zero-pads to a fixed width so lexicographic
// ordering matches numeric ordering. This guarantees "12" and "12.0" and
// "11.9996" (float rounding noise) all resolve to the identical stored
// bookmark, which is what "accept both Int and Double" actually requires at
// the storage layer -- the type-level distinction is erased here, not at
// the JSON boundary.
func CanonicalTimeKey(timeSec float64) string {
	ms := int64(math.Round(timeSec * 1000))
	if ms < 0 {
		ms = 0
	}
	// Zero-padded to 16 digits: comfortably wider than any realistic audio
	// duration in milliseconds (16 digits covers ~317,000 years), so
	// lexicographic string ordering always matches numeric ordering.
	return fmt.Sprintf("%016d", ms)
}

// ValidateBookmark checks the invariants a Bookmark must satisfy before
// being persisted: UserID, ItemID non-empty, TimeSec >= 0 (a negative
// position is never valid), Title non-empty (real ABS requires a title;
// an untitled bookmark is not a supported case here). Returns a descriptive
// error, never panics.
func ValidateBookmark(b Bookmark) error {
	if b.UserID == "" {
		return errors.New("progress: bookmark UserID must not be empty")
	}
	if b.ItemID == "" {
		return errors.New("progress: bookmark ItemID must not be empty")
	}
	if b.TimeSec < 0 {
		return fmt.Errorf("progress: bookmark TimeSec must be >= 0, got %v", b.TimeSec)
	}
	if b.Title == "" {
		return errors.New("progress: bookmark Title must not be empty")
	}
	return nil
}
