// file: internal/plugins/maintenance/missing_file_repoint_test.go
// version: 1.4.0
// guid: b6d0f39c-4a17-4e82-95c1-70fe2a8b31d4
// last-edited: 2026-09-06

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

// repointFakeStore records every UpdateBookFile so a test can assert BOTH what was
// written and — just as importantly — that nothing else was.
type repointFakeStore struct {
	mu      sync.Mutex // UpdateBookFile is called from RunItems' worker pool
	cores   []database.BookFileCore
	books   []database.BookCore            // owning books, for the book-path fallback derivation
	full    map[string][]database.BookFile // bookID → rows
	updates []database.BookFile
	getErr  error
}

func (f *repointFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.cores, nil
}
func (f *repointFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if offset >= len(f.books) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.books) {
		end = len(f.books)
	}
	return f.books[offset:end], nil
}
func (f *repointFakeStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.full[bookID], nil
}
func (f *repointFakeStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, *file)
	return nil
}

// writeFile creates a real file on disk — os.Stat is the whole mechanism here, so
// these tests cannot use a purely in-memory fixture.
func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
}

// seedRepoint builds a store whose one row points at the BROKEN track-slash path.
func seedRepoint(t *testing.T, brokenPath string, size int64) *repointFakeStore {
	t.Helper()
	core := database.BookFileCore{ID: "f1", BookID: "b1", FilePath: brokenPath, FileSize: size}
	return &repointFakeStore{
		cores: []database.BookFileCore{core},
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: brokenPath, FileSize: size,
			AcoustIDFingerprint: []byte("fp-keep-me"),
		}}},
	}
}

// The happy path. The row points at ".../Stem - 2/35.mp3" (gone); the bytes live at
// ".../Stem - 02.mp3". Apply must rewrite FilePath and PRESERVE the fingerprint —
// UpdateBookFile is a full-record replacement, so a partial write would silently
// destroy the very data that makes these rows worth recovering.
func TestRepoint_RewritesPathAndPreservesFingerprint(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Stem - 02.mp3")
	writeFile(t, real, 1234)
	broken := filepath.Join(dir, "Stem - 2", "35.mp3")

	store := seedRepoint(t, broken, 1234)
	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MissingRows)
	require.Equal(t, 1, plan.Repointable)
	require.Equal(t, 1, plan.Repointed)
	require.Len(t, store.updates, 1)
	require.Equal(t, real, store.updates[0].FilePath, "FilePath must point at the real bytes")
	require.Equal(t, []byte("fp-keep-me"), store.updates[0].AcoustIDFingerprint,
		"fingerprint must survive the repoint")
}

// Dry run is the default and must write NOTHING, while still reporting what it would
// have done. A dry run that silently wrote would be the worst possible bug here.
func TestRepoint_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Stem - 02.mp3"), 10)
	store := seedRepoint(t, filepath.Join(dir, "Stem - 2", "35.mp3"), 10)

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{}, &fakeReporter{}) // Apply defaults to false
	require.NoError(t, err)

	require.Equal(t, 1, plan.Repointable, "dry run still reports the plan")
	require.Equal(t, 0, plan.Repointed)
	require.Empty(t, store.updates, "DRY RUN MUST NOT WRITE")
}

// 🔴 The collision case, and the reason this op is not a one-liner. A directory of
// tracks that collapsed into ONE file gives many broken rows deriving the SAME
// target. Repointing them all would leave N rows sharing one path — a duplicate-row
// corruption worse than the missing path it "fixed".
func TestRepoint_RefusesWhenSeveralRowsDeriveTheSameFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Stem - 02.mp3")
	writeFile(t, real, 99)

	// Two rows, same parent "Stem - 2", different leaves — both derive "Stem - 02.mp3".
	store := &repointFakeStore{
		cores: []database.BookFileCore{
			{ID: "f1", BookID: "b1", FilePath: filepath.Join(dir, "Stem - 2", "35.mp3"), FileSize: 99},
			{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "Stem - 2", "36.mp3"), FileSize: 99},
		},
		full: map[string][]database.BookFile{"b1": {
			{ID: "f1", BookID: "b1"}, {ID: "f2", BookID: "b1"},
		}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 2, plan.MissingRows)
	require.Equal(t, 2, plan.TargetCollision, "both colliding rows must be refused")
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates, "a collision must never be written")
}

