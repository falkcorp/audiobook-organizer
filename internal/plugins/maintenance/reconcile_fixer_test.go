// file: internal/plugins/maintenance/reconcile_fixer_test.go
// version: 2.0.0
// guid: 8f0c7a5e-2b3d-4c61-9e47-a1d5b6c3f902
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The fixer reads the server's store through rmReader: database.Store must
// satisfy it, or production refuses every plan.
var _ rmReader = database.Store(nil)

// rmWarmingStore is the store while memdb is warming up: BookFilesAtPath
// cannot give a complete answer.
type rmWarmingStore struct {
	*database.PebbleStore
	warming atomic.Bool
}

func (s *rmWarmingStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	if s.warming.Load() {
		return nil, fmt.Errorf("%w: memdb is not serving", database.ErrBookFilesAtPathUnavailable)
	}
	return s.PebbleStore.BookFilesAtPath(path)
}

// rmFixture is a library with missing-file books and untracked files on
// disk, and the scan that matched them. Titles and paths are synthetic.
type rmFixture struct {
	s    *database.PebbleStore
	st   *rmWarmingStore
	dir  string
	deps *fakeDeps
	fx   *reconcileFixer
	scan reconcile.ReconcilePreviewResult
	// applies numbers the apply ops, so each has its own journal.
	applies int
}

func newRMFixture(t *testing.T) *rmFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &rmFixture{s: newFragStore(t), dir: dir}
	f.st = &rmWarmingStore{PebbleStore: f.s}
	f.deps = &fakeDeps{store: f.st, root: dir}
	f.deps.reconcileScan = func() (ReconcileScan, error) {
		at := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
		preview := f.scan
		return ReconcileScan{OpID: "scan-1", CompletedAt: &at, Preview: &preview}, nil
	}
	f.fx = newReconcileFixer(&Plugin{deps: f.deps})
	return f
}

// path is dir/rel.
func (f *rmFixture) path(rel string) string { return filepath.Join(f.dir, rel) }

// file writes an untracked file at rel and returns its path.
func (f *rmFixture) file(t *testing.T, rel string) string {
	t.Helper()
	p := f.path(rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("audio "+rel), 0o644))
	return p
}

// missingBook creates a book whose file (and one Missing book_file row,
// with a hash and size) is at rel, which is NOT on disk.
func (f *rmFixture) missingBook(t *testing.T, title, rel string, mut func(*database.Book)) string {
	t.Helper()
	b := &database.Book{Title: title, FilePath: f.path(rel)}
	if mut != nil {
		mut(b)
	}
	b, err := f.s.CreateBook(b)
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path(rel), Missing: true,
		FileHash: "hash-" + title, FileSize: 11}))
	return b.ID
}

// match adds a scan match of book to the untracked file newRel.
func (f *rmFixture) match(bookID, title, oldRel, newRel, kind, conf string) {
	f.scan.Matches = append(f.scan.Matches, reconcile.ReconcileMatch{BookID: bookID, BookTitle: title,
		OldPath: f.path(oldRel), NewPath: f.path(newRel), MatchType: kind, Confidence: conf, Score: 1})
}

// unmatched adds a book the scan found no file for.
func (f *rmFixture) unmatched(bookID, title, oldRel string) {
	f.scan.UnmatchedBooks = append(f.scan.UnmatchedBooks, reconcile.ReconcileBrokenRecord{BookID: bookID, Title: title, FilePath: f.path(oldRel)})
}

func (f *rmFixture) plan(t *testing.T) *repairs.PlanResult {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), f.fx, nil, repairs.PlanDeps{Guard: f.s}, &fakeReporter{})
	require.NoError(t, err)
	return res
}

// writer is an apply op's journaled Writer; opID names the op.
func (f *rmFixture) writer() (*repairs.Writer, string) {
	f.applies++
	opID := fmt.Sprintf("repairs-apply-%d", f.applies)
	return repairs.NewWriter(f.s, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, opID), opID
}

// apply runs a real (not dry-run) apply of rowIDs and returns its result
// and op id.
func (f *rmFixture) apply(t *testing.T, res *repairs.PlanResult, rowIDs ...string) (*repairs.ApplyResult, string) {
	t.Helper()
	w, opID := f.writer()
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	return out, opID
}

// state is a book's path and its book_file rows' locations, by row id.
type rmState struct {
	Path  string
	Files map[string]database.BookFileLocation
}

