// file: internal/plugins/maintenance/reconcile_fixer_test.go
// version: 2.12.0
// guid: 8f0c7a5e-2b3d-4c61-9e47-a1d5b6c3f902
// last-edited: 2026-10-15

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
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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

// rmFencedFiles is the book_file store of a run whose fence fires at its next
// book_file write: ModifyBookFile refuses with opswriter.ErrFenced while
// fenced is set, as a stopped run's writer does.
type rmFencedFiles struct {
	*database.PebbleStore
	fenced atomic.Bool
}

func (s *rmFencedFiles) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.fenced.Load() {
		return nil, fmt.Errorf("modify book_file %s: %w", fileID, opswriter.ErrFenced)
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
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

// writer is a new apply op's journaled Writer; opID names the op.
func (f *rmFixture) writer() (*repairs.Writer, string) {
	f.applies++
	opID := fmt.Sprintf("repairs-apply-%d", f.applies)
	return f.writerFor(opID, f.s), opID
}

// writerFor is the journaled Writer of op opID writing book_file rows
// through files.
func (f *rmFixture) writerFor(opID string, files repairs.BookFileWriter) *repairs.Writer {
	return repairs.NewWriter(f.s, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(files, f.s, opID)
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
		// Same size, new modification time: only the mtime half of the
		// file check catches it.
		{"matched file touched (mtime only)", func(t *testing.T, f *rmFixture, _ string) {
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(f.path("new/One.m4b"), later, later))
		}},
		// The book's own row at the matched file: the owner read excludes
		// the book itself, so only the pre-write own-row check stops a
		// second row of the book at one file.
		{"book gained a file row at the match", func(t *testing.T, f *rmFixture, id string) {
			require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: id, FilePath: f.path("new/One.m4b")}))
		}},
		{"old file back on disk", func(t *testing.T, f *rmFixture, _ string) { f.file(t, "old/One.m4b") }},
		{"book became an iTunes book", func(t *testing.T, f *rmFixture, id string) {
			pid := "FEDCBA9876543210"
			_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
			require.NoError(t, err)
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

// TestReconcileFixer_ResumeFinishesARowCutOffBetweenItsTwoWrites (BL-1): a
// run stopped after step 1 (the book's path) and before step 2 (its file
// row) reports the row canceled_skipped with the book at the matched file
// and the row still at the old path. Re-planning that state gives the
// planned fingerprint, and the resumed apply (same op) writes step 2 only:
// the row ends applied with both writes in place, and the revert of the op
// puts both back.
func TestReconcileFixer_ResumeFinishesARowCutOffBetweenItsTwoWrites(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	before := f.state(t, one)
	res := f.plan(t)
	planned := rmRow(t, res, one)
	require.True(t, planned.Applicable(), planned.SkipReason)

	files := &rmFencedFiles{PebbleStore: f.s}
	files.fenced.Store(true)
	const opID = "repairs-apply-cut"
	run := func() *repairs.ApplyResult {
		out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
			repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor(opID, files), OpID: opID}, &fakeReporter{})
		require.NoError(t, err)
		return out
	}
	out := run()
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	mid := f.state(t, one)
	assert.Equal(t, f.path("new/One.m4b"), mid.Path, "step 1 written")
	assert.Equal(t, before.Files, mid.Files, "step 2 not written")

	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.True(t, fresh.Applicable(), fresh.Reason)
	assert.Equal(t, planned.Fingerprint, fresh.Fingerprint, "the cut-off state re-plans as planned")

	files.fenced.Store(false)
	out = run()
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	done := f.state(t, one)
	assert.Equal(t, f.path("new/One.m4b"), done.Path)
	require.Len(t, done.Files, 1)
	for _, loc := range done.Files {
		assert.Equal(t, f.path("new/One.m4b"), loc.Path)
		assert.False(t, loc.Missing)
	}

	_, err = audiobooks.NewRevertService(f.s).RevertOperation(opID)
	require.NoError(t, err)
	assert.Equal(t, before, f.state(t, one), "the revert puts the book and its row back")
}

