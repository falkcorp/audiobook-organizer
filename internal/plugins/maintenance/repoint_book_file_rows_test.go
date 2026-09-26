// file: internal/plugins/maintenance/repoint_book_file_rows_test.go
// version: 1.0.0
// guid: 4c8e1a57-92d3-4f6b-b0a4-6e3f9d2c7b18
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/stretchr/testify/require"
)

// rbfFixture: one book with a MISSING row (old path absent on disk) and a
// present sibling row; the missing row's bytes sit, rowless, at newPath.
type rbfFixture struct {
	s               *database.PebbleStore
	root            string
	book            string
	row, sibling    string
	oldPath         string
	newPath         string
	content         []byte
	contentHash     string
	siblingPath     string
	siblingDuration int
}

const (
	rbfBookID    = "01RBFBOOK000000000000000AA"
	rbfRowID     = "01RBFROW0000000000000000AB"
	rbfSiblingID = "01RBFSIB0000000000000000AC"
	rbfOtherBook = "01RBFOTHER00000000000000AD"
)

func writeRBFFile(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

// newRBFFixture builds the fixture. rowSize and rowHash override what the
// missing row records ("-" keeps the file's true hash / size).
func newRBFFixture(t *testing.T, rowSize int64, rowHash string) rbfFixture {
	t.Helper()
	s := newRepairPebble(t)
	root := t.TempDir()
	f := rbfFixture{
		s: s, root: root, book: rbfBookID, row: rbfRowID, sibling: rbfSiblingID,
		oldPath:     filepath.Join(root, "Rebels of Last World", "01.mp3"),
		newPath:     filepath.Join(root, "Rebels of Last World", "Rebels of Last World", "01", "01.mp3"),
		siblingPath: filepath.Join(root, "Rebels of Last World", "02.mp3"),
		content:     []byte(strings.Repeat("ID3-audio-bytes-", 64)),
	}
	writeRBFFile(t, f.newPath, f.content)
	writeRBFFile(t, f.siblingPath, []byte("sibling"))
	h, err := filehash.BookFileHash(f.newPath)
	require.NoError(t, err)
	f.contentHash = h
	if rowSize < 0 {
		rowSize = int64(len(f.content))
	}
	if rowHash == "-" {
		rowHash = h
	}
	_, err = s.CreateBook(&database.Book{ID: f.book, Title: "Rebels of Last World", FilePath: filepath.Dir(f.oldPath)})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: f.row, BookID: f.book, FilePath: f.oldPath,
		TrackNumber: 1, Duration: 600, FileSize: rowSize, FileHash: rowHash, Missing: true}))
	f.siblingDuration = 700
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: f.sibling, BookID: f.book, FilePath: f.siblingPath,
		TrackNumber: 2, Duration: f.siblingDuration, FileSize: 7}))
	return f
}

func (f rbfFixture) item() rbfRepoint {
	return rbfRepoint{RowID: f.row, BookID: f.book, NewPath: f.newPath}
}

func runRBF(t *testing.T, store rbfStore, live bool, reporter *opIDReporter, items ...rbfRepoint) *rbfReport {
	t.Helper()
	p := rbfParams{Repoints: items}
	if live {
		v := false
		p.DryRunSnake = &v
	}
	if reporter == nil {
		reporter = &opIDReporter{id: "01RBFOP00000000000000000ZZ"}
	}
	rep, err := repointBookFileRows(context.Background(), rbfEnv{store: store}, p, reporter)
	require.NoError(t, err)
	return rep
}

func (f rbfFixture) storedRow(t *testing.T) *database.BookFile {
	t.Helper()
	row, err := f.s.GetBookFileByID(f.book, f.row)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row
}