func (f *rmFixture) state(t *testing.T, bookID string) rmState {
	t.Helper()
	b, err := f.s.GetBookByID(bookID)
	require.NoError(t, err)
	files, err := f.s.GetBookFiles(bookID)
	require.NoError(t, err)
	st := rmState{Path: b.FilePath, Files: map[string]database.BookFileLocation{}}
	for i := range files {
		st.Files[files[i].ID] = database.BookFileLocationOf(&files[i])
	}
	return st
}

func (f *rmFixture) countRows(t *testing.T) int {
	t.Helper()
	all, err := f.s.GetAllBookFilesCore()
	require.NoError(t, err)
	return len(all)
}

func rmRow(t *testing.T, res *repairs.PlanResult, bookID string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		if r.RowID == rmRowID(bookID) {
			return r
		}
	}
	t.Fatalf("no row for book %s", bookID)
	return repairs.Row{}
}

func rmOutcomes(out *repairs.ApplyResult) map[string]string {
	m := map[string]string{}
	for _, r := range out.Rows {
		m[r.RowID] = r.Outcome
	}
	return m
}

// TestReconcileFixer_RowsComeFromTheLatestSavedScan: every scan match and
// every unmatched book is one row; a match row proposes the path only, its
// match type and confidence are its class and evidence.
func TestReconcileFixer_RowsComeFromTheLatestSavedScan(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/Book One.m4b", nil)
	two := f.missingBook(t, "Book Two", "old/Book Two.m4b", nil)
	lost := f.missingBook(t, "Book Three", "old/Book Three.m4b", nil)
	f.file(t, "new/Book One.m4b")
	f.file(t, "new/b2.m4b")
	f.match(one, "Book One", "old/Book One.m4b", "new/Book One.m4b", "hash", "high")
	f.match(two, "Book Two", "old/Book Two.m4b", "new/b2.m4b", "filename", "low")
	f.unmatched(lost, "Book Three", "old/Book Three.m4b")

	res := f.plan(t)
	require.Equal(t, 3, res.Total, "matches + unmatched books")
	assert.Equal(t, 2, res.Applicable)
	assert.Equal(t, map[string]int{"hash": 1, "filename": 1, rmClassNoCandidate: 1}, res.ByClass)

	r1 := rmRow(t, res, one)
	assert.True(t, r1.Applicable(), r1.SkipReason)
	assert.Equal(t, "Book One", r1.Title)
	assert.Equal(t, []string{one}, r1.BookIDs)
	assert.Equal(t, map[string]string{"path": f.path("old/Book One.m4b") + " (missing)"}, r1.Current)
	assert.Equal(t, map[string]string{"path": f.path("new/Book One.m4b")}, r1.Proposed)
	assert.Equal(t, "hash", r1.Class)
	assert.Equal(t, repairs.RiskLow, r1.Risk)
	assert.Contains(t, r1.Evidence[0], "scan-1")
	assert.Contains(t, r1.Evidence[1], "matched by hash, confidence high")

	r2 := rmRow(t, res, two)
	assert.True(t, r2.Applicable(), r2.SkipReason)
	assert.Equal(t, map[string]string{"path": f.path("new/b2.m4b")}, r2.Proposed)
	assert.Contains(t, r2.Evidence[1], "confidence low")
	assert.Equal(t, repairs.RiskReview, r2.Risk, "a filename match is for review")
}

// TestReconcileFixer_NoCandidateFileIsASkippedRowWithAReason.
func TestReconcileFixer_NoCandidateFileIsASkippedRowWithAReason(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	lost := f.missingBook(t, "Book Three", "old/Book Three.m4b", nil)
	f.unmatched(lost, "Book Three", "old/Book Three.m4b")

	res := f.plan(t)
	r := rmRow(t, res, lost)
	assert.False(t, r.Applicable())
	assert.Equal(t, rmSkipNoCandidate, r.Skipped)
	assert.Equal(t, "no candidate file", r.SkipReason)
	assert.Equal(t, rmClassNoCandidate, r.Class)
	assert.Equal(t, map[string]int{rmSkipNoCandidate: 1}, res.SkippedByKind, "the skipped count is a filter")

	before := f.state(t, lost)
	out, _ := f.apply(t, res, r.RowID)
	assert.Equal(t, repairs.OutcomeNotApplicable, rmOutcomes(out)[r.RowID])
	assert.Equal(t, before, f.state(t, lost), "nothing written")
}