// TestReconcileFixer_AFileMatchedToTwoBooksSkipsBoth (SF-1): when the scan
// matches one file to two books, both rows are skipped (not the first kept),
// so the applicable rows stay disjoint by file for the parallel apply
// workers, and applying them writes nothing. A third book's own match is
// unaffected.
func TestReconcileFixer_AFileMatchedToTwoBooksSkipsBoth(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	a := f.missingBook(t, "Book A", "old/A.m4b", nil)
	b := f.missingBook(t, "Book B", "old/B.m4b", nil)
	c := f.missingBook(t, "Book C", "old/C.m4b", nil)
	f.file(t, "new/shared.m4b")
	f.file(t, "new/C.m4b")
	f.match(a, "Book A", "old/A.m4b", "new/shared.m4b", "filename", "low")
	f.match(b, "Book B", "old/B.m4b", "new/shared.m4b", "filename", "low")
	f.match(c, "Book C", "old/C.m4b", "new/C.m4b", "hash", "high")

	res := f.plan(t)
	assert.Equal(t, 1, res.Applicable)
	for id, other := range map[string]string{a: b, b: a} {
		r := rmRow(t, res, id)
		assert.Equal(t, rmSkipSharedMatch, r.Skipped, r.Reason)
		assert.Contains(t, r.Reason, other)
	}
	assert.True(t, rmRow(t, res, c).Applicable())

	before := map[string]rmState{a: f.state(t, a), b: f.state(t, b)}
	out, _ := f.apply(t, res, rmRowID(a), rmRowID(b))
	assert.Equal(t, 0, out.Applied)
	for id, st := range before {
		assert.Equal(t, st, f.state(t, id), "nothing written")
	}
}

// TestReconcileFixer_PrecheckRefusesABookThatMovedOrWasRetired calls
// rmPrecheck directly: the Modify compare-and-set would also refuse these,
// so only a direct call shows the pre-write check itself holds.
func TestReconcileFixer_PrecheckRefusesABookThatMovedOrWasRetired(t *testing.T) {
	t.Parallel()
	for name, drift := range map[string]func(b *database.Book, f *rmFixture){
		"path changed": func(b *database.Book, f *rmFixture) { b.FilePath = f.path("elsewhere/One.m4b") },
		"soft-deleted": func(b *database.Book, _ *rmFixture) { yes := true; b.MarkedForDeletion = &yes },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newRMFixture(t)
			one := f.missingBook(t, "Book One", "old/One.m4b", nil)
			f.file(t, "new/One.m4b")
			f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
			planned := rmRow(t, f.plan(t), one)
			fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			d, ok := fresh.Detail.(*rmDecision)
			require.True(t, ok)
			rd, err := f.fx.reader()
			require.NoError(t, err)
			_, done, err := rmPrecheck(rd, d)
			require.NoError(t, err)
			require.Nil(t, done)

			_, err = f.s.ModifyBook(one, func(b *database.Book) error { drift(b, f); return nil })
			require.NoError(t, err)
			_, _, err = rmPrecheck(rd, d)
			assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "%v", err)
		})
	}
}

// TestReconcileFixer_TwoOwnRowsAtTheOldPathIsNoSingleRow: a book with two
// rows at its missing path has no single row to repoint (no_file_row).
func TestReconcileFixer_TwoOwnRowsAtTheOldPathIsNoSingleRow(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: one, FilePath: f.path("old/One.m4b"), Missing: true}))
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	r := rmRow(t, f.plan(t), one)
	assert.Equal(t, rmSkipNoFileRow, r.Skipped, r.Reason)
}

// TestReconcileFixer_AMatchedDirectoryIsNotAFile: the matched path must be a
// regular file; a directory there is file_gone, never applicable.
func TestReconcileFixer_AMatchedDirectoryIsNotAFile(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	require.NoError(t, os.MkdirAll(f.path("new/One.m4b"), 0o755))
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "filename", "low")
	r := rmRow(t, f.plan(t), one)
	assert.Equal(t, rmSkipFileGone, r.Skipped, r.Reason)

	assert.False(t, rmNewFile(f.path("new/One.m4b")).exists, "a directory")
	assert.False(t, rmNewFile(f.path("new/nothing.m4b")).exists, "nothing")
	assert.True(t, rmNewFile(f.file(t, "new/real.m4b")).exists, "a regular file")
}

