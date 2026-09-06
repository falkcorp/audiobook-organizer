// file: internal/plugins/maintenance/reflink_outside_test.go
// version: 1.0.0
// guid: 9d2c4a71-6e08-4b3f-8c1a-2f7b0e5d9a34
// last-edited: 2026-09-06

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// statSize reads a file's size, failing the test if it is not there.
func statSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	require.NoError(t, err)
	return st.Size()
}

// Branch B core: a missing row whose bytes exist ONLY under a SourceDir, unique in both
// directions (one unclaimed source of that size, wanted by one row), is RESTORED by
// cloning the source back to the row's own FilePath — no DB write, source left intact.
func TestReflink_RestoresUniqueOutsideMatch(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(root, "Author", "Book", "restored.mp3") // row's FilePath, currently gone
	srcFile := filepath.Join(src, "ingest", "match.mp3")
	writeFile(t, srcFile, 321)

	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}}},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Reflinkable)
	require.Equal(t, 1, plan.Reflinked)
	require.Equal(t, 0, plan.Outside, "with reflinkOutside on, the outside row is reflinkable, not merely censused")
	require.Empty(t, store.updates, "Branch B writes NO DB row — the row already points at the restored path")
	require.Equal(t, int64(321), statSize(t, dst), "the bytes are restored at the row's own FilePath")
	require.Equal(t, int64(321), statSize(t, srcFile), "the source is left intact (clone/copy, never a move)")

	acq, rel, ren := c.counts()
	require.Equal(t, 1, acq, "the reflink write phase stands the scanner down once")
	require.Equal(t, 1, rel)
	require.GreaterOrEqual(t, ren, 1, "the lease is renewed at least once per reflink item")
}

// Off by default: without reflinkOutside the same fixture is only CENSUSED as "outside"
// and nothing is created — proving the recovery is opt-in, not a behavior change.
func TestReflink_OffByDefaultOnlyCensuses(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(src, "match.mp3"), 321)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, nil, root,
		recoverMissingParams{Apply: true, SourceDirs: []string{src}}, &fakeReporter{}) // ReflinkOutside default false
	require.NoError(t, err)

	require.Equal(t, 1, plan.Outside)
	require.Equal(t, 0, plan.Reflinkable)
	require.Equal(t, 0, plan.Reflinked)
	require.NoFileExists(t, dst, "reflinkOutside off must create nothing")
}

// Two unclaimed sources of the row's size ⇒ which one is the row's is unknowable ⇒ refused
// as ambiguous, nothing created. The uniqueness gate mirrors the in-tree branch.
func TestReflink_TwoSourcesSameSizeRefusedAmbiguous(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(src, "a", "one.mp3"), 500)
	writeFile(t, filepath.Join(src, "b", "two.mp3"), 500) // second same-size source
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 500}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 500}}},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Ambiguous)
	require.Equal(t, 0, plan.Reflinkable)
	require.Equal(t, 0, plan.Reflinked)
	require.NoFileExists(t, dst)
}

// Two missing rows want the ONE unclaimed source of their shared size ⇒ assigning it is
// unknowable ⇒ both refused as size-collision (the duplicate-book-record shape).
func TestReflink_TwoRowsOneSourceRefusedCollision(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "only.mp3"), 900) // the single source
	d1 := filepath.Join(root, "g1.mp3")
	d2 := filepath.Join(root, "g2.mp3")
	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "f1", BookID: "b1", FilePath: d1, FileSize: 900},
			{ID: "f2", BookID: "b2", FilePath: d2, FileSize: 900},
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "f1", BookID: "b1", FilePath: d1, FileSize: 900}},
			"b2": {{ID: "f2", BookID: "b2", FilePath: d2, FileSize: 900}},
		},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 2, plan.SizeCollision, "both rows are refused")
	require.Equal(t, 0, plan.Reflinkable)
	require.NoFileExists(t, d1)
	require.NoFileExists(t, d2)
}

// The under-root guard: a row whose FilePath resolves OUTSIDE RootDir (a mangled/doubled
// path) is planned (its outside match is unique) but the write phase REFUSES to create a
// file outside the tree — counted as an error, nothing created there.
func TestReflink_RefusesDestOutsideRoot(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escaped", "row.mp3") // a sibling temp dir, NOT under root
	writeFile(t, filepath.Join(src, "match.mp3"), 200)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: outside, FileSize: 200}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: outside, FileSize: 200}}},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Reflinkable, "the uniqueness gate held, so it planned the reflink")
	require.Equal(t, 0, plan.Reflinked, "but the under-root guard refused to create it")
	require.Equal(t, 1, plan.ReflinkErrs)
	require.NoFileExists(t, outside, "nothing is created outside the library tree")
}

// Dry run reports the reflinkable plan but stands the scanner down for NOTHING and creates
// no file — the plan that informs the apply is safe to run first.
func TestReflink_DryRunReportsButCreatesNothing(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(src, "match.mp3"), 321)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}}},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{ReflinkOutside: true, SourceDirs: []string{src}}, // Apply defaults false
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Reflinkable, "dry run still reports the plan")
	require.Equal(t, 0, plan.Reflinked)
	require.NoFileExists(t, dst, "DRY RUN MUST NOT CREATE FILES")
	acq, _, _ := c.counts()
	require.Equal(t, 0, acq, "dry run must never stand the scanner down")
}