func TestRepointBookFileRows_PreviewWritesNothing(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	rep := runRBF(t, f.s, false, nil, f.item())
	require.True(t, rep.DryRun)
	require.Equal(t, 1, rep.Planned)
	require.Equal(t, 0, rep.Repointed)
	r := rep.Repoints[0]
	require.Equal(t, rbfStatusPlanned, r.Status, r.Reason)
	require.Equal(t, rbfGates{rbfGatePass, rbfGatePass, rbfGatePass, rbfGatePass, rbfGatePass, rbfGatePass, rbfGatePass}, r.Gates)
	require.Equal(t, f.contentHash, r.DiskHash)
	require.Len(t, rep.Before, 1)
	require.Equal(t, 1, rep.Before[0].Missing)
	require.Empty(t, rep.After)

	row := f.storedRow(t)
	require.Equal(t, f.oldPath, row.FilePath, "a preview must not repoint the row")
	require.True(t, row.Missing)
	changes, err := f.s.GetOperationChanges("01RBFOP00000000000000000ZZ")
	require.NoError(t, err)
	require.Empty(t, changes, "a preview must not journal")
	got, err := f.s.GetBookFileByPath(f.newPath)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestRepointBookFileRows_LiveRepointsJournalsAndRefreshes(t *testing.T) {
	// A row with a size and no hash (the "Rebels" shape): size alone gates,
	// and the apply stores the recomputed hash.
	f := newRBFFixture(t, -1, "")
	opID := "01RBFOPLIVE0000000000000ZZ"
	rep := runRBF(t, f.s, true, &opIDReporter{id: opID}, f.item())
	require.False(t, rep.DryRun)
	require.Equal(t, 1, rep.Repointed, rep.Repoints[0].Error)
	r := rep.Repoints[0]
	require.Equal(t, rbfStatusRepointed, r.Status)
	require.Equal(t, rbfGateSkip, r.Gates.HashMatch, "no row hash to compare")

	row := f.storedRow(t)
	require.Equal(t, f.newPath, row.FilePath)
	require.False(t, row.Missing)
	require.Equal(t, f.contentHash, row.FileHash, "the hash is recomputed from the new file")
	require.Equal(t, 1, row.TrackNumber, "track kept")
	require.Equal(t, 600, row.Duration, "row kept")

	byNew, err := f.s.GetBookFileByPath(f.newPath)
	require.NoError(t, err)
	require.NotNil(t, byNew)
	require.Equal(t, f.row, byNew.ID)
	byOld, err := f.s.GetBookFileByPath(f.oldPath)
	require.NoError(t, err)
	require.Nil(t, byOld, "the old path index entry must be gone")

	changes, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	c := changes[0]
	require.Equal(t, rbfChangeType, c.ChangeType)
	require.Equal(t, f.book, c.BookID)
	require.Equal(t, f.row, c.FieldName)
	require.Equal(t, f.newPath, c.NewValue)
	var old database.BookFile
	require.NoError(t, json.Unmarshal([]byte(c.OldValue), &old))
	require.Equal(t, f.oldPath, old.FilePath, "the journal holds the row as it was")
	require.True(t, old.Missing)

	require.Len(t, rep.After, 1)
	require.Equal(t, 0, rep.After[0].Missing)
	require.Equal(t, 2, rep.After[0].FileCount)
}

func TestRepointBookFileRows_JournalFailureWritesNothing(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	rep := runRBF(t, &journalFailStore{PebbleStore: f.s}, true, nil, f.item())
	require.Equal(t, 0, rep.Repointed)
	require.Equal(t, 1, rep.Failed)
	require.Equal(t, rbfStatusFailed, rep.Repoints[0].Status)
	require.Contains(t, rep.Repoints[0].Error, "journal")
	row := f.storedRow(t)
	require.Equal(t, f.oldPath, row.FilePath, "journal-first: no journal, no write")
	require.True(t, row.Missing)
}

func TestRepointBookFileRows_Gates(t *testing.T) {
	cases := []struct {
		name     string
		rowSize  int64
		rowHash  string
		setup    func(t *testing.T, f *rbfFixture, it *rbfRepoint)
		reason   string
		gate     func(g rbfGates) string
		planned  bool
		warnings string
	}{
		{name: "row not found", rowSize: -1, rowHash: "-",
			setup:  func(_ *testing.T, _ *rbfFixture, it *rbfRepoint) { it.RowID = "01NOSUCHROW0000000000000XX" },
			reason: "row not found", gate: func(g rbfGates) string { return g.RowFound }},
		{name: "row belongs to another book", rowSize: -1, rowHash: "-",
			setup:  func(_ *testing.T, _ *rbfFixture, it *rbfRepoint) { it.BookID = rbfOtherBook },
			reason: "row not found under book", gate: func(g rbfGates) string { return g.RowFound }},
		{name: "old file present", rowSize: -1, rowHash: "-",
			setup:  func(t *testing.T, f *rbfFixture, _ *rbfRepoint) { writeRBFFile(t, f.oldPath, f.content) },
			reason: "present on disk", gate: func(g rbfGates) string { return g.OldPathMissing }},
		{name: "new path absent", rowSize: -1, rowHash: "-",
			setup:  func(_ *testing.T, f *rbfFixture, it *rbfRepoint) { it.NewPath = filepath.Join(f.root, "nope.mp3") },
			reason: "new_path cannot be read", gate: func(g rbfGates) string { return g.NewPathRegular }},
		{name: "new path is a directory", rowSize: -1, rowHash: "-",
			setup:  func(_ *testing.T, f *rbfFixture, it *rbfRepoint) { it.NewPath = filepath.Dir(f.newPath) },
			reason: "not a regular file", gate: func(g rbfGates) string { return g.NewPathRegular }},
		{name: "new path under books/itunes", rowSize: -1, rowHash: "-",
			setup: func(t *testing.T, f *rbfFixture, it *rbfRepoint) {
				it.NewPath = filepath.Join(f.root, "books", "itunes", "Author", "01.mp3")
				writeRBFFile(t, it.NewPath, f.content)
			},
			reason: "itunes-path", gate: func(g rbfGates) string { return g.NotITunes }},
		{name: "size differs", rowSize: 12345, rowHash: "-",
			reason: "size differs", gate: func(g rbfGates) string { return g.SizeMatch }},
		{name: "no size and no hash", rowSize: 0, rowHash: "",
			reason: "no recorded size and no file_hash", gate: func(g rbfGates) string { return g.SizeMatch }},
		{name: "no size and a differing hash", rowSize: 0, rowHash: "deadbeef",
			reason: "no recorded size and new_path's hash differs", gate: func(g rbfGates) string { return g.HashMatch }},
		{name: "no size but the hash matches", rowSize: 0, rowHash: "-", planned: true},
		{name: "hash differs at equal size, not allowed", rowSize: -1, rowHash: "deadbeef",
			reason: "set allow_hash_mismatch", gate: func(g rbfGates) string { return g.HashMatch }},
		{name: "hash differs at equal size, allowed", rowSize: -1, rowHash: "deadbeef",
			setup:   func(_ *testing.T, _ *rbfFixture, it *rbfRepoint) { it.AllowHashMismatch = true },
			planned: true, warnings: "allowed by allow_hash_mismatch"},
		{name: "a stub book's file_path is new_path", rowSize: -1, rowHash: "-",
			setup: func(t *testing.T, f *rbfFixture, _ *rbfRepoint) {
				_, err := f.s.CreateBook(&database.Book{ID: rbfOtherBook, Title: "stub", FilePath: f.newPath})
				require.NoError(t, err)
			},
			planned: true, warnings: "file_path = new_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRBFFixture(t, tc.rowSize, tc.rowHash)
			it := f.item()
			if tc.setup != nil {
				tc.setup(t, &f, &it)
			}
			rep := runRBF(t, f.s, false, nil, it)
			r := rep.Repoints[0]
			if tc.planned {
				require.Equal(t, rbfStatusPlanned, r.Status, r.Reason)
			} else {
				require.Equal(t, rbfStatusRefused, r.Status)
				require.Contains(t, r.Reason, tc.reason)
				require.Equal(t, rbfGateFail, tc.gate(r.Gates))
			}
			if tc.warnings != "" {
				require.Contains(t, strings.Join(r.Warnings, "|"), tc.warnings)
			}
		})
	}
}