// TestReconcileFixer_ApplyRepointsExactlyThePickedBooksAndUndoRestoresThem:
// two of four applicable rows are picked. Exactly those two books, and each
// one's book_file row, now point at the matched file; the other two are
// untouched; no book_file row is created or deleted (a second Missing row on
// a picked book stays as it was). Reverting the apply op puts both books and
// both rows back.
func TestReconcileFixer_ApplyRepointsExactlyThePickedBooksAndUndoRestoresThem(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	var ids []string
	for i, n := range []string{"A", "B", "C", "D"} {
		id := f.missingBook(t, "Book "+n, "old/"+n+".m4b", nil)
		f.file(t, "new/"+n+".m4b")
		kind := "hash"
		if i == 2 {
			kind = "filename"
		}
		f.match(id, "Book "+n, "old/"+n+".m4b", "new/"+n+".m4b", kind, "high")
		ids = append(ids, id)
	}
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: ids[0], FilePath: f.path("old/A part 2.m4b"), Missing: true}))
	before := map[string]rmState{}
	for _, id := range ids {
		before[id] = f.state(t, id)
	}
	rowsBefore := f.countRows(t)
	res := f.plan(t)
	require.Equal(t, 4, res.Applicable)

	out, opID := f.apply(t, res, rmRowID(ids[0]), rmRowID(ids[2]))
	require.Equal(t, 2, out.Applied, "%+v", out.Rows)
	assert.Equal(t, rowsBefore, f.countRows(t), "no book_file row created or deleted")

	for _, k := range []struct {
		id, n    string
		keepHash bool
	}{{ids[0], "A", true}, {ids[2], "C", false}} {
		st := f.state(t, k.id)
		assert.Equal(t, f.path("new/"+k.n+".m4b"), st.Path, "book %s repointed", k.n)
		require.Len(t, st.Files, len(before[k.id].Files))
		moved := 0
		for fid, loc := range st.Files {
			was := before[k.id].Files[fid]
			if was.Path != f.path("old/"+k.n+".m4b") {
				assert.Equal(t, was, loc, "another row of the book is untouched")
				continue
			}
			moved++
			assert.Equal(t, f.path("new/"+k.n+".m4b"), loc.Path)
			assert.False(t, loc.Missing)
			assert.Equal(t, int64(len("audio new/"+k.n+".m4b")), loc.Size)
			if k.keepHash {
				assert.Equal(t, was.Hash, loc.Hash, "a hash match keeps the row's hash")
			} else {
				assert.Empty(t, loc.Hash, "a filename match proves nothing about content")
			}
		}
		assert.Equal(t, 1, moved)
	}
	for _, id := range []string{ids[1], ids[3]} {
		assert.Equal(t, before[id], f.state(t, id), "an unpicked book is untouched")
	}

	_, err := audiobooks.NewRevertService(f.s).RevertOperation(opID)
	require.NoError(t, err)
	for _, id := range ids {
		assert.Equal(t, before[id], f.state(t, id), "undo restores book %s and its rows", id)
	}
	assert.Equal(t, rowsBefore, f.countRows(t))
}

// TestReconcileFixer_DriftBetweenReplanAndWriteIsRefused: Apply re-reads
// right before writing. A row fresh from Replan whose book moved, whose
// matched file another book took (its path or a book_file row), whose book
// was retired, or whose matched file changed is refused as
// changed_since_plan with nothing written.
func TestReconcileFixer_DriftBetweenReplanAndWriteIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		drift func(t *testing.T, f *rmFixture, bookID string)
	}{
		{"book path changed", func(t *testing.T, f *rmFixture, id string) {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.FilePath = f.path("elsewhere/One.m4b"); return nil })
			require.NoError(t, err)
		}},
		{"file now another book's path", func(t *testing.T, f *rmFixture, _ string) {
			_, err := f.s.CreateBook(&database.Book{Title: "Book Other", FilePath: f.path("new/One.m4b")})
			require.NoError(t, err)
		}},
		{"file now another book's file row", func(t *testing.T, f *rmFixture, _ string) {
			b, err := f.s.CreateBook(&database.Book{Title: "Book Other", FilePath: f.path("other")})
			require.NoError(t, err)
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path("new/One.m4b")}))
		}},
		{"book soft-deleted", func(t *testing.T, f *rmFixture, id string) {
			yes := true
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
			require.NoError(t, err)
		}},
		{"matched file rewritten", func(t *testing.T, f *rmFixture, _ string) {
			require.NoError(t, os.WriteFile(f.path("new/One.m4b"), []byte("a longer, different file"), 0o644))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newRMFixture(t)
			one := f.missingBook(t, "Book One", "old/One.m4b", nil)
			f.file(t, "new/One.m4b")
			f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
			planned := rmRow(t, f.plan(t), one)
			require.True(t, planned.Applicable(), planned.SkipReason)
			fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			require.Equal(t, planned.Fingerprint, fresh.Fingerprint)
			before := f.state(t, one)

			tc.drift(t, f, one)
			before2 := f.state(t, one)
			w, _ := f.writer()
			err = f.fx.Apply(context.Background(), w, fresh)
			require.Error(t, err)
			assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
			assert.Equal(t, 0, w.Journaled(), "nothing journaled")
			assert.Equal(t, before2, f.state(t, one), "nothing written")
			for fid, loc := range before.Files {
				assert.Equal(t, loc, f.state(t, one).Files[fid], "the file row is as planned")
			}
		})
	}
}

