// file: internal/plugins/maintenance/recover_missing_files_test.go
// version: 1.1.0
// guid: c1f6a2d8-7b40-4e93-9a5c-6d81e0f4b72a
// last-edited: 2026-09-05

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// errFakeUpdate is the injected write failure for the update-error path test.
var errFakeUpdate = errors.New("fake update failure")

// recoverFakeStore implements recoverStore. It records every UpdateBookFile so a test
// can assert BOTH what was written and that nothing else was. It reuses the package's
// writeFile helper for the on-disk fixtures (os.Stat / filepath.WalkDir are the whole
// mechanism — an in-memory fixture cannot observe this op).
type recoverFakeStore struct {
	mu      sync.Mutex // UpdateBookFile is called from RunItems' worker pool
	cores   []database.BookFileCore
	full    map[string][]database.BookFile
	updates []database.BookFile
	// updateErr, when set, is returned by UpdateBookFile instead of writing — to exercise
	// the write phase's update-error branch (UpdateErrs++, row not counted as repointed).
	updateErr error
}

func (f *recoverFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.cores, nil
}
func (f *recoverFakeStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return f.full[bookID], nil
}
func (f *recoverFakeStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr // do not record — a failed write must not read as a success
	}
	f.updates = append(f.updates, *file)
	return nil
}

// The core recovery: a missing row whose recorded size matches exactly one unclaimed
// in-tree file of the same extension is repointed to it, and the full record (a
// fingerprint) survives — UpdateBookFile is a full-record replacement, so a partial
// write would destroy it.
func TestRecover_RepointsUniqueInTreeMatch(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "Author", "Book", "gone.mp3") // never written — the missing row
	target := filepath.Join(root, "Author", "Book", "renamed.mp3")
	writeFile(t, target, 4242) // the real bytes, under a name the shape rules can't derive

	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 4242}},
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: gone, FileSize: 4242,
			AcoustIDFingerprint: []byte("fp-keep-me"),
		}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MissingRows)
	require.Equal(t, 1, plan.Repointable)
	require.Equal(t, 1, plan.Repointed)
	require.Len(t, store.updates, 1)
	require.Equal(t, target, store.updates[0].FilePath, "FilePath must be rewritten to the real file")
	require.Equal(t, []byte("fp-keep-me"), store.updates[0].AcoustIDFingerprint,
		"the full record must survive the FilePath write")
}

// Dry run is the default and must write NOTHING while still reporting the plan.
func TestRecover_DryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "renamed.mp3"), 100)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 100}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 100}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{}, &fakeReporter{}) // Apply defaults false
	require.NoError(t, err)

	require.Equal(t, 1, plan.Repointable, "dry run still reports the plan")
	require.Equal(t, 0, plan.Repointed)
	require.Empty(t, store.updates, "DRY RUN MUST NOT WRITE")
}

// Two unclaimed in-tree files of the same size ⇒ which one this row meant is unknowable.
// Refuse and report; never guess.
func TestRecover_AmbiguousTwoInTreeCandidates(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "a", "one.mp3"), 500)
	writeFile(t, filepath.Join(root, "b", "two.mp3"), 500) // second same-size unclaimed file
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 500}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Ambiguous)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates, "an ambiguous row must not be repointed")
}

// Two missing rows want the ONE unclaimed file of their shared size ⇒ assigning it to
// either is unknowable (the duplicate-book-record shape). Both refused as size-collision.
func TestRecover_SizeCollisionTwoRowsOneFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "only.mp3"), 900) // the single candidate
	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "g1.mp3"), FileSize: 900}, // gone
			{ID: "f2", BookID: "b2", FilePath: filepath.Join(root, "g2.mp3"), FileSize: 900}, // gone, same size
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "f1", BookID: "b1"}}, "b2": {{ID: "f2", BookID: "b2"}},
		},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 2, plan.SizeCollision, "both rows are refused")
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)
}