func TestRepointBookFileRows_StubBookReported(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	_, err := f.s.CreateBook(&database.Book{ID: rbfOtherBook, Title: "stub", FilePath: f.newPath})
	require.NoError(t, err)
	rep := runRBF(t, f.s, false, nil, f.item())
	require.Equal(t, []string{rbfOtherBook}, rep.Repoints[0].StubBookIDs)
}

func TestRepointBookFileRows_PathOwnedByAnotherLiveRowRefused(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	_, err := f.s.CreateBook(&database.Book{ID: rbfOtherBook, Title: "owner", FilePath: filepath.Dir(f.newPath)})
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "01RBFOWNER00000000000000AE", BookID: rbfOtherBook,
		FilePath: f.newPath, FileSize: int64(len(f.content)), Missing: true}))

	rep := runRBF(t, f.s, true, nil, f.item())
	r := rep.Repoints[0]
	require.Equal(t, rbfStatusRefused, r.Status)
	require.Contains(t, r.Reason, "owned by row")
	require.Equal(t, rbfGateFail, r.Gates.NoOtherOwner)
	require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
}

func TestRepointBookFileRows_DuplicatesInOneRunRefused(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	rep := runRBF(t, f.s, false, nil, f.item(), f.item())
	require.Equal(t, 1, rep.Planned)
	require.Equal(t, 1, rep.Refused)
	require.Contains(t, rep.Repoints[1].Reason, "more than once")

	// Two rows naming one new_path: only the first is planned.
	f2 := newRBFFixture(t, -1, "-")
	other := rbfRepoint{RowID: f2.sibling, BookID: f2.book, NewPath: f2.newPath}
	rep2 := runRBF(t, f2.s, false, nil, f2.item(), other)
	require.Equal(t, rbfStatusPlanned, rep2.Repoints[0].Status)
	require.Contains(t, rep2.Repoints[1].Reason, "already the target of row")
}

