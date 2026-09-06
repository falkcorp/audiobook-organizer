// file: internal/plugins/maintenance/mark_missing_files_test.go
// version: 1.1.1
// guid: 8b2e4f61-9c73-45a0-8d1e-2f6a7c904b3d
// last-edited: 2026-09-05

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// markFakeStore records every UpdateBookFile so a test can assert BOTH what was
// written and that nothing else was. It reuses the package's writeFile helper.
type markFakeStore struct {
	mu    sync.Mutex // UpdateBookFile is called from RunItems' worker pool
	cores []database.BookFileCore
	// books overrides the book-core set GetAllBooksCore returns. When nil, the
	// store DERIVES one all-primary book per distinct BookID in cores — so the
	// BooksBrokenOnDisk tests need not each spell out a book list. A test that
	// needs a NON-primary book (to prove BooksBrokenOnDisk excludes it) sets books.
	books   []database.BookCore
	full    map[string][]database.BookFile
	updates []database.BookFile
}

func (f *markFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.cores, nil
}

func (f *markFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	books := f.books
	if books == nil {
		seen := map[string]struct{}{}
		for _, c := range f.cores {
			if _, ok := seen[c.BookID]; ok {
				continue
			}
			seen[c.BookID] = struct{}{}
			// nil IsPrimaryVersion == primary, matching the stats derivation.
			books = append(books, database.BookCore{ID: c.BookID})
		}
	}
	if offset >= len(books) {
		return nil, nil
	}
	end := offset + limit
	if end > len(books) {
		end = len(books)
	}
	return books[offset:end], nil
}
func (f *markFakeStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return f.full[bookID], nil
}
func (f *markFakeStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, *file)
	return nil
}

// seedMark builds a store with one row at path, its stored Missing flag, and a
// full record carrying a fingerprint so a test can prove the write is a
// full-record replacement that preserves the other fields.
func seedMark(path string, storedMissing bool) *markFakeStore {
	return &markFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: path, Missing: storedMissing}},
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: path, Missing: storedMissing,
			AcoustIDFingerprint: []byte("fp-keep-me"),
		}}},
	}
}

// The core reconcile: a row whose bytes are gone but whose flag is false must be
// set Missing=true, and the full record (fingerprint) must survive — UpdateBookFile
// is a full-record replacement, so a partial write would destroy it.
func TestMarkMissing_MarksGoneRowsAndPreservesRecord(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.mp3") // never written to disk
	store := seedMark(gone, false)

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.WouldMarkMissing)
	require.Equal(t, 1, plan.MarkedMissing)
	require.Equal(t, 1, plan.BooksBrokenOnDisk)
	require.Len(t, store.updates, 1)
	require.True(t, store.updates[0].Missing, "flag must be set true")
	require.Equal(t, []byte("fp-keep-me"), store.updates[0].AcoustIDFingerprint,
		"the full record must survive the flag write")
}

// The other direction: a row flagged Missing whose bytes are present again (e.g.
// after a repoint restored the path) must be cleared — a one-directional mark would
// let the counter drift permanently high.
func TestMarkMissing_ClearsStaleFlag(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mp3")
	writeFile(t, present, 10)
	store := seedMark(present, true) // stored Missing=true, but the file is there

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.WouldClearStale)
	require.Equal(t, 1, plan.ClearedStale)
	require.Equal(t, 0, plan.BooksBrokenOnDisk, "a present file is not broken")
	require.Len(t, store.updates, 1)
	require.False(t, store.updates[0].Missing, "flag must be cleared to false")
}

// Dry run is the default and must write NOTHING while still reporting the flips.
func TestMarkMissing_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	store := seedMark(filepath.Join(dir, "gone.mp3"), false)

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{}, &fakeReporter{}) // Apply defaults to false
	require.NoError(t, err)

	require.Equal(t, 1, plan.WouldMarkMissing, "dry run still reports the plan")
	require.Equal(t, 0, plan.MarkedMissing)
	require.Empty(t, store.updates, "DRY RUN MUST NOT WRITE")
}

