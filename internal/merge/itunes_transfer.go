// file: internal/merge/itunes_transfer.go
// version: 1.0.0
// guid: 888ac9c0-035e-4ee6-af7e-2584ce3269b9
// last-edited: 2026-09-14

package merge

import "github.com/falkcorp/audiobook-organizer/internal/database"

// TransferITunesMetadataFirstWin copies the six iTunes-provenance Book fields
// (persistent ID, play count, rating, date added, last played, bookmark) from a
// merged-away loser onto the surviving keep book, first-win: a field is only
// filled if the keep book does not already have it. These are Book-level fields,
// NOT external-ID mappings, so ReassignExternalIDs does not carry them -- any
// merge path that soft-deletes/hard-deletes a loser must apply this first or the
// loser's iTunes stats are stranded on the removed row.
//
// It lives in merge (not dedup, which imports merge) so that
// Service.MergeBooksWithOptions can apply it inside mergeSerializeMu, in the
// same write that marks the survivor primary (MergeOptions.CarryITunesFields).
// dedup.TransferITunesMetadataFirstWin forwards here for the legacy
// dedup.MergeBooks hard-delete path, so the copy semantics live in one place.
func TransferITunesMetadataFirstWin(keep, from *database.Book) {
	if keep == nil || from == nil {
		return
	}
	if (keep.ITunesPersistentID == nil || *keep.ITunesPersistentID == "") &&
		from.ITunesPersistentID != nil && *from.ITunesPersistentID != "" {
		keep.ITunesPersistentID = from.ITunesPersistentID
	}
	if keep.ITunesPlayCount == nil && from.ITunesPlayCount != nil {
		keep.ITunesPlayCount = from.ITunesPlayCount
	}
	if keep.ITunesRating == nil && from.ITunesRating != nil {
		keep.ITunesRating = from.ITunesRating
	}
	if keep.ITunesDateAdded == nil && from.ITunesDateAdded != nil {
		keep.ITunesDateAdded = from.ITunesDateAdded
	}
	if keep.ITunesLastPlayed == nil && from.ITunesLastPlayed != nil {
		keep.ITunesLastPlayed = from.ITunesLastPlayed
	}
	if keep.ITunesBookmark == nil && from.ITunesBookmark != nil {
		keep.ITunesBookmark = from.ITunesBookmark
	}
}