// setRBFHashHook runs after on the first hash call only (the plan's), so a
// test can change the world between the plan and the apply.
func setRBFHashHook(t *testing.T, after func()) {
	t.Helper()
	orig := rbfHash
	calls := 0
	rbfHash = func(p string) (string, error) {
		h, err := orig(p)
		calls++
		if calls == 1 {
			after()
		}
		return h, err
	}
	t.Cleanup(func() { rbfHash = orig })
}

func TestRepointBookFileRows_ApplyTimeRecheck(t *testing.T) {
	t.Run("old file reappears", func(t *testing.T) {
		f := newRBFFixture(t, -1, "-")
		setRBFHashHook(t, func() { writeRBFFile(t, f.oldPath, []byte("back")) })
		rep := runRBF(t, f.s, true, nil, f.item())
		require.Equal(t, rbfStatusFailed, rep.Repoints[0].Status)
		require.Contains(t, rep.Repoints[0].Error, "apply-time re-check")
		require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
	})
	t.Run("new file changes size", func(t *testing.T) {
		f := newRBFFixture(t, -1, "-")
		setRBFHashHook(t, func() { writeRBFFile(t, f.newPath, append(append([]byte{}, f.content...), 'x')) })
		rep := runRBF(t, f.s, true, nil, f.item())
		require.Equal(t, rbfStatusFailed, rep.Repoints[0].Status)
		require.Contains(t, rep.Repoints[0].Error, "size differs")
		require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
	})
	t.Run("another row takes new_path", func(t *testing.T) {
		f := newRBFFixture(t, -1, "-")
		setRBFHashHook(t, func() {
			_, err := f.s.CreateBook(&database.Book{ID: rbfOtherBook, Title: "late", FilePath: filepath.Dir(f.newPath)})
			require.NoError(t, err)
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "01RBFLATE000000000000000AF",
				BookID: rbfOtherBook, FilePath: f.newPath}))
		})
		rep := runRBF(t, f.s, true, nil, f.item())
		require.Equal(t, rbfStatusFailed, rep.Repoints[0].Status)
		require.Contains(t, rep.Repoints[0].Error, "owned by row")
		require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
	})
}