// Rows whose flag already matches disk must be left alone — no write, counted as
// unchanged. Present+false and gone+true are both already correct.
func TestMarkMissing_UnchangedRowsNotWritten(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mp3")
	writeFile(t, present, 5)
	gone := filepath.Join(dir, "gone.mp3")

	store := &markFakeStore{
		cores: []database.BookFileCore{
			{ID: "ok", BookID: "b1", FilePath: present, Missing: false}, // present, flag false → correct
			{ID: "broken", BookID: "b2", FilePath: gone, Missing: true}, // gone, flag true → correct
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "ok", BookID: "b1", FilePath: present}},
			"b2": {{ID: "broken", BookID: "b2", FilePath: gone, Missing: true}},
		},
	}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 2, plan.Unchanged)
	require.Equal(t, 0, plan.WouldMarkMissing)
	require.Equal(t, 0, plan.WouldClearStale)
	require.Equal(t, 1, plan.BooksBrokenOnDisk, "b2's file is gone → it is broken even though its flag was already set")
	require.Empty(t, store.updates, "already-correct rows must not be rewritten")
}

// A stat error that is NOT not-exist (here ENOTDIR: a path component is a regular
// file) must leave the flag untouched and land in the unreadable bucket — "I could
// not tell" is not "it is gone".
func TestMarkMissing_UnreadableLeavesFlagUntouched(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	writeFile(t, notADir, 3)
	// Statting "afile/child.mp3" fails with ENOTDIR, which is not IsNotExist.
	store := seedMark(filepath.Join(notADir, "child.mp3"), false)

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Unreadable)
	require.Equal(t, 0, plan.WouldMarkMissing)
	require.Empty(t, store.updates, "an unreadable row must not have its flag changed")
}

// The row-level idempotency guard: if a concurrent run already reconciled the row
// (the full record fetched at write time already carries the target value), the
// write is skipped rather than counted twice. This is the row-level half of the
// write-time interlock (the full record already agrees); the disk-re-stat half —
// the fresh os.Stat before each write — is not directly unit-tested here because
// the fake cannot make a path flip existence between plan and write.
func TestMarkMissing_SkipsRowAlreadyReconciled(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.mp3")

	store := &markFakeStore{
		// Core says Missing=false (so the plan wants to set it true)…
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, Missing: false}},
		// …but by write time the row already reads Missing=true (a concurrent run won).
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: gone, Missing: true,
		}}},
	}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.WouldMarkMissing, "the plan still counts the row it intended to flip")
	require.Equal(t, 0, plan.MarkedMissing, "but the write is skipped because the row was already reconciled")
	require.Empty(t, store.updates, "no redundant write")
}

// BooksBrokenOnDisk counts DISTINCT books with ≥1 gone file — the number the
// dashboard counter will show — regardless of how many of a book's files are gone.
func TestMarkMissing_BooksBrokenCountsDistinctBooks(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mp3")
	writeFile(t, present, 1)

	store := &markFakeStore{
		cores: []database.BookFileCore{
			{ID: "a1", BookID: "b1", FilePath: filepath.Join(dir, "g1.mp3"), Missing: false},  // gone
			{ID: "a2", BookID: "b1", FilePath: filepath.Join(dir, "g2.mp3"), Missing: false},  // gone (same book)
			{ID: "b1f", BookID: "b2", FilePath: filepath.Join(dir, "g3.mp3"), Missing: false}, // gone (other book)
			{ID: "ok", BookID: "b3", FilePath: present, Missing: false},                       // present
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "a1", BookID: "b1"}, {ID: "a2", BookID: "b1"}},
			"b2": {{ID: "b1f", BookID: "b2"}},
			"b3": {{ID: "ok", BookID: "b3"}},
		},
	}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 2, plan.BooksBrokenOnDisk, "b1 (two gone files, counted once) and b2; b3 is present")
	require.Equal(t, 3, plan.WouldMarkMissing, "three gone rows across the two broken books")
}

// BooksBrokenOnDisk must count only PRIMARY-version books — the same population the
// dashboard's BrokenFiles tile counts. A gone file on a NON-primary (redundant
// version) book still gets its flag flipped (the flag is per-row truth), but it must
// NOT inflate the predicted counter, or the op would report a number higher than the
// tile it claims to predict.
func TestMarkMissing_BooksBrokenExcludesNonPrimary(t *testing.T) {
	dir := t.TempDir()
	nonPrimary := false
	store := &markFakeStore{
		cores: []database.BookFileCore{
			{ID: "p", BookID: "b1", FilePath: filepath.Join(dir, "g1.mp3"), Missing: false}, // gone, primary book
			{ID: "n", BookID: "b2", FilePath: filepath.Join(dir, "g2.mp3"), Missing: false}, // gone, NON-primary book
		},
		books: []database.BookCore{
			{ID: "b1"}, // nil IsPrimaryVersion → primary
			{ID: "b2", IsPrimaryVersion: &nonPrimary}, // explicitly non-primary
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "p", BookID: "b1"}},
			"b2": {{ID: "n", BookID: "b2"}},
		},
	}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.BooksBrokenOnDisk, "only b1 (primary) counts; b2 is a non-primary version")
	require.Equal(t, 2, plan.WouldMarkMissing, "both rows' flags still flip — the flag is per-row truth")
	require.Len(t, store.updates, 2, "the non-primary row is still written; only the counter excludes it")
}