// TestReconcileFixer_BookHashesFollowWhatTheMatchProves (NIT b): a hash match
// keeps the book's current-file hashes and the row's hash; an original_hash
// or filename match clears Book.FileHash and Book.OrganizedFileHash (they
// name content the book no longer points at) in the same compare-and-set as
// the path, and the row's hash, and keeps Book.OriginalFileHash. Reverting
// the apply puts every cleared hash back.
func TestReconcileFixer_BookHashesFollowWhatTheMatchProves(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	hashes := func(n string) func(b *database.Book) {
		return func(b *database.Book) {
			cur, org, orig := "cur-"+n, "org-"+n, "orig-"+n
			b.FileHash, b.OrganizedFileHash, b.OriginalFileHash = &cur, &org, &orig
		}
	}
	ids := map[string]string{}
	for _, k := range []string{"hash", "original_hash", "filename"} {
		id := f.missingBook(t, "Book "+k, "old/"+k+".m4b", hashes(k))
		f.file(t, "new/"+k+".m4b")
		f.match(id, "Book "+k, "old/"+k+".m4b", "new/"+k+".m4b", k, "high")
		ids[k] = id
	}
	type bookHashes struct{ cur, org, orig string }
	read := func(id string) bookHashes {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		return bookHashes{rmStr(b.FileHash), rmStr(b.OrganizedFileHash), rmStr(b.OriginalFileHash)}
	}
	before := map[string]rmState{}
	for _, id := range ids {
		before[id] = f.state(t, id)
	}
	res := f.plan(t)
	out, opID := f.apply(t, res, rmRowID(ids["hash"]), rmRowID(ids["original_hash"]), rmRowID(ids["filename"]))
	require.Equal(t, 3, out.Applied, "%+v", out.Rows)

	assert.Equal(t, bookHashes{"cur-hash", "org-hash", "orig-hash"}, read(ids["hash"]))
	assert.Equal(t, bookHashes{"", "", "orig-original_hash"}, read(ids["original_hash"]))
	assert.Equal(t, bookHashes{"", "", "orig-filename"}, read(ids["filename"]))
	for k, id := range ids {
		for _, loc := range f.state(t, id).Files {
			if k == "hash" {
				assert.Equal(t, "hash-Book hash", loc.Hash, k)
			} else {
				assert.Empty(t, loc.Hash, k)
			}
		}
	}

	_, err := audiobooks.NewRevertService(f.s).RevertOperation(opID)
	require.NoError(t, err)
	for k, id := range ids {
		assert.Equal(t, bookHashes{"cur-" + k, "org-" + k, "orig-" + k}, read(id), "revert restores %s", k)
		assert.Equal(t, before[id], f.state(t, id), "revert restores %s's path and row", k)
	}
}

// rmLegacyMove does what the legacy reconcile.apply (ExecuteReconcile) does
// to a book: its path to newRel, journaled as a file_path_update under its
// own op, its book_file rows and hashes untouched.
func (f *rmFixture) rmLegacyMove(t *testing.T, bookID, oldRel, newRel string, journal bool) {
	t.Helper()
	_, err := f.s.ModifyBook(bookID, func(b *database.Book) error { b.FilePath = f.path(newRel); return nil })
	require.NoError(t, err)
	if journal {
		require.NoError(t, f.s.CreateOperationChange(&database.OperationChange{
			ID: "legacy-" + bookID, OperationID: "reconcile-apply-legacy", BookID: bookID,
			ChangeType: "file_path_update", FieldName: "file_path", OldValue: f.path(oldRel), NewValue: f.path(newRel)}))
	}
}

// TestRR3Probe_LegacyReconcileMoveIsClaimed (round-3 B-1): the legacy
// reconcile.apply leaves the book at the matched file with its row at the
// old path, the same state as this fixer's cut-off step 1. With no journal
// evidence that this fixer wrote it, the row is a path_changed skip showing
// the book's real path, its fingerprint is not the planned one, the apply
// refuses it (changed_since_plan) and nothing is written: the row stays at
// the old path and the book's hashes are not cleared. Covered with and
// without the legacy op's own journal row.
func TestRR3Probe_LegacyReconcileMoveIsClaimed(t *testing.T) {
	t.Parallel()
	for _, journal := range []bool{false, true} {
		t.Run(fmt.Sprintf("journaled=%v", journal), func(t *testing.T) {
			t.Parallel()
			f := newRMFixture(t)
			id := f.missingBook(t, "Book F", "old/F.m4b", func(b *database.Book) {
				cur, org := "cur-F", "org-F"
				b.FileHash, b.OrganizedFileHash = &cur, &org
			})
			f.file(t, "new/F.m4b")
			f.match(id, "Book F", "old/F.m4b", "new/F.m4b", "filename", "medium")
			res := f.plan(t)
			planned := rmRow(t, res, id)
			require.True(t, planned.Applicable(), planned.SkipReason)
			fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)

			f.rmLegacyMove(t, id, "old/F.m4b", "new/F.m4b", journal)
			moved := f.state(t, id)

			// Between Replan and the write: the pre-write check refuses.
			w, _ := f.writer()
			err = f.fx.Apply(context.Background(), w, fresh)
			assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "%v", err)
			assert.Equal(t, 0, w.Journaled())

			again, err := f.fx.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			assert.Equal(t, rmSkipPathChanged, again.Skipped, again.Reason)
			assert.NotEqual(t, planned.Fingerprint, again.Fingerprint)
			assert.Equal(t, f.path("new/F.m4b"), again.Current["path"], "Current shows where the book really points")

			out, _ := f.apply(t, res, planned.RowID)
			assert.Equal(t, repairs.OutcomeChangedSincePlan, rmOutcomes(out)[planned.RowID])
			assert.Equal(t, moved, f.state(t, id), "nothing written")
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			assert.Equal(t, "cur-F", rmStr(b.FileHash))
			assert.Equal(t, "org-F", rmStr(b.OrganizedFileHash))
		})
	}
}