// TestReconcileFixer_ITunesRowsAreSkippedAndNeverWritten: an iTunes book (its
// iTunes id, or its iTunes path) and a match onto a file in an iTunes Media
// folder are all skipped as iTunes, and applying them writes nothing.
func TestReconcileFixer_ITunesRowsAreSkippedAndNeverWritten(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	pid := "0123456789ABCDEF"
	book := f.missingBook(t, "Book One", "old/One.m4b", func(b *database.Book) { b.ITunesPersistentID = &pid })
	f.file(t, "new/One.m4b")
	f.match(book, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	onto := f.missingBook(t, "Book Two", "old/Two.m4b", nil)
	f.file(t, "Music/iTunes Media/Audiobooks/Author A/Two.m4b")
	f.match(onto, "Book Two", "old/Two.m4b", "Music/iTunes Media/Audiobooks/Author A/Two.m4b", "filename", "low")
	itPath := "file:///Users/someone/Music/Three.m4b"
	byPath := f.missingBook(t, "Book Three", "old/Three.m4b", func(b *database.Book) {
		//lint:ignore SA1019 the fixture sets the legacy column the fixer must still honour
		b.ITunesPath = &itPath
	})
	f.file(t, "new/Three.m4b")
	f.match(byPath, "Book Three", "old/Three.m4b", "new/Three.m4b", "hash", "high")

	res := f.plan(t)
	assert.Equal(t, 0, res.Applicable)
	all := []string{book, onto, byPath}
	before := map[string]rmState{}
	for _, id := range all {
		r := rmRow(t, res, id)
		assert.Equal(t, repairs.SkipITunes, r.Skipped, id)
		assert.Equal(t, "iTunes", r.SkipReason, id)
		before[id] = f.state(t, id)
	}
	out, _ := f.apply(t, res, rmRowID(book), rmRowID(onto), rmRowID(byPath))
	assert.Equal(t, 0, out.Applied)
	for _, id := range all {
		assert.Equal(t, before[id], f.state(t, id), "nothing written")
	}
}

// TestReconcileFixer_ReplanEqualsPlanForUnchangedState: Plan and Replan run
// the same evaluate, so an untouched row re-plans identically.
func TestReconcileFixer_ReplanEqualsPlanForUnchangedState(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "original_hash", "high")
	lost := f.missingBook(t, "Book Two", "old/Two.m4b", nil)
	f.unmatched(lost, "Book Two", "old/Two.m4b")

	res := f.plan(t)
	for _, planned := range res.Rows {
		fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		assert.Equal(t, planned.Fingerprint, fresh.Fingerprint, planned.RowID)
		assert.Equal(t, planned.Skipped, fresh.Skipped, planned.RowID)
		assert.Equal(t, planned.Proposed, fresh.Proposed, planned.RowID)
	}
}

// TestReconcileFixer_ReplanSkipsAMatchWhoseFileIsGone: drift between the plan
// and the apply is caught by the re-plan, and the apply refuses the row.
func TestReconcileFixer_ReplanSkipsAMatchWhoseFileIsGone(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	res := f.plan(t)
	planned := rmRow(t, res, one)
	require.True(t, planned.Applicable())

	require.NoError(t, os.Remove(f.path("new/One.m4b")))
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.Equal(t, rmSkipFileGone, fresh.Skipped)
	assert.NotEqual(t, planned.Fingerprint, fresh.Fingerprint)

	before := f.state(t, one)
	out, _ := f.apply(t, res, planned.RowID)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, rmOutcomes(out)[planned.RowID])
	assert.Equal(t, before, f.state(t, one))
}

