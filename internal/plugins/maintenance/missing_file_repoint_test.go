// file: internal/plugins/maintenance/missing_file_repoint_test.go
// version: 1.0.0
// guid: b6d0f39c-4a17-4e82-95c1-70fe2a8b31d4
// last-edited: 2026-08-20

package maintenance

import (
	"context"
	"os"
	"path/filepath"
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
	full    map[string][]database.BookFile // bookID → rows
	updates []database.BookFile
	getErr  error
}

func (f *repointFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.cores, nil
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
	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 1, plan.SizeMismatch)
	require.Equal(t, 0, plan.Repointable)
	require.Empty(t, store.updates)

	// ...and the escape hatch works when the operator opts out explicitly.
	off := false
	store2 := seedRepoint(t, filepath.Join(dir, "Stem - 2", "35.mp3"), 999)
	plan2, err := planMissingFileRepoint(context.Background(), store2,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
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

	plan, err := planMissingFileRepoint(context.Background(), store,
		missingFileRepointParams{Apply: true, Max: 2}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 3, plan.Repointable, "repointable counts the FULL set, not the capped slice")
	require.Equal(t, 2, plan.CappedAt)
	require.Len(t, store.updates, 2)
	// Sorted by file ID, so the cap takes f1 and f2 — never f3.
	got := []string{store.updates[0].ID, store.updates[1].ID}
	require.ElementsMatch(t, []string{"f1", "f2"}, got, "cap must take a stable prefix by ID")
}