// TestReconcileFixer_BookAtMatchWithTwoRowsAtOldIsPathChanged (N-1): the
// book at the matched file is step 1 only with exactly one row left at the
// old path; with two it is path_changed.
func TestReconcileFixer_BookAtMatchWithTwoRowsAtOldIsPathChanged(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id := f.missingBook(t, "Book N", "old/N.m4b", nil)
	f.file(t, "new/N.m4b")
	f.match(id, "Book N", "old/N.m4b", "new/N.m4b", "hash", "high")
	planned := rmRow(t, f.plan(t), id)
	require.True(t, planned.Applicable())
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: id, FilePath: f.path("old/N.m4b"), Missing: true}))
	_, err := f.s.ModifyBook(id, func(b *database.Book) error { b.FilePath = f.path("new/N.m4b"); return nil })
	require.NoError(t, err)
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.Equal(t, rmSkipPathChanged, fresh.Skipped, fresh.Reason)
}

// rmCutThenNewOp runs op-1, whose fence fires at step 2's book_file write,
// then op-2, a NEW apply op that finishes the row, for a filename match on a
// book with both current-file hashes set. It returns the book id and its
// state before either op.
func rmCutThenNewOp(t *testing.T, f *rmFixture) (string, rmState) {
	t.Helper()
	id := f.missingBook(t, "Book K", "old/K.m4b", func(b *database.Book) {
		cur, org, orig := "cur-K", "org-K", "orig-K"
		b.FileHash, b.OrganizedFileHash, b.OriginalFileHash = &cur, &org, &orig
	})
	f.file(t, "new/K.m4b")
	f.match(id, "Book K", "old/K.m4b", "new/K.m4b", "filename", "medium")
	before := f.state(t, id)
	res := f.plan(t)
	planned := rmRow(t, res, id)
	require.True(t, planned.Applicable(), planned.SkipReason)
	files := &rmFencedFiles{PebbleStore: f.s}
	files.fenced.Store(true)
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-1", files), OpID: "op-1"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(out)[planned.RowID])
	out, err = repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-2", f.s), OpID: "op-2"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	return id, before
}

// rmHashesOf is a book's three hashes.
func (f *rmFixture) rmHashesOf(t *testing.T, id string) [3]string {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	return [3]string{rmStr(b.FileHash), rmStr(b.OrganizedFileHash), rmStr(b.OriginalFileHash)}
}

// TestRR3Probe_CancelThenNewApplyRevertSecondOpOnly (round-3 S-1): the op
// that finishes a cut-off row journals the step-1 changes it depends on, so
// reverting it ALONE restores the book's path, its row and its hashes (and
// the hash index finds the book again). op-1's rows were handed to op-2
// (rmHandOff), so reverting op-1 afterwards restores nothing.
func TestRR3Probe_CancelThenNewApplyRevertSecondOpOnly(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, before := rmCutThenNewOp(t, f)
	assert.Equal(t, [3]string{"", "", "orig-K"}, f.rmHashesOf(t, id))

	r2, err := audiobooks.NewRevertService(f.s).RevertOperation("op-2")
	require.NoError(t, err)
	assert.Zero(t, r2.Failed, "%+v", r2)
	assert.Equal(t, before, f.state(t, id), "op-2 alone restores the book and its row")
	assert.Equal(t, [3]string{"cur-K", "org-K", "orig-K"}, f.rmHashesOf(t, id), "and its hashes")
	bh, err := f.s.GetBookByFileHash("cur-K")
	require.NoError(t, err)
	require.NotNil(t, bh)
	assert.Equal(t, id, bh.ID)

	assert.Zero(t, f.rmRevert(t, "op-1"))
	assert.Equal(t, before, f.state(t, id))
	assert.Equal(t, [3]string{"cur-K", "org-K", "orig-K"}, f.rmHashesOf(t, id))
}

// TestRR3Probe_CancelThenNewApplyRevertFirstOpFirst (round-3 S-1, probe 3;
// round-4 SF-1): the cut-off op-1 was fenced at step 2's book_file write,
// after its repoint row was journaled. op-2 finishes the row and takes over
// every live row op-1 holds for it -- path, two hashes, that repoint row --
// so reverting op-1 first restores nothing and leaves the book and its row
// together at the matched file; reverting op-2 then restores the state
// before both.
func TestRR3Probe_CancelThenNewApplyRevertFirstOpFirst(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, before := rmCutThenNewOp(t, f)
	applied := f.state(t, id)
	assert.Equal(t, 4, f.rmHandedOff(t, "op-1"), "op-1's path, two hashes and repoint row")

	assert.Zero(t, f.rmRevert(t, "op-1"))
	assert.Equal(t, applied, f.state(t, id))
	f.rmConsistent(t, id)
	assert.Equal(t, [3]string{"", "", "orig-K"}, f.rmHashesOf(t, id))

	assert.Equal(t, 4, f.rmRevert(t, "op-2"))
	assert.Equal(t, before, f.state(t, id))
	assert.Equal(t, [3]string{"cur-K", "org-K", "orig-K"}, f.rmHashesOf(t, id))
}