// The target is already owned by a healthy row. Repointing onto it would create two
// rows for one file.
func TestRepoint_RefusesTargetAlreadyClaimedByAnotherRow(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Stem - 02.mp3")
	writeFile(t, real, 42)

	store := &repointFakeStore{
		cores: []database.BookFileCore{
			{ID: "broken", BookID: "b1", FilePath: filepath.Join(dir, "Stem - 2", "35.mp3"), FileSize: 42},
			{ID: "healthy", BookID: "b1", FilePath: real, FileSize: 42}, // already points there
		},
		full: map[string][]database.BookFile{"b1": {{ID: "broken", BookID: "b1"}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.TargetClaimed)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)
}

// Size is recorded on 100% of missing rows, so it is a real check on every one. A
// candidate whose size disagrees is a DIFFERENT file that merely shares a name.
func TestRepoint_RefusesOnSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Stem - 02.mp3"), 500)
	store := seedRepoint(t, filepath.Join(dir, "Stem - 2", "35.mp3"), 999) // row says 999, disk 500

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.SizeMismatch)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)

	// ...and the escape hatch works when the operator opts out explicitly.
	off := false
	store2 := seedRepoint(t, filepath.Join(dir, "Stem - 2", "35.mp3"), 999)
	plan2, err := planMissingFileRepoint(context.Background(), store2, nil,
		missingFileRepointParams{Apply: true, RequireSizeMatch: &off}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan2.Repointed, "requireSizeMatch=false must allow the repoint")
}

// A row whose bytes are simply gone must be left ALONE — not deleted, not rewritten.
// This op never removes a row; that boundary is the entire reason it is separate
// from missing-file-repair.
func TestRepoint_LeavesUnrecoverableRowsUntouched(t *testing.T) {
	dir := t.TempDir() // nothing written to disk at all
	store := seedRepoint(t, filepath.Join(dir, "Stem - 2", "35.mp3"), 10)

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MissingRows)
	require.Equal(t, 1, plan.NoCandidateBytes)
	require.Equal(t, 0, plan.Repointed)
	require.Empty(t, store.updates, "an unrecoverable row must be left exactly as it was")
}

// A present file must not be touched, and a path that does not match the track-slash
// shape must be counted as no-shape rather than guessed at.
func TestRepoint_IgnoresPresentFilesAndUnknownShapes(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "fine.mp3")
	writeFile(t, present, 7)

	store := &repointFakeStore{
		cores: []database.BookFileCore{
			{ID: "ok", BookID: "b1", FilePath: present, FileSize: 7},
			{ID: "weird", BookID: "b1", FilePath: filepath.Join(dir, "no-shape-here.mp3"), FileSize: 7},
		},
		full: map[string][]database.BookFile{"b1": {{ID: "weird", BookID: "b1"}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.MissingRows, "the present file is not missing")
	require.Equal(t, 1, plan.NoShape)
	require.Empty(t, store.updates)
}

// The cap bounds a run, and the prefix taken must be STABLE across runs — otherwise
// repeated runs would each rewrite a different arbitrary slice.
func TestRepoint_CapIsBoundedAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	cores := []database.BookFileCore{}
	full := []database.BookFile{}
	for _, id := range []string{"f3", "f1", "f2"} { // deliberately unsorted
		sub := filepath.Join(dir, id+" - 2")
		writeFile(t, filepath.Join(dir, id+" - 02.mp3"), 5)
		cores = append(cores, database.BookFileCore{
			ID: id, BookID: "b1", FilePath: filepath.Join(sub, "35.mp3"), FileSize: 5})
		full = append(full, database.BookFile{ID: id, BookID: "b1"})
	}
	store := &repointFakeStore{cores: cores, full: map[string][]database.BookFile{"b1": full}}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true, Max: 2}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 3, plan.Repointable, "repointable counts the FULL set, not the capped slice")
	require.Equal(t, 2, plan.CappedAt)
	require.Len(t, store.updates, 2)
	// Sorted by file ID, so the cap takes f1 and f2 — never f3.
	got := []string{store.updates[0].ID, store.updates[1].ID}
	require.ElementsMatch(t, []string{"f1", "f2"}, got, "cap must take a stable prefix by ID")
}