// A same-size candidate whose EXTENSION differs is not the row's audio (requireExtMatch
// default true) ⇒ ext-mismatch, not repointed and NOT silently classed as nowhere. With
// requireExtMatch=false the same fixture becomes repointable — proving the distinction is
// real, not cosmetic.
func TestRecover_ExtMismatchVsForcedMatch(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "cover.jpg"), 777) // same size, wrong extension
	newStore := func() *recoverFakeStore {
		return &recoverFakeStore{
			cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 777}},
			full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
		}
	}

	// Default: extension must match → refused as ext-mismatch, not nowhere.
	plan, err := planRecoverMissingFiles(context.Background(), newStore(), root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.ExtMismatch)
	require.Equal(t, 0, plan.Nowhere, "a same-size wrong-ext file is ext-mismatch, never nowhere")
	require.Equal(t, 0, plan.Repointable)

	// Forced: requireExtMatch=false → the .jpg is accepted as the unique size match.
	no := false
	store2 := newStore()
	plan2, err := planRecoverMissingFiles(context.Background(), store2, root,
		recoverMissingParams{Apply: true, RequireExtMatch: &no}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan2.Repointable)
	require.Equal(t, 1, plan2.Repointed)
	require.Equal(t, 0, plan2.ExtMismatch)
}

// Bytes of the row's size exist only under a SourceDir ⇒ a reflink candidate for the
// deferred Branch B, censused as "outside" and NEVER repointed in-tree, even on apply.
func TestRecover_OutsideCensusNotRepointed(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir() // a separate source tree (newbooks/itunes analog)
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(src, "ingest", "match.mp3"), 321) // only match is OUTSIDE root
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 321}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true, SourceDirs: []string{src}}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Outside)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates, "outside candidates are reflink-later, never in-tree repoints")
}

// No file of the row's size anywhere walked ⇒ nowhere residue.
func TestRecover_NowhereWhenNoSizeMatch(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "other.mp3"), 111) // different size — not a candidate
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 999}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Nowhere)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)
}

// The claimed-file guard: a file of the row's exact size EXISTS but is already claimed by
// another (present) row. It must NOT be stolen — the missing row falls to nowhere, not
// repointed onto a file another row owns.
func TestRecover_NeverStealsAClaimedFile(t *testing.T) {
	root := t.TempDir()
	claimedFile := filepath.Join(root, "owned.mp3")
	writeFile(t, claimedFile, 640) // present, and owned by f-present below
	gone := filepath.Join(root, "gone.mp3")
	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "f-present", BookID: "b1", FilePath: claimedFile, FileSize: 640}, // present, claims owned.mp3
			{ID: "f-gone", BookID: "b2", FilePath: gone, FileSize: 640},           // missing, same size
		},
		full: map[string][]database.BookFile{
			"b1": {{ID: "f-present", BookID: "b1", FilePath: claimedFile}},
			"b2": {{ID: "f-gone", BookID: "b2"}},
		},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MissingRows, "only f-gone is missing; f-present's file is on disk")
	require.Equal(t, 0, plan.Repointable, "the only size-640 file is claimed by f-present — not a candidate")
	require.Equal(t, 1, plan.Nowhere)
	require.Empty(t, store.updates, "a claimed file must never be stolen for another row")
}

// A row with no recorded size cannot be matched on size ⇒ no-size bucket.
func TestRecover_NoSizeRowCannotMatch(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "renamed.mp3"), 50)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 0}}, // no size
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.NoSize)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)
}

// The cap bounds a run and the prefix taken must be STABLE across runs (sorted by file
// ID) so repeated runs converge instead of each repointing a different slice. Each row
// gets its OWN unique-size candidate so all are independently repointable.
func TestRecover_CapIsBoundedAndDeterministic(t *testing.T) {
	root := t.TempDir()
	var cores []database.BookFileCore
	full := map[string][]database.BookFile{}
	sizes := map[string]int64{"f3": 3000, "f1": 1000, "f2": 2000} // distinct sizes → each unique
	for _, id := range []string{"f3", "f1", "f2"} {               // deliberately unsorted
		gone := filepath.Join(root, id+"-gone.mp3")
		writeFile(t, filepath.Join(root, id+"-real.mp3"), int(sizes[id]))
		cores = append(cores, database.BookFileCore{ID: id, BookID: id, FilePath: gone, FileSize: sizes[id]})
		full[id] = []database.BookFile{{ID: id, BookID: id}}
	}
	store := &recoverFakeStore{cores: cores, full: full}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true, Max: 2}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 3, plan.Repointable, "the count reflects the FULL set, not the capped slice")
	require.Equal(t, 2, plan.CappedAt)
	require.Len(t, store.updates, 2)
	got := []string{store.updates[0].ID, store.updates[1].ID}
	require.ElementsMatch(t, []string{"f1", "f2"}, got, "cap must take a stable prefix by ID, never f3")
}