// A lapsed stand-down lease mid-reflink is a HARD ABORT: no file is created and the op
// surfaces an error, so a resumed scanner never races ungated writes.
func TestReflink_AbortsWhenLeaseLost(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(src, "match.mp3"), 321)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: dst, FileSize: 321}}},
	}
	c := &recordingScanController{renewOK: false} // lease gone from the first heartbeat

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-9"})
	require.Error(t, err, "a lapsed lease mid-apply must surface as an error")
	require.Contains(t, err.Error(), "lease lapsed")
	require.Equal(t, 0, plan.Reflinked, "no file is created once the lease is lost")
	require.NoFileExists(t, dst)

	acq, rel, _ := c.counts()
	require.Equal(t, 1, acq)
	require.Equal(t, 1, rel, "the gate is still released on the abort path")
}

// Branch A (in-tree repoint) and Branch B (reflink) run under ONE stand-down in a single
// apply: the in-tree row is repointed in the DB and the outside row is restored on disk,
// and only Branch A writes the store.
func TestReflink_MixedInTreeAndOutsideInOneApply(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	// Branch A — an in-tree unique match (repoint, DB only).
	inGone := filepath.Join(root, "in-gone.mp3")
	inReal := filepath.Join(root, "in-real.mp3")
	writeFile(t, inReal, 111)
	// Branch B — an outside unique match (reflink, disk only).
	outGone := filepath.Join(root, "out-gone.mp3")
	writeFile(t, filepath.Join(src, "out-real.mp3"), 222)

	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "fa", BookID: "ba", FilePath: inGone, FileSize: 111},
			{ID: "fb", BookID: "bb", FilePath: outGone, FileSize: 222},
		},
		full: map[string][]database.BookFile{
			"ba": {{ID: "fa", BookID: "ba", FilePath: inGone, FileSize: 111}},
			"bb": {{ID: "fb", BookID: "bb", FilePath: outGone, FileSize: 222}},
		},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Repointed, "Branch A repointed the in-tree row")
	require.Equal(t, 1, plan.Reflinked, "Branch B reflinked the outside row")
	require.Len(t, store.updates, 1, "only Branch A writes the DB")
	require.Equal(t, "fa", store.updates[0].ID)
	require.Equal(t, inReal, store.updates[0].FilePath)
	require.Equal(t, int64(222), statSize(t, outGone), "the outside row is restored at its own path")

	acq, rel, _ := c.counts()
	require.Equal(t, 1, acq, "one stand-down covers both write phases")
	require.Equal(t, 1, rel)
}

// The reflink cap bounds a run's FILE CREATIONS, not just the counter, and takes a stable
// prefix by file ID so repeated runs converge. Each row gets its own distinct-size source so
// all are independently reflinkable; Max:2 must create exactly two files and leave the third
// row's destination untouched — the assertion the cap actually gates the disk writes.
func TestReflink_CapBoundsFileCreation(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	sizes := map[string]int64{"f3": 3000, "f1": 1000, "f2": 2000} // distinct sizes → each unique
	dsts := map[string]string{}
	var cores []database.BookFileCore
	full := map[string][]database.BookFile{}
	for _, id := range []string{"f3", "f1", "f2"} { // deliberately unsorted
		dst := filepath.Join(root, id+"-gone.mp3")
		dsts[id] = dst
		writeFile(t, filepath.Join(src, id+"-real.mp3"), int(sizes[id]))
		cores = append(cores, database.BookFileCore{ID: id, BookID: id, FilePath: dst, FileSize: sizes[id]})
		full[id] = []database.BookFile{{ID: id, BookID: id, FilePath: dst, FileSize: sizes[id]}}
	}
	store := &recoverFakeStore{cores: cores, full: full}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, Max: 2, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 3, plan.Reflinkable, "the count reflects the FULL set, not the capped slice")
	require.Equal(t, 2, plan.ReflinkCappedAt)
	require.Equal(t, 2, plan.Reflinked, "only the capped prefix is created")
	require.FileExists(t, dsts["f1"], "cap takes a stable prefix by ID (f1)")
	require.FileExists(t, dsts["f2"], "cap takes a stable prefix by ID (f2)")
	require.NoFileExists(t, dsts["f3"], "the row beyond the cap must have NO file created")
}

// The no-clobber guarantee under a concurrent restore: two missing rows share one
// destination path (a pathological duplicate-row shape) and each matches its own unique
// source. Exactly one worker creates the file; the other's ReflinkOrCopy hits fs.ErrExist
// and SKIPS rather than truncating what the winner just wrote.
func TestReflink_ConcurrentRestoreSkipsExistingDest(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	shared := filepath.Join(root, "shared.mp3") // both rows point here; currently gone
	writeFile(t, filepath.Join(src, "a.mp3"), 100)
	writeFile(t, filepath.Join(src, "b.mp3"), 200)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "f1", BookID: "b1", FilePath: shared, FileSize: 100},
			{ID: "f2", BookID: "b2", FilePath: shared, FileSize: 200},
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "f1", BookID: "b1", FilePath: shared, FileSize: 100}},
			"b2": {{ID: "f2", BookID: "b2", FilePath: shared, FileSize: 200}},
		},
	}
	c := &recordingScanController{renewOK: true}

	plan, err := planRecoverMissingFiles(context.Background(), store, c, root,
		recoverMissingParams{Apply: true, ReflinkOutside: true, SourceDirs: []string{src}},
		&opIDReporter{id: "op-1"})
	require.NoError(t, err)

	require.Equal(t, 2, plan.Reflinkable, "both rows are individually unique against their own source")
	require.Equal(t, 1, plan.Reflinked, "exactly one worker creates the shared destination")
	require.Equal(t, 1, plan.ReflinkSkippedExists, "the other refuses to clobber it")
	require.FileExists(t, shared)
}