// TestRR3Probe_ResumeFilenameMatchRestoresHashes: a resume within the SAME
// op journals nothing twice, and that op's revert restores path, row and
// every hash.
func TestRR3Probe_ResumeFilenameMatchRestoresHashes(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id := f.missingBook(t, "Book G", "old/G.m4b", func(b *database.Book) {
		cur, org, orig := "cur-G", "org-G", "orig-G"
		b.FileHash, b.OrganizedFileHash, b.OriginalFileHash = &cur, &org, &orig
	})
	f.file(t, "new/G.m4b")
	f.match(id, "Book G", "old/G.m4b", "new/G.m4b", "filename", "medium")
	before := f.state(t, id)
	res := f.plan(t)
	planned := rmRow(t, res, id)
	files := &rmFencedFiles{PebbleStore: f.s}
	files.fenced.Store(true)
	const opID = "repairs-apply-cut-g"
	run := func() *repairs.ApplyResult {
		out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
			repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor(opID, files), OpID: opID}, &fakeReporter{})
		require.NoError(t, err)
		return out
	}
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(run())[planned.RowID])
	journaled, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	files.fenced.Store(false)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(run())[planned.RowID])
	after, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	live := func(cs []*database.OperationChange, typ string) int {
		n := 0
		for _, c := range cs {
			if c.ChangeType == typ && !c.Voided {
				n++
			}
		}
		return n
	}
	assert.Equal(t, live(journaled, undo.ChangeTypeBookPathUpdate), live(after, undo.ChangeTypeBookPathUpdate), "no second path row")
	assert.Equal(t, live(journaled, "metadata_update"), live(after, "metadata_update"), "no second hash rows")

	_, err = audiobooks.NewRevertService(f.s).RevertOperation(opID)
	require.NoError(t, err)
	assert.Equal(t, before, f.state(t, id))
	assert.Equal(t, [3]string{"cur-G", "org-G", "orig-G"}, f.rmHashesOf(t, id))
}

// TestReconcileFixer_RefusedStepTwoOfAnEarlierOpIsChangedNotPartial (N-2):
// finishing an earlier op's cut-off row writes nothing of its own before
// step 2, so a refused step 2 is changed_since_plan (not
// partially_applied), and the journal-only rows it recorded are voided.
func TestReconcileFixer_RefusedStepTwoOfAnEarlierOpIsChangedNotPartial(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id := f.missingBook(t, "Book Q", "old/Q.m4b", nil)
	f.file(t, "new/Q.m4b")
	f.match(id, "Book Q", "old/Q.m4b", "new/Q.m4b", "hash", "high")
	res := f.plan(t)
	planned := rmRow(t, res, id)
	files := &rmFencedFiles{PebbleStore: f.s}
	files.fenced.Store(true)
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-1", files), OpID: "op-1"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(out)[planned.RowID])

	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.True(t, fresh.Applicable(), fresh.Reason)
	// Another writer touches the row's location (a hash filled in does not
	// count; its Missing flag does) between the re-plan and the write.
	rows, err := f.s.GetBookFiles(id)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = f.s.ModifyBookFile(id, rows[0].ID, func(bf *database.BookFile) error { bf.Missing = false; return nil })
	require.NoError(t, err)

	w := f.writerFor("op-2", f.s)
	err = f.fx.Apply(context.Background(), w, fresh)
	require.Error(t, err)
	assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "%v", err)
	assert.False(t, errors.Is(err, repairs.ErrPartiallyApplied), "%v", err)
	assert.ErrorContains(t, err, "is marked missing=false, not true", "names the field that changed (N-a)")
	changes, err := f.s.GetOperationChanges("op-2")
	require.NoError(t, err)
	for _, c := range changes {
		if c.ChangeType != database.ChangeTypeBookFileRepoint {
			assert.True(t, c.Voided, "journal-only row %s %s voided", c.ChangeType, c.FieldName)
		}
	}
}

// rmHookedBooks is the Writer's book store with hooks around ModifyBook:
// before runs (once) ahead of the first call, after runs (once) after the
// first call that succeeded.
type rmHookedBooks struct {
	*database.PebbleStore
	before, after         func()
	beforeDone, afterDone atomic.Bool
}

func (s *rmHookedBooks) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.before != nil && s.beforeDone.CompareAndSwap(false, true) {
		s.before()
	}
	b, err := s.PebbleStore.ModifyBook(id, fn)
	if err == nil && s.after != nil && s.afterDone.CompareAndSwap(false, true) {
		s.after()
	}
	return b, err
}