// The report records every missing row's decision (each bucket), so the population the
// "run the apply?" decision turns on is readable — not only the repointable rows.
func TestRecover_ReportCoversEveryBucket(t *testing.T) {
	root := t.TempDir()
	// repointable
	writeFile(t, filepath.Join(root, "r-real.mp3"), 10)
	// nowhere: size 20 nowhere on disk
	store := &recoverFakeStore{
		cores: []database.BookFileCore{
			{ID: "rep", BookID: "b1", FilePath: filepath.Join(root, "r-gone.mp3"), FileSize: 10},
			{ID: "now", BookID: "b2", FilePath: filepath.Join(root, "n-gone.mp3"), FileSize: 20},
		},
		full: map[string][]database.BookFile{"b1": {{ID: "rep", BookID: "b1"}}, "b2": {{ID: "now", BookID: "b2"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: false}, &fakeReporter{})
	require.NoError(t, err)

	require.Len(t, plan.all, 2, "every missing row is recorded, repointable and nowhere alike")
	seen := map[string]int{}
	for _, d := range plan.all {
		require.NotEmpty(t, d.Bucket)
		seen[d.Bucket]++
	}
	require.Equal(t, 1, seen["repointable"])
	require.Equal(t, 1, seen["nowhere"])
}

// A RootDir that does not exist (unmounted NAS, wrong --dir) must be a HARD ERROR, never
// a silent empty inventory that would report every missing row as "nowhere" — a
// confident-but-wrong census we might then act on. This is the blind-instrument guard.
func TestRecover_NonexistentRootIsFatal(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "not-mounted") // never created
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: filepath.Join(missingRoot, "gone.mp3"), FileSize: 100}},
		full:  map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, missingRoot,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.Error(t, err, "an unreadable RootDir must fail, not proceed with an empty inventory")
	require.Equal(t, 0, plan.Repointable, "no plan should be produced from a dead root")
	require.Empty(t, store.updates)
}

// Write-phase interlock, branch 1 of 2: the row vanishes between plan and write (GetBookFiles
// no longer returns the row's ID) ⇒ full==nil ⇒ counted as an update error, not written.
// (The re-stat size-changed branch is exercised structurally by the os.Stat guard but is not
// reachable in a single-call unit test — plan and write see the same on-disk file.)
func TestRecover_RowVanishedBeforeWriteCountsError(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "real.mp3"), 4242)
	store := &recoverFakeStore{
		cores: []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 4242}},
		// GetBookFiles returns a sibling with a DIFFERENT id → the target row is not found.
		full: map[string][]database.BookFile{"b1": {{ID: "other", BookID: "b1"}}},
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.Repointable, "it planned the repoint")
	require.Equal(t, 0, plan.Repointed, "but the row was gone at write time")
	require.Equal(t, 1, plan.UpdateErrs)
	require.Empty(t, store.updates)
}

// Write-phase interlock, branch 2 of 2: UpdateBookFile itself fails ⇒ counted as an update
// error, not a repoint. The store returns the error and records nothing.
func TestRecover_UpdateFailureCountsError(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone.mp3")
	writeFile(t, filepath.Join(root, "real.mp3"), 4242)
	store := &recoverFakeStore{
		cores:     []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: gone, FileSize: 4242}},
		full:      map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1"}}},
		updateErr: errFakeUpdate,
	}

	plan, err := planRecoverMissingFiles(context.Background(), store, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err, "one row's write failure must not fail the whole op")

	require.Equal(t, 1, plan.Repointable)
	require.Equal(t, 0, plan.Repointed)
	require.Equal(t, 1, plan.UpdateErrs)
	require.Empty(t, store.updates, "a failed write records nothing")
}

// The TSV must round-trip: one header + one line per decision, tabs intact.
func TestRecover_WriteReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.tsv")
	decisions := []recoverDecision{
		{Bucket: "repointable", FileID: "f1", BookID: "b1", Size: 10, OldPath: "/a/gone.mp3", NewPath: "/a/real.mp3", CandSeen: 1, Reason: "unique in-tree size match"},
		{Bucket: "nowhere", FileID: "f2", BookID: "b2", Size: 20, OldPath: "/a/weird\tname.mp3", Reason: "no file of this size"},
	}
	require.NoError(t, writeRecoverReport(path, decisions))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 3, "header + one line per decision")
	require.Equal(t, "bucket\tfile_id\tbook_id\tsize\told_path\tnew_path\tcand_seen\treason", lines[0])
	for i, l := range lines[1:] {
		require.Len(t, strings.Split(l, "\t"), 8, "row %d has the wrong column count: %q", i, l)
	}
}