// 🔑 The report must account for EVERY missing row, not just the ones that would
// change. The first version of this op kept 40 decisions in arrival order; on the
// 2026-08-20 prod run that meant 40 collision rows from 3 adjacent books, and zero
// visibility into the 25,160 no-shape / 11,498 no-bytes / 14,439 repointable rows.
// A report that silently covers a subset reads exactly like a complete one.
func TestRepoint_ReportAccountsForEveryMissingRow(t *testing.T) {
	dir := t.TempDir()

	// One row per bucket, so a bucket that stops recording is a failing count.
	writeFile(t, filepath.Join(dir, "Ok - 02.mp3"), 42)    // repointable
	writeFile(t, filepath.Join(dir, "Coll - 02.mp3"), 42)  // collision (2 rows)
	writeFile(t, filepath.Join(dir, "Size - 02.mp3"), 999) // size mismatch
	writeFile(t, filepath.Join(dir, "Claim - 02.mp3"), 42) // already claimed

	cores := []database.BookFileCore{
		{ID: "ok", BookID: "b1", FilePath: filepath.Join(dir, "Ok - 2", "35.mp3"), FileSize: 42},
		{ID: "c1", BookID: "b1", FilePath: filepath.Join(dir, "Coll - 2", "35.mp3"), FileSize: 42},
		{ID: "c2", BookID: "b1", FilePath: filepath.Join(dir, "Coll - 2", "36.mp3"), FileSize: 42},
		{ID: "size", BookID: "b1", FilePath: filepath.Join(dir, "Size - 2", "35.mp3"), FileSize: 42},
		{ID: "claim", BookID: "b1", FilePath: filepath.Join(dir, "Claim - 2", "35.mp3"), FileSize: 42},
		{ID: "owner", BookID: "b1", FilePath: filepath.Join(dir, "Claim - 02.mp3"), FileSize: 42},
		{ID: "shape", BookID: "b1", FilePath: filepath.Join(dir, "not-a-track-path.mp3"), FileSize: 42},
		{ID: "bytes", BookID: "b1", FilePath: filepath.Join(dir, "Gone - 2", "35.mp3"), FileSize: 42},
	}
	full := map[string][]database.BookFile{"b1": {}}
	for _, c := range cores {
		full["b1"] = append(full["b1"], database.BookFile{ID: c.ID, BookID: c.BookID})
	}

	plan, err := planMissingFileRepoint(context.Background(), &repointFakeStore{cores: cores, full: full}, nil,
		missingFileRepointParams{Apply: false}, &fakeReporter{})
	require.NoError(t, err)

	// The identity that makes the counts trustworthy: buckets partition the missing
	// rows exactly. "owner" is present on disk, so it is not missing.
	sum := plan.NoShape + plan.NoCandidateBytes + plan.SizeMismatch +
		plan.TargetCollision + plan.TargetClaimed + plan.Repointable
	require.Equal(t, plan.MissingRows, sum, "buckets must partition the missing rows")
	require.Equal(t, plan.MissingRows, len(plan.all),
		"every missing row must produce exactly one decision in the report")

	// Every bucket must actually be represented -- a floor assertion, so a change
	// that stops recording one bucket cannot pass by recording more of another.
	seen := map[string]int{}
	for _, d := range plan.all {
		require.NotEmpty(t, d.Bucket, "decision %s has no bucket", d.FileID)
		seen[d.Bucket]++
	}
	for _, want := range []string{"repointable", "collision", "size-mismatch", "already-claimed", "no-shape", "no-bytes"} {
		require.NotZero(t, seen[want], "no %q rows in the report; buckets present: %v", want, seen)
	}

	// And the stratified sample must span buckets rather than describing arrival order.
	sampled := map[string]bool{}
	for _, d := range plan.Samples {
		sampled[d.Bucket] = true
	}
	require.Greater(t, len(sampled), 1, "sample covers only %v — it describes iteration order, not the population", sampled)
}

// The TSV must round-trip: one header + one line per decision, tabs intact.
func TestRepoint_WriteReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.tsv")
	decisions := []repointDecision{
		{Bucket: "repointable", FileID: "f1", BookID: "b1", OldPath: "/a/x - 2/35.mp3", NewPath: "/a/x - 02.mp3", Reason: "would repoint"},
		{Bucket: "no-shape", FileID: "f2", BookID: "b1", OldPath: "/a/weird\tname.mp3", Reason: "path does not match the track-slash shape"},
	}
	require.NoError(t, writeRepointReport(path, decisions))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 3, "header + one line per decision")
	require.Equal(t, "bucket\tfile_id\tbook_id\told_path\tnew_path\treason", lines[0])
	for i, l := range lines[1:] {
		require.Len(t, strings.Split(l, "\t"), 6, "row %d has the wrong column count: %q", i, l)
	}
	require.Contains(t, lines[1], "would repoint")
}