// TestReconcileFixer_HashChangedBetweenPrecheckAndWriteIsRefused (S-3): a
// book whose FileHash changes after the pre-write check and before step 1's
// Modify is refused by that Modify's compare-and-set (changed_since_plan),
// its journal rows are voided, and nothing of the fixer's is written.
func TestReconcileFixer_HashChangedBetweenPrecheckAndWriteIsRefused(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id := f.missingBook(t, "Book S", "old/S.m4b", func(b *database.Book) {
		cur, org := "cur-S", "org-S"
		b.FileHash, b.OrganizedFileHash = &cur, &org
	})
	f.file(t, "new/S.m4b")
	f.match(id, "Book S", "old/S.m4b", "new/S.m4b", "filename", "low")
	planned := rmRow(t, f.plan(t), id)
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	books := &rmHookedBooks{PebbleStore: f.s, before: func() {
		_, err := f.s.ModifyBook(id, func(b *database.Book) error { h := "raced"; b.FileHash = &h; return nil })
		require.NoError(t, err)
	}}
	w := repairs.NewWriter(books, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-s3")
	err = f.fx.Apply(context.Background(), w, fresh)
	assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "%v", err)
	assert.Equal(t, 0, w.Journaled(), "every journal row voided")
	changes, err := f.s.GetOperationChanges("op-s3")
	require.NoError(t, err)
	for _, c := range changes {
		assert.True(t, c.Voided, "%s %s", c.ChangeType, c.FieldName)
	}
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	assert.Equal(t, f.path("old/S.m4b"), b.FilePath)
	assert.Equal(t, "raced", rmStr(b.FileHash))
	assert.Equal(t, "org-S", rmStr(b.OrganizedFileHash))
}