// TestReconcileFixer_ReplanCatchesEveryDrift covers the other re-checks: the
// book repointed, its file back, the matched file taken by another book or
// rewritten (same size, new modification time), and the book gaining a file
// row at the match.
func TestReconcileFixer_ReplanCatchesEveryDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		drift func(t *testing.T, f *rmFixture, bookID string)
		skip  string
	}{
		{"book repointed", func(t *testing.T, f *rmFixture, id string) {
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.FilePath = f.path("elsewhere/One.m4b"); return nil })
			require.NoError(t, err)
		}, rmSkipPathChanged},
		{"book file back", func(t *testing.T, f *rmFixture, _ string) { f.file(t, "old/One.m4b") }, rmSkipNotMissing},
		{"book retired", func(t *testing.T, f *rmFixture, id string) {
			yes := true
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
			require.NoError(t, err)
		}, rmSkipGone},
		{"file now a book's path", func(t *testing.T, f *rmFixture, _ string) {
			_, err := f.s.CreateBook(&database.Book{Title: "Book Other", FilePath: f.path("new/One.m4b")})
			require.NoError(t, err)
		}, rmSkipFileTracked},
		{"file now another book's file row", func(t *testing.T, f *rmFixture, _ string) {
			b, err := f.s.CreateBook(&database.Book{Title: "Book Other", FilePath: f.path("other")})
			require.NoError(t, err)
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path("new/One.m4b")}))
		}, rmSkipFileTracked},
		{"matched file touched (mtime only)", func(t *testing.T, f *rmFixture, _ string) {
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(f.path("new/One.m4b"), later, later))
		}, ""},
		{"book gained a file row at the match", func(t *testing.T, f *rmFixture, id string) {
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: id, FilePath: f.path("new/One.m4b")}))
		}, rmSkipOwnRowAtMatch},
		{"book lost its file row", func(t *testing.T, f *rmFixture, id string) {
			files, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			require.Len(t, files, 1)
			// Repointed elsewhere by another writer (never deleted).
			files[0].FilePath = f.path("elsewhere/One part.m4b")
			require.NoError(t, f.s.UpdateBookFile(files[0].ID, &files[0]))
		}, rmSkipNoFileRow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newRMFixture(t)
			one := f.missingBook(t, "Book One", "old/One.m4b", nil)
			f.file(t, "new/One.m4b")
			f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
			planned := rmRow(t, f.plan(t), one)
			require.True(t, planned.Applicable(), planned.SkipReason)
			tc.drift(t, f, one)
			fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.skip, fresh.Skipped, fresh.Reason)
			assert.NotEqual(t, planned.Fingerprint, fresh.Fingerprint)
		})
	}
}

// TestReconcileFixer_OldPathStates (B1): only "does not exist" is missing. A
// folder at the book's path is present (a folder book whose folder came
// back), and a stat error that is not "does not exist" (an unreadable parent)
// is an error row, never applicable.
func TestReconcileFixer_OldPathStates(t *testing.T) {
	t.Parallel()
	t.Run("folder comes back", func(t *testing.T) {
		t.Parallel()
		f := newRMFixture(t)
		one := f.missingBook(t, "Book One", "old/One", nil)
		f.file(t, "new/One.m4b")
		f.match(one, "Book One", "old/One", "new/One.m4b", "filename", "low")
		planned := rmRow(t, f.plan(t), one)
		require.True(t, planned.Applicable(), planned.SkipReason)

		require.NoError(t, os.MkdirAll(f.path("old/One"), 0o755))
		fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		assert.Equal(t, rmSkipNotMissing, fresh.Skipped, fresh.Reason)
	})
	t.Run("unreadable parent", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root reads through a 0o000 directory")
		}
		f := newRMFixture(t)
		one := f.missingBook(t, "Book One", "locked/One.m4b", nil)
		f.file(t, "new/One.m4b")
		f.match(one, "Book One", "locked/One.m4b", "new/One.m4b", "hash", "high")
		require.NoError(t, os.MkdirAll(f.path("locked"), 0o755))
		require.NoError(t, os.Chmod(f.path("locked"), 0o000))
		t.Cleanup(func() { _ = os.Chmod(f.path("locked"), 0o755) })

		r := rmRow(t, f.plan(t), one)
		assert.False(t, r.Applicable())
		assert.Equal(t, rmSkipError, r.Skipped, r.Reason)
		assert.Contains(t, r.Reason, "Could not tell whether")
		assert.Contains(t, r.Reason, "permission denied")

		missing, err := rmOldPathMissing(f.path("locked/One.m4b"))
		assert.Error(t, err, "an unreadable parent is not \"missing\"")
		assert.False(t, missing)
	})
	t.Run("only does-not-exist is missing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "folder"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "file.m4b"), []byte("x"), 0o644))
		for name, want := range map[string]bool{"folder": false, "file.m4b": false, "gone.m4b": true, "gone/deeper.m4b": true} {
			missing, err := rmOldPathMissing(filepath.Join(dir, name))
			require.NoError(t, err, name)
			assert.Equal(t, want, missing, name)
		}
	})
}

