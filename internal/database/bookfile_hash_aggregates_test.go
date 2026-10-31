// file: internal/database/bookfile_hash_aggregates_test.go
// version: 1.0.0
// guid: 0e7b9a52-4d3c-4f18-9a6b-c1d58e2f7a34
// last-edited: 2026-10-31

package database

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/stretchr/testify/require"
)

// SetBookFileHash and UpdateBookFileHashes used to skip the owning book's
// RecomputeBookAggregates on the claim that a hash feeds no aggregate. It does:
// a missing row whose hash matches a present row is the old half of a repoint
// and is dropped from Book.Duration (book_runtime.go), and rows that share a
// FileHash are copies of which only one counts (book_own_folder.go). A hash
// written onto the present row that pairs it with its twin left Book.Duration
// at the double count until something else recomputed the book.

const (
	hashAggDur  = 600
	hashAggSize = 1000
	hashAggTwin = "hash-batcave"
)

// hashAggShape builds the stale-aggregate setup. The present row A carries no
// hash; its twin B does. Both carry hashAggDur seconds, so while they are not
// paired the book reads 2*hashAggDur and once they are it reads hashAggDur.
type hashAggShape struct {
	name string
	twin func(bookID string) *BookFile
}

var hashAggShapes = []hashAggShape{
	{
		// book_runtime.go's rule: the twin is MISSING and shares an
		// OriginalFileHash with the present row. Its FileHash is empty, so the
		// copy-cluster rule (FileHash equality) cannot explain the result.
		name: "missing repoint twin by original hash",
		twin: func(bookID string) *BookFile {
			return &BookFile{
				BookID: bookID, FilePath: "/old/Batcave/zz.m4b", OriginalFilename: "zz.m4b",
				OriginalFileHash: hashAggTwin, Missing: true,
				Duration: hashAggDur, FileSize: hashAggSize,
			}
		},
	},
	{
		// book_own_folder.go's rule: a PRESENT twin outside the book's folder
		// sharing the FileHash is a copy, and only one of the pair counts.
		name: "present copy by file hash",
		twin: func(bookID string) *BookFile {
			return &BookFile{
				BookID: bookID, FilePath: "/elsewhere/Batcave/zz.m4b", OriginalFilename: "zz.m4b",
				FileHash: hashAggTwin,
				Duration: hashAggDur, FileSize: hashAggSize,
			}
		},
	},
}

// hashAggBook creates a book with the present row A (no hash) and the shape's
// twin B, and returns the store, the book ID and both row IDs. The stored
// Duration is checked to be the unpaired double count, so a shape that already
// pairs its rows (and so proves nothing) fails here, not silently.
func hashAggBook(t *testing.T, shape hashAggShape) (s *PebbleStore, bookID, aID, bID string) {
	t.Helper()
	s = openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Batcave Ledger", FilePath: "/lib/Batcave"})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: "/lib/Batcave/01.m4b", OriginalFilename: "01.m4b",
		Duration: hashAggDur, FileSize: hashAggSize,
	}))
	require.NoError(t, s.CreateBookFile(shape.twin(book.ID)))
	files, err := s.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, f := range files {
		if f.FilePath == "/lib/Batcave/01.m4b" {
			aID = f.ID
		} else {
			bID = f.ID
		}
	}
	require.NotEmpty(t, aID)
	require.NotEmpty(t, bID)
	require.Equal(t, 2*hashAggDur, hashAggBookDuration(t, s, book.ID),
		"setup: the unpaired rows must read as a double count")
	return s, book.ID, aID, bID
}

func hashAggBookDuration(t *testing.T, s *PebbleStore, bookID string) int {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.NotNil(t, b.Duration)
	return *b.Duration
}

// A hash stored through either public path that pairs the present row with
// its twin must leave Book.Duration at the single count.
func TestHashWriters_PairingHashRecomputesBookDuration(t *testing.T) {
	writers := []struct {
		name  string
		write func(s *PebbleStore, fileID, hash string) error
	}{
		{"SetBookFileHash", func(s *PebbleStore, id, h string) error { return s.SetBookFileHash(id, h) }},
		{"UpdateBookFileHashes", func(s *PebbleStore, id, h string) error {
			// The tag-write recorder's argument order: pre-write sampled digest,
			// whole-file SHA-256, post-write sampled digest (the new file_hash).
			return s.UpdateBookFileHashes(id, h, "post-metadata-sha", h)
		}},
	}
	for _, shape := range hashAggShapes {
		for _, w := range writers {
			t.Run(shape.name+"/"+w.name, func(t *testing.T) {
				s, bookID, aID, _ := hashAggBook(t, shape)
				require.NoError(t, w.write(s, aID, hashAggTwin))
				require.Equal(t, hashAggDur, hashAggBookDuration(t, s, bookID),
					"the hash pairs A with its twin; Book.Duration must drop the duplicate")
			})
		}
	}
}