// TestReconcileFixer_FenceInTheBeatBeforeStepTwo (N-4): the run's fence is
// revoked right after step 1, so step 2's writer beat refuses before any
// journal row of it is recorded. The row is canceled_skipped; a NEW op
// finishes it and takes op-1's rows over (rmHandOff), so reverting op-1
// first restores nothing and leaves book and row together at the matched
// file, and reverting op-2 then restores the state before both.
func TestReconcileFixer_FenceInTheBeatBeforeStepTwo(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id := f.missingBook(t, "Book V", "old/V.m4b", func(b *database.Book) {
		cur, org := "cur-V", "org-V"
		b.FileHash, b.OrganizedFileHash = &cur, &org
	})
	f.file(t, "new/V.m4b")
	f.match(id, "Book V", "old/V.m4b", "new/V.m4b", "filename", "low")
	before := f.state(t, id)
	res := f.plan(t)
	planned := rmRow(t, res, id)

	fence, revoke := opswriter.NewFence(1, nil)
	books := &rmHookedBooks{PebbleStore: f.s, after: revoke}
	w1 := repairs.NewWriter(books, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-1")
	out, err := repairs.RunApply(opswriter.WithFence(context.Background(), fence), f.fx, res, "plan-1",
		[]string{planned.RowID}, false, repairs.ApplyDeps{Guard: f.s, Writer: w1, OpID: "op-1"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	op1, err := f.s.GetOperationChanges("op-1")
	require.NoError(t, err)
	for _, c := range op1 {
		assert.NotEqual(t, database.ChangeTypeBookFileRepoint, c.ChangeType, "no step-2 row journaled")
	}

	out, err = repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-2", f.s), OpID: "op-2"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)

	applied := f.state(t, id)
	assert.Zero(t, f.rmRevert(t, "op-1"))
	assert.Equal(t, applied, f.state(t, id))
	f.rmConsistent(t, id)

	r2, err := audiobooks.NewRevertService(f.s).RevertOperation("op-2")
	require.NoError(t, err)
	assert.Zero(t, r2.Failed, "%+v", r2)
	assert.Equal(t, before, f.state(t, id))
}

// rmCutAtBeat runs op-1 with the run's fence revoked right after step 1, so
// step 2's writer beat refuses before its row is journaled: op-1 holds step
// 1 only and the row is canceled_skipped. A filename match, both hashes set.
func rmCutAtBeat(t *testing.T, f *rmFixture, name string) (id string, before rmState, res *repairs.PlanResult, planned repairs.Row) {
	t.Helper()
	id = f.missingBook(t, "Book "+name, "old/"+name+".m4b", func(b *database.Book) {
		cur, org := "cur-"+name, "org-"+name
		b.FileHash, b.OrganizedFileHash = &cur, &org
	})
	f.file(t, "new/"+name+".m4b")
	f.match(id, "Book "+name, "old/"+name+".m4b", "new/"+name+".m4b", "filename", "low")
	before = f.state(t, id)
	res = f.plan(t)
	planned = rmRow(t, res, id)
	fence, revoke := opswriter.NewFence(1, nil)
	books := &rmHookedBooks{PebbleStore: f.s, after: revoke}
	w1 := repairs.NewWriter(books, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-1")
	out, err := repairs.RunApply(opswriter.WithFence(context.Background(), fence), f.fx, res, "plan-1",
		[]string{planned.RowID}, false, repairs.ApplyDeps{Guard: f.s, Writer: w1, OpID: "op-1"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeCanceledSkip, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	return id, before, res, planned
}

// TestReconcileFixer_SameOpResumeRefusedStepTwoIsPartial (round-4 SF-2): the
// op that wrote step 1 resumes and its step 2 is refused; its own step 1
// stands, so the row is partially_applied, not changed_since_plan.
func TestReconcileFixer_SameOpResumeRefusedStepTwoIsPartial(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, _, _, planned := rmCutAtBeat(t, f, "P")
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.True(t, fresh.Applicable(), fresh.Reason)
	rows, err := f.s.GetBookFiles(id)
	require.NoError(t, err)
	_, err = f.s.ModifyBookFile(id, rows[0].ID, func(bf *database.BookFile) error { bf.Missing = false; return nil })
	require.NoError(t, err)
	err = f.fx.Apply(context.Background(), f.writerFor("op-1", f.s), fresh)
	assert.True(t, errors.Is(err, repairs.ErrPartiallyApplied), "%v", err)
}

// rmRevert reverts op and returns how many rows it restored, failing on any
// row it could not. An op whose every row was handed to a later op
// (rmHandOff) has nothing left: the revert service refuses it as already
// reverted, which counts as 0 here.
func (f *rmFixture) rmRevert(t *testing.T, op string) int {
	t.Helper()
	r, err := audiobooks.NewRevertService(f.s).RevertOperation(op)
	if err != nil && strings.Contains(err.Error(), "has already been reverted") {
		return 0
	}
	require.NoError(t, err)
	assert.Zero(t, r.Failed, "%s: %+v", op, r)
	return r.Restored
}

// rmHandedOff counts op's rows marked reverted without being voided: the
// rows a later op took over.
func (f *rmFixture) rmHandedOff(t *testing.T, op string) int {
	t.Helper()
	rows, err := f.s.GetOperationChanges(op)
	require.NoError(t, err)
	n := 0
	for _, c := range rows {
		if !c.Voided && c.RevertedAt != nil {
			n++
		}
	}
	return n
}

// rmConsistent asserts the book and its single file row point at the same
// file: never the book at one and its row at the other.
func (f *rmFixture) rmConsistent(t *testing.T, id string) {
	t.Helper()
	st := f.state(t, id)
	require.Len(t, st.Files, 1)
	for _, loc := range st.Files {
		assert.Equal(t, st.Path, loc.Path, "book path and its file row agree")
	}
}

// TestReconcileFixer_HandOffRevertCutOpAlone (round-4 SF-1, probe P2): op-1
// is cut in the beat before step 2, op-2 finishes. Reverting op-1 alone
// restores nothing (its rows are op-2's now) and leaves book and row
// together; reverting op-2 alone instead restores the state before both.
func TestReconcileFixer_HandOffRevertCutOpAlone(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"op-1", "op-2"} {
		t.Run("first="+first, func(t *testing.T) {
			t.Parallel()
			f := newRMFixture(t)
			id, before, res, planned := rmCutAtBeat(t, f, "Q")
			out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
				repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-2", f.s), OpID: "op-2"}, &fakeReporter{})
			require.NoError(t, err)
			require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID])
			applied := f.state(t, id)
			assert.Equal(t, 3, f.rmHandedOff(t, "op-1"), "op-1's path and two hash rows")

			if first == "op-1" {
				assert.Zero(t, f.rmRevert(t, "op-1"))
				assert.Equal(t, applied, f.state(t, id))
				f.rmConsistent(t, id)
				assert.Equal(t, 4, f.rmRevert(t, "op-2"))
			} else {
				assert.Equal(t, 4, f.rmRevert(t, "op-2"))
				assert.Equal(t, before, f.state(t, id))
				assert.Zero(t, f.rmRevert(t, "op-1"))
			}
			assert.Equal(t, before, f.state(t, id))
			assert.Equal(t, [3]string{"cur-Q", "org-Q", ""}, f.rmHashesOf(t, id))
		})
	}
}

// TestReconcileFixer_HandOffStaleCutOpAfterReapply (round-4 SF-1, probe P3):
// op-2 finishes op-1's cut row and is reverted (the state before both); op-3
// re-applies from a new plan; reverting op-1 then restores nothing, so the
// book and its row stay at the matched file together, and reverting op-3
// restores the state before every op.
func TestReconcileFixer_HandOffStaleCutOpAfterReapply(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, before, res, planned := rmCutAtBeat(t, f, "R")
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-2", f.s), OpID: "op-2"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID])
	assert.Equal(t, 4, f.rmRevert(t, "op-2"))
	require.Equal(t, before, f.state(t, id))

	res3 := f.plan(t)
	p3 := rmRow(t, res3, id)
	require.True(t, p3.Applicable(), p3.Reason)
	out, err = repairs.RunApply(context.Background(), f.fx, res3, "plan-3", []string{p3.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-3", f.s), OpID: "op-3"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[p3.RowID], "%+v", out.Rows)
	applied := f.state(t, id)

	assert.Zero(t, f.rmRevert(t, "op-1"))
	assert.Equal(t, applied, f.state(t, id))
	f.rmConsistent(t, id)
	assert.Equal(t, 4, f.rmRevert(t, "op-3"))
	assert.Equal(t, before, f.state(t, id))
	assert.Equal(t, [3]string{"cur-R", "org-R", ""}, f.rmHashesOf(t, id))
}

// TestReconcileFixer_HandOffTakesEveryCutOp (round-4 SF-1, probe P4): op-1
// is cut after step 1 and op-2 after its journal-only rows (written by hand
// here, book untouched); op-3 finishes and takes BOTH ops' rows over, so
// reverting op-1 or op-2 alone restores nothing and op-3 alone undoes it all.
func TestReconcileFixer_HandOffTakesEveryCutOp(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, before, res, planned := rmCutAtBeat(t, f, "S")
	w2 := f.writerFor("op-2", f.s)
	require.NoError(t, w2.Journal(id, undo.ChangeTypeBookPathUpdate, "file_path", f.path("old/S.m4b"), f.path("new/S.m4b")))
	require.NoError(t, w2.Journal(id, "metadata_update", rmFieldFileHash, "cur-S", ""))
	require.NoError(t, w2.Journal(id, "metadata_update", rmFieldOrganizedHash, "org-S", ""))
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", []string{planned.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: f.writerFor("op-3", f.s), OpID: "op-3"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.OutcomeApplied, rmOutcomes(out)[planned.RowID], "%+v", out.Rows)
	applied := f.state(t, id)
	assert.Equal(t, 3, f.rmHandedOff(t, "op-1"))
	assert.Equal(t, 3, f.rmHandedOff(t, "op-2"))

	assert.Zero(t, f.rmRevert(t, "op-1"))
	assert.Zero(t, f.rmRevert(t, "op-2"))
	assert.Equal(t, applied, f.state(t, id))
	f.rmConsistent(t, id)
	assert.Equal(t, 4, f.rmRevert(t, "op-3"))
	assert.Equal(t, before, f.state(t, id))
	assert.Equal(t, [3]string{"cur-S", "org-S", ""}, f.rmHashesOf(t, id))
}

// rmHookedFiles is the Writer's book_file store with a hook run once ahead of
// the first ModifyBookFile.
type rmHookedFiles struct {
	*database.PebbleStore
	before     func()
	beforeDone atomic.Bool
}

func (s *rmHookedFiles) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.before != nil && s.beforeDone.CompareAndSwap(false, true) {
		s.before()
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

// TestReconcileFixer_FinishRepointRefusedAtTheWriteIsVoided (round-4 N-b):
// op-2 finishes op-1's cut row, and the row's location changes after
// RepointBookFile read it and before its compare-and-set write. The write is
// refused (changed_since_plan: op-2 wrote nothing), and every row op-2
// journaled -- the repoint row included -- is voided, so no revert reads a
// repoint that never happened; op-1 keeps its rows (no hand-off).
func TestReconcileFixer_FinishRepointRefusedAtTheWriteIsVoided(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	id, _, _, planned := rmCutAtBeat(t, f, "W")
	fresh, err := f.fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	require.True(t, fresh.Applicable(), fresh.Reason)
	rows, err := f.s.GetBookFiles(id)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	files := &rmHookedFiles{PebbleStore: f.s, before: func() {
		_, merr := f.s.ModifyBookFile(id, rows[0].ID, func(bf *database.BookFile) error { bf.FileSize++; return nil })
		require.NoError(t, merr)
	}}
	err = f.fx.Apply(context.Background(), f.writerFor("op-2", files), fresh)
	assert.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "%v", err)
	assert.False(t, errors.Is(err, repairs.ErrPartiallyApplied), "%v", err)
	changes, err := f.s.GetOperationChanges("op-2")
	require.NoError(t, err)
	var repoint int
	for _, c := range changes {
		assert.True(t, c.Voided, "%s %s voided", c.ChangeType, c.FieldName)
		if c.ChangeType == database.ChangeTypeBookFileRepoint {
			repoint++
		}
	}
	assert.Equal(t, 1, repoint, "the repoint row was journaled, then voided")
	assert.Zero(t, f.rmHandedOff(t, "op-1"))
	assert.Equal(t, f.path("old/W.m4b"), f.state(t, id).Files[rows[0].ID].Path)
}
