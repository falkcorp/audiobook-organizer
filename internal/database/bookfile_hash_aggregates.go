// file: internal/database/bookfile_hash_aggregates.go
// version: 1.0.0
// guid: 5c1d4fb5-25bf-4c2f-8514-f851ed8cdb2c
// last-edited: 2026-10-31

package database

// When a hash write has to recompute its book's aggregates.
//
// A book's stored Duration and FileSize are not a plain sum of its rows. Two
// rules in the sum compare rows by content hash:
//
//   - copy clusters (OwnFolderFiles / IsBookFileCopy): rows that share a
//     FileHash are copies of each other and only one of them counts;
//   - missing duplicates (ComputeBookRuntime / missingDuplicateOf): a missing
//     row that shares a FileHash or OriginalFileHash with a present row is the
//     old half of a repoint and is dropped from Duration.
//
// So a hash write can change the aggregate, but only through EQUALITY between
// two rows' hashes. If neither the values a row held before nor the values it
// holds now equal any hash a sibling holds, no pair of rows compares
// differently than it did and the stored aggregate is still right. That is
// the common case (a tag write gives every chapter a fresh, unique digest), so
// UpdateBookFileHashes and SetBookFileHash only pay a recompute when a sibling
// shares a value. A 50-chapter write-back of unrelated hashes still costs no
// recompute; the book that holds a repoint pair costs one per write that
// touches it.
//
// Callers that write a whole chunk of one book's hashes and recompute once at
// the end (backfill-file-hashes: ModifyBookFileDeferAggregates, then
// RecomputeBookAggregates per book) do not come through here at all.

// hashWriteMovesAggregate reports whether replacing the row old with written
// (a hash-only write, already committed and visible to GetBookFilesForIDsCore)
// can change its book's stored aggregates, so the caller must recompute them.
//
// Only the hash values that actually moved are looked for among the siblings:
// a value the row held before and still holds is one equality that did not
// change. A read error answers true: an unneeded recompute is cheap, a missed
// one leaves Book.Duration stale with nothing to revisit it.
func (s *PebbleStore) hashWriteMovesAggregate(old, written *BookFile) bool {
	moved := map[string]struct{}{}
	add := func(v string) {
		if v != "" {
			moved[v] = struct{}{}
		}
	}
	if old.FileHash != written.FileHash {
		add(old.FileHash)
		add(written.FileHash)
	}
	if old.OriginalFileHash != written.OriginalFileHash {
		add(old.OriginalFileHash)
		add(written.OriginalFileHash)
	}
	if len(moved) == 0 {
		return false
	}
	byBook, err := s.GetBookFilesForIDsCore([]string{written.BookID})
	if err != nil {
		return true
	}
	for _, f := range byBook[written.BookID] {
		// The row itself is excluded; a retired row is not one of the book's
		// files (RecomputeBookAggregates reads GetBookFiles, which drops them).
		if f.ID == written.ID || f.Retired {
			continue
		}
		if _, hit := moved[f.FileHash]; hit && f.FileHash != "" {
			return true
		}
		if _, hit := moved[f.OriginalFileHash]; hit && f.OriginalFileHash != "" {
			return true
		}
	}
	return false
}