func TestRepointBookFileRows_EmptyAndStrictParams(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	_, err := repointBookFileRows(context.Background(), rbfEnv{store: f.s}, rbfParams{}, &fakeReporter{})
	require.Error(t, err)

	var p rbfParams
	require.Error(t, decodeStrictParams(json.RawMessage(`{"repoints":[{"row_id":"a","book_id":"b","new_path":"/x","bogus":1}]}`), &p))
	require.NoError(t, decodeStrictParams(json.RawMessage(`{"repoints":[{"row_id":"a","book_id":"b","new_path":"/x","allow_hash_mismatch":true}],"dry_run":false}`), &p))
	require.True(t, p.Repoints[0].AllowHashMismatch)
	dry, err := p.dryRun()
	require.NoError(t, err)
	require.False(t, dry)
}

// The prod shape: a stub book whose file_path is new_path owns its own MISSING
// row on the same old path. The stub and its row are reported and left alone.
func TestRepointBookFileRows_StubWithRowOnOldPathLeftAlone(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	_, err := f.s.CreateBook(&database.Book{ID: rbfOtherBook, Title: "stub", FilePath: f.newPath})
	require.NoError(t, err)
	stubRow := "01RBFSTUBROW000000000000AG"
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: stubRow, BookID: rbfOtherBook,
		FilePath: f.oldPath, FileSize: int64(len(f.content)), Missing: true}))
	idxBefore, err := f.s.GetBookFileByPath(f.oldPath)
	require.NoError(t, err)

	rep := runRBF(t, f.s, true, nil, f.item())
	r := rep.Repoints[0]
	require.Equal(t, rbfStatusRepointed, r.Status, r.Error)
	require.Equal(t, []string{rbfOtherBook}, r.StubBookIDs)
	require.Equal(t, []string{stubRow}, r.OldPathOtherRows)
	require.Equal(t, f.newPath, f.storedRow(t).FilePath)

	stub, err := f.s.GetBookFileByID(rbfOtherBook, stubRow)
	require.NoError(t, err)
	require.NotNil(t, stub)
	require.Equal(t, f.oldPath, stub.FilePath)
	require.True(t, stub.Missing)
	b, err := f.s.GetBookByID(rbfOtherBook)
	require.NoError(t, err)
	require.Equal(t, f.newPath, b.FilePath, "Book.FilePath is never changed")

	// Observation, logged not asserted: which row the single-owner old-path
	// index names before and after (the store owns that index's semantics).
	idxAfter, err := f.s.GetBookFileByPath(f.oldPath)
	require.NoError(t, err)
	name := func(bf *database.BookFile) string {
		if bf == nil {
			return "<none>"
		}
		return bf.ID
	}
	t.Logf("old-path index: before=%s after=%s", name(idxBefore), name(idxAfter))
}

func TestRepointBookFileRows_LiveZeroSizeHashMatchSetsSize(t *testing.T) {
	f := newRBFFixture(t, 0, "-")
	rep := runRBF(t, f.s, true, nil, f.item())
	require.Equal(t, rbfStatusRepointed, rep.Repoints[0].Status, rep.Repoints[0].Error)
	row := f.storedRow(t)
	require.Equal(t, int64(len(f.content)), row.FileSize, "an unknown size is filled from disk")
	require.Equal(t, f.contentHash, row.FileHash)
	b, err := f.s.GetBookByID(f.book)
	require.NoError(t, err)
	require.NotNil(t, b.FileSize)
	require.Equal(t, int64(len(f.content))+7, *b.FileSize, "the book's size follows the row")
}

func TestRepointBookFileRows_LiveAllowedHashMismatchStoresNewHash(t *testing.T) {
	f := newRBFFixture(t, -1, "deadbeef")
	it := f.item()
	it.AllowHashMismatch = true
	rep := runRBF(t, f.s, true, nil, it)
	r := rep.Repoints[0]
	require.Equal(t, rbfStatusRepointed, r.Status, r.Error)
	require.True(t, r.HashMismatchAllowed)
	require.Equal(t, "deadbeef", r.RowHash)
	require.Equal(t, f.contentHash, r.DiskHash)
	require.Equal(t, f.contentHash, f.storedRow(t).FileHash)
}
