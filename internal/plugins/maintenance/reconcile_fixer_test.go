// file: internal/plugins/maintenance/reconcile_fixer_test.go
// version: 1.0.0
// guid: 8f0c7a5e-2b3d-4c61-9e47-a1d5b6c3f902
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The fixer reads the server's store through rmReader: database.Store must
// satisfy it, or production refuses every plan.
var _ rmReader = database.Store(nil)

// rmDeps is fakeDeps with a scripted latest scan and an EnqueueOp that
// records what the fixer starts. When exec is set, each enqueued
// reconcile.apply also runs, synchronously, the core the real op runs.
type rmDeps struct {
	fakeDeps
	mu       sync.Mutex
	enqueued []rmApplyParams
	defIDs   []string
	exec     func(rmApplyParams) error
}

func (d *rmDeps) EnqueueOp(_ context.Context, defID string, params any) (string, error) {
	p, ok := params.(rmApplyParams)
	if !ok {
		return "", errors.New("rmDeps: unexpected params type")
	}
	d.mu.Lock()
	d.defIDs = append(d.defIDs, defID)
	d.enqueued = append(d.enqueued, p)
	n := len(d.enqueued)
	d.mu.Unlock()
	if d.exec != nil {
		if err := d.exec(p); err != nil {
			return "", err
		}
	}
	return "apply-op-" + string(rune('0'+n)), nil
}

// rmFixture is a library with missing-file books and untracked files on
// disk, and the scan that matched them. Titles and paths are synthetic.
type rmFixture struct {
	s    *database.PebbleStore
	dir  string
	deps *rmDeps
	fx   *reconcileFixer
	scan reconcile.ReconcilePreviewResult
}

func newRMFixture(t *testing.T) *rmFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &rmFixture{s: newFragStore(t), dir: dir}
	f.deps = &rmDeps{fakeDeps: fakeDeps{store: f.s, root: dir}}
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

// missingBook creates a book whose file (and one book_file row) is at rel,
// which is NOT on disk.
func (f *rmFixture) missingBook(t *testing.T, title, rel string, mut func(*database.Book)) string {
	t.Helper()
	b := &database.Book{Title: title, FilePath: f.path(rel)}
	if mut != nil {
		mut(b)
	}
	b, err := f.s.CreateBook(b)
	require.NoError(t, err)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f.path(rel), Missing: true}))
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

func (f *rmFixture) apply(t *testing.T, res *repairs.PlanResult, rowIDs ...string) *repairs.ApplyResult {
	t.Helper()
	w := repairs.NewWriter(f.s, f.s, reconcileFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "repairs-apply-1")
	out, err := repairs.RunApply(context.Background(), f.fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Writer: w, OpID: "repairs-apply-1"}, &fakeReporter{})
	require.NoError(t, err)
	return out
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
// every unmatched book is one row, with book -> file, match type and
// confidence on the match rows.
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
	assert.Equal(t, f.path("old/Book One.m4b")+" (missing)", r1.Current["path"])
	assert.Equal(t, map[string]string{"path": f.path("new/Book One.m4b"), "match_type": "hash", "confidence": "high"}, r1.Proposed)
	assert.Equal(t, repairs.RiskLow, r1.Risk)
	assert.Contains(t, r1.Evidence[0], "scan-1")

	r2 := rmRow(t, res, two)
	assert.True(t, r2.Applicable(), r2.SkipReason)
	assert.Equal(t, "low", r2.Proposed["confidence"])
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

	// Applying it starts nothing.
	out := f.apply(t, res, r.RowID)
	assert.Equal(t, repairs.OutcomeNotApplicable, rmOutcomes(out)[r.RowID])
	assert.Empty(t, f.deps.enqueued)
}

// TestReconcileFixer_ApplyPassesOnlyPickedRowsToReconcileApply: two of three
// applicable rows are picked; ONE reconcile.apply run carries exactly them.
// A picked row that drifted since the plan is refused and left out.
func TestReconcileFixer_ApplyPassesOnlyPickedRowsToReconcileApply(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	var ids []string
	for _, n := range []string{"A", "B", "C", "D"} {
		id := f.missingBook(t, "Book "+n, "old/"+n+".m4b", nil)
		f.file(t, "new/"+n+".m4b")
		f.match(id, "Book "+n, "old/"+n+".m4b", "new/"+n+".m4b", "hash", "high")
		ids = append(ids, id)
	}
	res := f.plan(t)
	require.Equal(t, 4, res.Applicable)

	out := f.apply(t, res, rmRowID(ids[0]), rmRowID(ids[2]))
	assert.Equal(t, 2, out.Applied)
	require.Equal(t, []string{"reconcile.apply"}, f.deps.defIDs, "one run")
	got := f.deps.enqueued[0].Matches
	sort.Slice(got, func(i, j int) bool { return got[i].BookID < got[j].BookID })
	want := []reconcile.ReconcileApplyItem{
		{BookID: ids[0], NewPath: f.path("new/A.m4b")},
		{BookID: ids[2], NewPath: f.path("new/C.m4b")},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].BookID < want[j].BookID })
	assert.Equal(t, want, got)
	assert.Equal(t, "apply-op-1", out.FollowUp)

	// A drifted pick (its matched file gone) is refused at re-plan and is
	// not handed on; the other pick is.
	require.NoError(t, os.Remove(f.path("new/D.m4b")))
	out = f.apply(t, res, rmRowID(ids[1]), rmRowID(ids[3]))
	o := rmOutcomes(out)
	assert.Equal(t, repairs.OutcomeApplied, o[rmRowID(ids[1])])
	assert.Equal(t, repairs.OutcomeChangedSincePlan, o[rmRowID(ids[3])])
	require.Len(t, f.deps.enqueued, 2)
	assert.Equal(t, []reconcile.ReconcileApplyItem{{BookID: ids[1], NewPath: f.path("new/B.m4b")}}, f.deps.enqueued[1].Matches)
}