// The book-path fallback: a single-file book was renamed by an apply BEFORE the
// 2026-09-05 organizer fix, so the row still points at the pre-move path (which
// does not match the track-slash shape) while the bytes sit at the book's own
// current FilePath. The repoint must recover it via the owning book's path.
func TestRepoint_FallsBackToOwningBookPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "[PZG]", "Disciple Vol 01.m4b")
	writeFile(t, real, 4096)
	broken := filepath.Join(dir, "Mark Sanderlin", "Disciple Vol 01.m4b") // gone, no track-slash shape

	core := database.BookFileCore{ID: "f1", BookID: "b1", FilePath: broken, FileSize: 4096}
	store := &repointFakeStore{
		cores: []database.BookFileCore{core},
		books: []database.BookCore{{ID: "b1", FilePath: real}}, // book already moved to the new path
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: broken, FileSize: 4096,
		}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.Repointable, "the book-path fallback should recover this row")
	require.Equal(t, 1, plan.Repointed)
	require.Len(t, store.updates, 1)
	require.Equal(t, real, store.updates[0].FilePath, "row must point at the book's real path")
}

// A directory book's FilePath is a directory, not an audio file, so the book-path
// fallback must refuse it (repointing a row at a directory would be nonsense).
func TestRepoint_BookPathFallback_RefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	bookDir := filepath.Join(dir, "Some Author", "Some Book")
	require.NoError(t, os.MkdirAll(bookDir, 0o755))
	broken := filepath.Join(dir, "Old", "track.mp3") // gone, no track-slash shape

	core := database.BookFileCore{ID: "f1", BookID: "b1", FilePath: broken, FileSize: 10}
	store := &repointFakeStore{
		cores: []database.BookFileCore{core},
		books: []database.BookCore{{ID: "b1", FilePath: bookDir}}, // FilePath is a directory
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: broken, FileSize: 10,
		}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, plan.Repointable, "a directory must never be a repoint target")
	require.Equal(t, 1, plan.NoShape)
	require.Len(t, store.updates, 0)
}

// A size mismatch on the book-path fallback is refused, just as on the track-slash
// path: the file at the book's location must be the same size the row recorded.
func TestRepoint_BookPathFallback_RefusesSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "New", "book.m4b")
	writeFile(t, real, 500) // disk size differs from the row's recorded 4096
	broken := filepath.Join(dir, "Old", "book.m4b")

	core := database.BookFileCore{ID: "f1", BookID: "b1", FilePath: broken, FileSize: 4096}
	store := &repointFakeStore{
		cores: []database.BookFileCore{core},
		books: []database.BookCore{{ID: "b1", FilePath: real}},
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: broken, FileSize: 4096,
		}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	// The fallback checks size inline and never offers a mismatched candidate, so
	// the row is refused as no-shape (no track-slash match either) rather than
	// reaching the phase-2 size gate. Either way it must not be repointed.
	require.Equal(t, 0, plan.Repointable)
	require.Equal(t, 1, plan.NoShape)
	require.Len(t, store.updates, 0)
}

// The book-path fallback must be refused when a positive size cannot be verified.
// A zero recorded size cannot prove the file at the book's path is this row's file,
// so — unlike the track-slash path, whose candidate is derived from the row's own
// name — the fallback refuses rather than risk repointing at the wrong bytes. This
// holds even though requireSizeMatch defaults to true (the default gate skips a
// zero size; the fallback's own gate does not).
func TestRepoint_BookPathFallback_RefusesZeroSize(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "New", "book.m4b")
	writeFile(t, real, 4096)
	broken := filepath.Join(dir, "Old", "book.m4b")

	core := database.BookFileCore{ID: "f1", BookID: "b1", FilePath: broken, FileSize: 0} // no recorded size
	store := &repointFakeStore{
		cores: []database.BookFileCore{core},
		books: []database.BookCore{{ID: "b1", FilePath: real}},
		full: map[string][]database.BookFile{"b1": {{
			ID: "f1", BookID: "b1", FilePath: broken, FileSize: 0,
		}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, plan.Repointable, "a zero-size row must not be repointed via the book path")
	require.Empty(t, store.updates)
}

// The book-path fallback is still subject to the already-claimed guard: if a
// healthy sibling row of the same book already points at the book's path, the
// broken row must not be repointed onto it (two rows, one file).
func TestRepoint_BookPathFallback_RefusesAlreadyClaimedTarget(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "New", "book.m4b")
	writeFile(t, real, 4096)
	broken := filepath.Join(dir, "Old", "book.m4b")

	store := &repointFakeStore{
		cores: []database.BookFileCore{
			{ID: "broken", BookID: "b1", FilePath: broken, FileSize: 4096},
			{ID: "healthy", BookID: "b1", FilePath: real, FileSize: 4096}, // already at the book path
		},
		books: []database.BookCore{{ID: "b1", FilePath: real}},
		full:  map[string][]database.BookFile{"b1": {{ID: "broken", BookID: "b1"}}},
	}

	plan, err := planMissingFileRepoint(context.Background(), store, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.TargetClaimed)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)
}
