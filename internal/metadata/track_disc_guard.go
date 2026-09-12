// file: internal/metadata/track_disc_guard.go
// version: 1.0.0
// guid: 3c299cdb-8785-4fa4-b306-ff8cd601f126
// last-edited: 2026-09-12

package metadata

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TagVerdict is the per-book decision on whether tag track/disc numbers may
// replace positional ones.
type TagVerdict int

const (
	TagTakePositions   TagVerdict = iota // tag numbers distinct across the whole book
	TagRefuseDuplicate                   // two files share a (disc, track) pair
	TagRefuseMissing                     // some file has no known tag track number
	TagRefuseMixedDisc                   // some files have a tag disc number and some do not
)

// String names the verdict for logs and summaries.
func (v TagVerdict) String() string {
	switch v {
	case TagTakePositions:
		return "take"
	case TagRefuseDuplicate:
		return "duplicate"
	case TagRefuseMissing:
		return "missing-number"
	case TagRefuseMixedDisc:
		return "mixed-disc"
	default:
		return fmt.Sprintf("TagVerdict(%d)", int(v))
	}
}

// TagPlacement is one file's position as its tags state it. Disc is 0 when the
// tag carries no disc number; Track is 0 when it carries no track number.
type TagPlacement struct{ Track, TrackTotal, Disc, DiscTotal int }

// PlacementFromMetadata is the TagPlacement a tag reading states.
func PlacementFromMetadata(m Metadata) TagPlacement {
	return TagPlacement{Track: m.TrackNumber, TrackTotal: m.TrackTotal, Disc: m.DiscNumber, DiscTotal: m.DiscTotal}
}

type tagPositionKey struct{ disc, track int }

// JudgeTagPositions decides whether a book's tag track/disc numbers may be
// trusted. It is the one copy of this guard: the scanner calls it at import
// and maintenance.tag-backfill calls it when backfilling existing rows.
//
// keys and placements are parallel slices covering EVERY file of ONE book, in
// the book's order; keys[i] names file i (a row ID or a path) and is used only
// in the returned reason and as the key of the returned map, so keys must be
// distinct. The caller is responsible for completeness: a file left out of the
// slices cannot be judged, and a file whose tags could not be read must be
// passed with a zero TagPlacement so it refuses.
//
// A file with no tag track number (Track <= 0) makes the book "not distinct"
// and it is refused. This fails closed on purpose: refusing costs only the tag
// track numbers (the positional order stays), while a wrong accept scrambles
// chapter order.
//
// Positions are compared as (disc, track) pairs, so a multi-disc book with
// track 1 on each disc passes and two files both at track 1 on the same disc do
// not. Disc-tag presence must be uniform: either every file's tag carries a
// disc number or none does (then all are disc 0). A book that mixes the two is
// refused as "mixed-disc", because absent disc = 0 would never collide with
// disc 1 yet would sort the disc-less files first — e.g. disc 1 tagged TPOS=1
// tracks 1-5 and disc 2 untagged tracks 1-5 would otherwise be accepted with
// disc 2 playing before disc 1. A single-file book passes iff its tag carries a
// track number. Checks run per file in slice order: a missing track refuses
// first, then a disc-presence mismatch, then a duplicate pair. An empty book is
// vacuously accepted with an empty map: there is nothing to place.
//
// On accept it returns every file's placement keyed by keys[i], so the caller
// can move the whole book to its tag positions (see ApplyTagPlacement).
func JudgeTagPositions(keys []string, placements []TagPlacement) (TagVerdict, string, map[string]TagPlacement) {
	if len(keys) != len(placements) {
		return TagRefuseMissing, fmt.Sprintf("%d keys for %d placements", len(keys), len(placements)), nil
	}
	out := make(map[string]TagPlacement, len(keys))
	seen := make(map[tagPositionKey]string, len(keys))
	firstHasDisc := false
	for i, p := range placements {
		if p.Track <= 0 {
			return TagRefuseMissing, fmt.Sprintf("file %s has no tag track", keys[i]), nil
		}
		if hasDisc := p.Disc > 0; i == 0 {
			firstHasDisc = hasDisc
		} else if hasDisc != firstHasDisc {
			return TagRefuseMixedDisc, fmt.Sprintf("disc tag on only some files: %s has-disc=%v, %s has-disc=%v",
				keys[0], firstHasDisc, keys[i], hasDisc), nil
		}
		key := tagPositionKey{disc: max(p.Disc, 0), track: p.Track}
		if other, dup := seen[key]; dup {
			return TagRefuseDuplicate, fmt.Sprintf("disc %d track %d on %s and %s", key.disc, key.track, other, keys[i]), nil
		}
		seen[key] = keys[i]
		out[keys[i]] = p
	}
	return TagTakePositions, "", out
}

// ApplyTagPlacement moves a row to its tag position and reports whether any
// position field changed. TrackNumber and DiscNumber are set exactly as the tag
// states them (DiscNumber 0 when the tag has no disc) so the row sorts where the
// judgement placed it. A tag total replaces the stored count; with no disc, the
// disc count is cleared too, since a disc count without a disc is meaningless.
// A missing track total keeps the stored TrackCount, which does not affect order.
// Call it only for a book JudgeTagPositions accepted.
func ApplyTagPlacement(u *database.BookFile, p TagPlacement) bool {
	before := [4]int{u.TrackNumber, u.TrackCount, u.DiscNumber, u.DiscCount}
	u.TrackNumber = p.Track
	if p.TrackTotal > 0 {
		u.TrackCount = p.TrackTotal
	}
	u.DiscNumber = max(p.Disc, 0)
	switch {
	case p.DiscTotal > 0:
		u.DiscCount = p.DiscTotal
	case u.DiscNumber == 0:
		u.DiscCount = 0
	}
	return before != [4]int{u.TrackNumber, u.TrackCount, u.DiscNumber, u.DiscCount}
}