// TestReconcileFixer_ApplySplitsAtTheBulkApplyCap: reconcile.apply refuses a
// whole set over the cap, so a larger selection goes in runs of the cap.
// Not parallel: it sets the process-wide config.
func TestReconcileFixer_ApplySplitsAtTheBulkApplyCap(t *testing.T) {
	prev := config.Snapshot().BulkApplyMaxItems
	config.Mutate(func(c *config.Config) { c.BulkApplyMaxItems = 2 })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.BulkApplyMaxItems = prev }) })

	f := newRMFixture(t)
	var rows []string
	for _, n := range []string{"A", "B", "C"} {
		id := f.missingBook(t, "Book "+n, "old/"+n+".m4b", nil)
		f.file(t, "new/"+n+".m4b")
		f.match(id, "Book "+n, "old/"+n+".m4b", "new/"+n+".m4b", "hash", "high")
		rows = append(rows, rmRowID(id))
	}
	res := f.plan(t)
	out := f.apply(t, res, rows...)
	assert.Equal(t, 3, out.Applied)
	require.Len(t, f.deps.enqueued, 2)
	assert.Len(t, f.deps.enqueued[0].Matches, 2)
	assert.Len(t, f.deps.enqueued[1].Matches, 1)
	assert.Equal(t, "apply-op-1,apply-op-2", out.FollowUp)
}

// TestReconcileFixer_ITunesRowsAreSkippedAndNeverWritten: an iTunes book
// (its iTunes id) and a match onto a file in an iTunes Media folder are both
// skipped as iTunes, and applying them starts nothing.
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

	res := f.plan(t)
	assert.Equal(t, 0, res.Applicable)
	for _, id := range []string{book, onto} {
		r := rmRow(t, res, id)
		assert.Equal(t, repairs.SkipITunes, r.Skipped, id)
		assert.Equal(t, "iTunes", r.SkipReason, id)
	}
	out := f.apply(t, res, rmRowID(book), rmRowID(onto))
	assert.Equal(t, 0, out.Applied)
	assert.Empty(t, f.deps.enqueued, "nothing handed to reconcile.apply")
	for _, id := range []string{book, onto} {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		assert.Equal(t, f.path("old/"+map[string]string{book: "One", onto: "Two"}[id]+".m4b"), b.FilePath)
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
		assert.Equal(t, planned, fresh, planned.RowID)
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

	out := f.apply(t, res, planned.RowID)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, rmOutcomes(out)[planned.RowID])
	assert.Empty(t, f.deps.enqueued)
}

// TestReconcileFixer_ReplanCatchesEveryDrift covers the other re-checks: the
// book repointed, its file back, the matched file taken by another book.
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

// TestReconcileFixer_OwnFileRowAtTheMatchIsNotAnotherOwner: the book's own
// book_file row at the matched path is the book's file, not a conflict.
func TestReconcileFixer_OwnFileRowAtTheMatchIsNotAnotherOwner(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	f.file(t, "new/One.m4b")
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: one, FilePath: f.path("new/One.m4b")}))
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	r := rmRow(t, f.plan(t), one)
	assert.True(t, r.Applicable(), r.SkipReason)
}

// TestReconcileFixer_NoBookFileRowIsDeletedByApply runs the picked match
// through reconcile.apply's own core: the book is repointed and every
// book_file row is still there.
func TestReconcileFixer_NoBookFileRowIsDeletedByApply(t *testing.T) {
	t.Parallel()
	f := newRMFixture(t)
	one := f.missingBook(t, "Book One", "old/One.m4b", nil)
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{BookID: one, FilePath: f.path("old/One part 2.m4b"), Missing: true}))
	f.file(t, "new/One.m4b")
	f.match(one, "Book One", "old/One.m4b", "new/One.m4b", "hash", "high")
	f.deps.exec = func(p rmApplyParams) error {
		return reconcile.ExecuteReconcile(context.Background(), f.s, "reconcile-apply-1",
			func(any) error { return nil }, p.Matches, logger.New("test"))
	}
	countRows := func() int {
		all, err := f.s.GetAllBookFilesCore()
		require.NoError(t, err)
		return len(all)
	}
	before := countRows()
	require.Equal(t, 2, before)

	res := f.plan(t)
	out := f.apply(t, res, rmRowID(one))
	require.Equal(t, 1, out.Applied)
	require.Len(t, f.deps.enqueued, 1)
	assert.Equal(t, before, countRows(), "no book_file row deleted or created")
	b, err := f.s.GetBookByID(one)
	require.NoError(t, err)
	assert.Equal(t, f.path("new/One.m4b"), b.FilePath, "reconcile.apply repointed the book")
	files, err := f.s.GetBookFiles(one)
	require.NoError(t, err)
	assert.Len(t, files, 2)
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
// and title.
func TestReconcileFixer_RegisteredAsMissingFiles(t *testing.T) {
	t.Parallel()
	p := &Plugin{deps: fakeDeps{}}
	fx, ok := p.Repairs().Get(reconcileFixerID)
	require.True(t, ok)
	assert.Equal(t, "Missing files", fx.Title())
	assert.True(t, strings.Contains(fx.Description(), "Reconcile Apply"))
	_, isNoStandDown := fx.(repairs.NoScanStandDown)
	assert.True(t, isNoStandDown)
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