// TestReconcileFixer_IndexWarmingUp (S3): while memdb cannot list every
// book_file row at a path, the plan fails with "file index warming up; retry
// in a few minutes", a re-plan holds the row retry_later (its retry
// fingerprint is the planned one, so the engine says retry_later, not
// changed), and Apply refuses with ErrRetryLater.
func TestReconcileFixer_IndexWarmingUp(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	res := f.plan(t)
	planned := rmRow(t, res, one)
	require.True(t, planned.Applicable(), planned.SkipReason)
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)

	f.st.warming.Store(true)
	_, err = f.fx.Plan(context.Background(), nil, &fakeReporter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file index warming up; retry in a few minutes")

	held, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.True(t, held.RetryLater)
	assert.Equal(t, rmSkipIndexWarming, held.Skipped)
	assert.Equal(t, planned.Fingerprint, held.RetryFingerprint)

	before := f.state(t, one)
	out, _ := f.apply(t, res, planned.RowID)
	assert.Equal(t, repairs.OutcomeRetryLater, rmOutcomes(out)[planned.RowID])

	w, _ := f.writer()
	err = f.fx.Apply(context.Background(), w, fresh)
	assert.True(t, errors.Is(err, repairs.ErrRetryLater), "%v", err)
	assert.Equal(t, before, f.state(t, one), "nothing written while warming up")

	f.st.warming.Store(false)
	out, _ = f.apply(t, res, planned.RowID)
	assert.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID], "applies once warm")
}

// TestReconcileFixer_NoUsableScanFailsThePlan: with no completed scan the
// plan says so instead of listing nothing.
func TestReconcileFixer_NoUsableScanFailsThePlan(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	f.deps.reconcileScan = func() (ReconcileScan, error) {
		return ReconcileScan{AttemptOpID: "scan-9", AttemptStatus: "running"}, nil
	}
	_, err := f.fx.Plan(context.Background(), nil, &fakeReporter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Match missing files")
	assert.Contains(t, err.Error(), "the newest scan, scan-9, is running")
}

// TestReconcileFixer_RegisteredAsMissingFiles: the lane lists it by its id
// and title; it writes itself (takes the normal stand-down, keeps no
// per-run state) and needs settings.manage.
func TestReconcileFixer_RegisteredAsMissingFiles(t *testing.T) {
	t.Parallel()
	p := &Plugin{deps: fakeDeps{}}
	fx, ok := p.Repairs().Get(reconcileFixerID)
	require.True(t, ok)
	assert.Equal(t, "Missing files", fx.Title())
	assert.True(t, strings.Contains(fx.Description(), "nothing moves on disk"))
	assert.False(t, strings.Contains(fx.Description(), "Reconcile Apply"))
	_, isNoStandDown := fx.(repairs.NoScanStandDown)
	assert.False(t, isNoStandDown)
	_, isScoped := fx.(repairs.ApplyScoped)
	assert.False(t, isScoped)
	assert.Equal(t, []auth.Permission{auth.PermSettingsManage}, repairs.RequiredPermissions(fx))
}

// The candidate is the row's State: it round-trips, so a re-plan decides the
// same candidate the plan did.
func TestReconcileFixer_StateRoundTrips(t *testing.T) {
	t.Parallel()
	c := rmCandidate{BookID: "b1", Title: "Book One", OldPath: "/x/old", NewPath: "/x/new", MatchType: "hash",
		Confidence: "high", Score: 1, ScanOpID: "scan-1", ScanCompleted: "2026-10-14 03:00 UTC"}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var back rmCandidate
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, c, back)
}