// The reverse: a write that moves the present row's hash OFF the twin's value
// un-pairs them, and the duplicate counts again.
func TestUpdateBookFileHashes_UnpairingHashRecomputesBookDuration(t *testing.T) {
	for _, shape := range hashAggShapes {
		t.Run(shape.name, func(t *testing.T) {
			s, bookID, aID, _ := hashAggBook(t, shape)
			require.NoError(t, s.UpdateBookFileHashes(aID, hashAggTwin, "sha-1", hashAggTwin))
			require.Equal(t, hashAggDur, hashAggBookDuration(t, s, bookID), "setup: paired")

			require.NoError(t, s.UpdateBookFileHashes(aID, "", "sha-2", "hash-rewritten"))
			wantOrigStillPaired := shape.name == "missing repoint twin by original hash"
			if wantOrigStillPaired {
				// OriginalFileHash is frozen once recorded (originalHashFrozen), so A
				// still shares it with the missing twin: still one count.
				require.Equal(t, hashAggDur, hashAggBookDuration(t, s, bookID))
				return
			}
			require.Equal(t, 2*hashAggDur, hashAggBookDuration(t, s, bookID),
				"A's file_hash moved off the twin's; the copy rule no longer drops it")
		})
	}
}

// The cost side of the fix: a hash that equals no sibling's, before or after,
// cannot change the aggregate, so a write-back of fresh hashes across a whole
// book recomputes it zero times (it used to be once per file, then not at all).
func TestHashWriters_UnrelatedHashesDoNotRecompute(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Gotham Serial", FilePath: "/lib/Gotham"})
	require.NoError(t, err)
	for i := range 6 {
		require.NoError(t, s.CreateBookFile(&BookFile{
			BookID: book.ID, FilePath: fmt.Sprintf("/lib/Gotham/%02d.m4b", i), OriginalFilename: fmt.Sprintf("%02d.m4b", i),
			Duration: 100 + i, FileSize: int64(1000 + i),
		}))
	}
	files, err := s.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 6)

	logs := aggtest.Capture(t)
	for i, f := range files {
		if i%2 == 0 {
			require.NoError(t, s.SetBookFileHash(f.ID, fmt.Sprintf("set-%d", i)))
		} else {
			require.NoError(t, s.UpdateBookFileHashes(f.ID, fmt.Sprintf("orig-%d", i), fmt.Sprintf("sha-%d", i), fmt.Sprintf("fh-%d", i)))
		}
	}
	require.Zero(t, aggtest.CountInvocations(logs(), book.ID),
		"hashes that match no sibling must not recompute the book")
}

// The bulk shape backfill-file-hashes uses: hashes stored through
// ModifyBookFileDeferAggregates, then ONE RecomputeBookAggregates per book.
// The deferred writer must not recompute per row even when a row's hash pairs
// it with a twin (that is the caller's one recompute), and the single recompute
// must leave Book.Duration correct.
func TestDeferredHashBulkPath_RecomputesOncePerBook(t *testing.T) {
	s, bookID, aID, _ := hashAggBook(t, hashAggShapes[0])
	// Extra present, unrelated rows: the chunk stores hashes for all of them.
	var ids []string
	ids = append(ids, aID)
	for i := range 4 {
		require.NoError(t, s.CreateBookFile(&BookFile{
			BookID: bookID, FilePath: fmt.Sprintf("/lib/Batcave/x%02d.m4b", i), OriginalFilename: fmt.Sprintf("x%02d.m4b", i),
			Duration: 50 + i, FileSize: int64(2000 + i),
		}))
	}
	all, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	before := hashAggBookDuration(t, s, bookID)
	for _, f := range all {
		if !f.Missing && f.ID != aID {
			ids = append(ids, f.ID)
		}
	}

	logs := aggtest.Capture(t)
	for _, id := range ids {
		h := fmt.Sprintf("bulk-%s", id)
		if id == aID {
			h = hashAggTwin
		}
		_, err := s.ModifyBookFileDeferAggregates(bookID, id, func(row *BookFile) error {
			row.FileHash = h
			if row.OriginalFileHash == "" {
				row.OriginalFileHash = h
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Zero(t, aggtest.CountInvocations(logs(), bookID), "the deferred writer must not recompute per row")
	require.Equal(t, before, hashAggBookDuration(t, s, bookID), "nothing recomputed yet")

	require.NoError(t, s.RecomputeBookAggregates(bookID))
	require.Equal(t, 1, aggtest.CountInvocations(logs(), bookID), "the caller's one recompute per book")
	require.Equal(t, before-hashAggDur, hashAggBookDuration(t, s, bookID),
		"the stored hashes pair A with its missing twin; the one recompute drops the duplicate")
}