// The cap bounds a run and the prefix taken must be STABLE across runs (sorted by
// file ID) so repeated runs converge instead of each flipping a different slice.
func TestMarkMissing_CapIsBoundedAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	var cores []database.BookFileCore
	full := map[string][]database.BookFile{"b1": {}}
	for _, id := range []string{"f3", "f1", "f2"} { // deliberately unsorted
		cores = append(cores, database.BookFileCore{
			ID: id, BookID: "b1", FilePath: filepath.Join(dir, id+".mp3"), Missing: false}) // all gone
		full["b1"] = append(full["b1"], database.BookFile{ID: id, BookID: "b1"})
	}
	store := &markFakeStore{cores: cores, full: full}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: true, Max: 2}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 3, plan.WouldMarkMissing, "the count reflects the FULL set, not the capped slice")
	require.Equal(t, 2, plan.CappedAt)
	require.Len(t, store.updates, 2)
	got := []string{store.updates[0].ID, store.updates[1].ID}
	require.ElementsMatch(t, []string{"f1", "f2"}, got, "cap must take a stable prefix by ID, never f3")
}

// The report records every row that is NOT trivially unchanged (each flip and each
// unreadable), and NOT the unchanged majority — a 726k-row library must not produce
// a 726k-line TSV. The plan counters account for the unchanged rows.
func TestMarkMissing_ReportRecordsFlipsAndUnreadableOnly(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mp3")
	writeFile(t, present, 4)
	notADir := filepath.Join(dir, "afile")
	writeFile(t, notADir, 4)

	store := &markFakeStore{
		cores: []database.BookFileCore{
			{ID: "gone", BookID: "b1", FilePath: filepath.Join(dir, "gone.mp3"), Missing: false},   // → mark-missing
			{ID: "stale", BookID: "b2", FilePath: present, Missing: true},                          // → clear-stale
			{ID: "ok", BookID: "b3", FilePath: present, Missing: false},                            // unchanged (not recorded)
			{ID: "weird", BookID: "b4", FilePath: filepath.Join(notADir, "x.mp3"), Missing: false}, // unreadable
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "gone", BookID: "b1"}},
			"b2": {{ID: "stale", BookID: "b2", FilePath: present, Missing: true}},
			"b3": {{ID: "ok", BookID: "b3"}},
			"b4": {{ID: "weird", BookID: "b4"}},
		},
	}

	plan, err := planMarkMissingFiles(context.Background(), store,
		markMissingParams{Apply: false}, &fakeReporter{}) // dry run — report still written
	require.NoError(t, err)

	require.Equal(t, 1, plan.WouldMarkMissing)
	require.Equal(t, 1, plan.WouldClearStale)
	require.Equal(t, 1, plan.Unchanged)
	require.Equal(t, 1, plan.Unreadable)
	require.Len(t, plan.all, 3, "report covers the two flips and the unreadable row, not the unchanged one")

	seen := map[string]int{}
	for _, d := range plan.all {
		require.NotEmpty(t, d.Bucket)
		seen[d.Bucket]++
	}
	require.Equal(t, 1, seen["mark-missing"])
	require.Equal(t, 1, seen["clear-stale"])
	require.Equal(t, 1, seen["unreadable"])
	require.Zero(t, seen["unchanged"], "unchanged rows must not be recorded")
}

// The TSV must round-trip: one header + one line per decision, tabs intact.
func TestMarkMissing_WriteReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.tsv")
	decisions := []markDecision{
		{Bucket: "mark-missing", FileID: "f1", BookID: "b1", Path: "/a/gone.mp3", Reason: "bytes gone → set Missing=true"},
		{Bucket: "unreadable", FileID: "f2", BookID: "b1", Path: "/a/weird\tname.mp3", Reason: "stat failed"},
	}
	require.NoError(t, writeMarkMissingReport(path, decisions))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 3, "header + one line per decision")
	require.Equal(t, "bucket\tfile_id\tbook_id\tpath\treason", lines[0])
	for i, l := range lines[1:] {
		require.Len(t, strings.Split(l, "\t"), 5, "row %d has the wrong column count: %q", i, l)
	}
}
