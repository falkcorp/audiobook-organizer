// file: internal/maintenance/jobs/backfill_file_hashes_missing_test.go
// version: 1.0.0
// guid: 5e1c7a42-93b8-4d06-a7f1-2c84e0b9d315
// last-edited: 2026-10-25

package jobs_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hbCounts is the parsed "hashed=1 skipped_known_missing=2 ..." tally the job
// logs as its last line and stores on the operation log.
type hbCounts struct{ hashed, skipped, newlyMissing, readErrors, writeErrors int }

var hbCountRe = regexp.MustCompile(`hashed=(\d+) skipped_known_missing=(\d+) newly_missing=(\d+) read_errors=(\d+) write_errors=(\d+)`)

// hbSummaryCounts finds the job's closing summary line among the reporter's logs.
func hbSummaryCounts(t *testing.T, rep *noopReporter) hbCounts {
	t.Helper()
	for i := len(rep.logs) - 1; i >= 0; i-- {
		m := hbCountRe.FindStringSubmatch(rep.logs[i])
		if m == nil {
			continue
		}
		n := func(s string) int { v, err := strconv.Atoi(s); require.NoError(t, err); return v }
		return hbCounts{n(m[1]), n(m[2]), n(m[3]), n(m[4]), n(m[5])}
	}
	t.Fatalf("no summary line with counts in logs: %q", rep.logs)
	return hbCounts{}
}

// hbRows is an in-memory book_file table behind a MockStore: ModifyBookFile runs
// the callback on a copy and stores the result, exactly as the real store does
// under the row lock, so a test can run the job repeatedly and watch the state
// it left for the next run.
type hbRows struct {
	mu          sync.Mutex
	rows        map[string]*database.BookFile
	modifyCalls int
	hashCalls   []string
}

func newHBRows(rows ...database.BookFile) *hbRows {
	h := &hbRows{rows: map[string]*database.BookFile{}}
	for i := range rows {
		r := rows[i]
		h.rows[r.ID] = &r
	}
	return h
}

func (h *hbRows) store() *database.MockStore {
	return &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			ids := make([]string, 0, len(h.rows))
			for id := range h.rows {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			out := make([]database.BookFileCore, 0, len(ids))
			for _, id := range ids {
				out = append(out, h.rows[id].Core())
			}
			return out, nil
		},
		SetBookFileHashFunc: func(id, hash string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.hashCalls = append(h.hashCalls, id)
			h.rows[id].FileHash = hash
			return nil
		},
		ModifyBookFileFunc: func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.modifyCalls++
			stored := h.rows[fileID]
			if stored == nil {
				return nil, nil
			}
			row := *stored
			if err := fn(&row); err != nil {
				if err == database.ErrSkipBookFileWrite {
					return stored, nil
				}
				return nil, err
			}
			*stored = row
			return &row, nil
		},
	}
}

func (h *hbRows) get(id string) database.BookFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.rows[id]
}

func runHashBackfill(t *testing.T, store maintenance.JobStore, dryRun bool) (*noopReporter, hbCounts) {
	t.Helper()
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, rep, dryRun))
	return rep, hbSummaryCounts(t, rep)
}

func hbSum(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

// A file that does not exist is flagged on its row (the existing Missing flag),
// not hashed, not deleted, and counted as newly_missing.
func TestBackfillFileHashes_NotFoundMarksRowMissing(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "gone.m4b")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: absent})

	rep, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{newlyMissing: 1}, c)
	got := h.get("f1")
	assert.True(t, got.Missing, "a not-found file must be recorded on the row")
	assert.Empty(t, got.FileHash)
	assert.Equal(t, absent, got.FilePath, "the path is never touched")
	assert.Empty(t, h.hashCalls, "nothing to store a hash for")
	assert.Len(t, h.rows, 1, "the row must never be deleted")
	assert.NotEmpty(t, rep.logs, "a newly missing file is logged (capped sample)")
}

// ENOTDIR (a path component is a regular file) is also "not there".
func TestBackfillFileHashes_NotADirectoryMarksRowMissing(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: filepath.Join(blocker, "child.m4b")})

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{newlyMissing: 1}, c)
	assert.True(t, h.get("f1").Missing)
}

// A row already marked missing whose file is still gone is skipped: no hash
// attempt, no write, no warning, counted as skipped_known_missing.
func TestBackfillFileHashes_KnownMissingSkipped(t *testing.T) {
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", Missing: true, FilePath: filepath.Join(t.TempDir(), "gone.m4b")})

	rep, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{skipped: 1}, c)
	assert.Zero(t, h.modifyCalls, "a skipped row is not rewritten")
	assert.Empty(t, h.hashCalls)
	for _, l := range rep.logs {
		assert.NotContains(t, l, "Hash computation failed", "a known-missing file must not warn again")
	}
	assert.True(t, h.get("f1").Missing)
}

// The whole point end to end across runs: run 1 flags the absent file, run 2
// skips it (no rewrite), and a present file is hashed in both.
func TestBackfillFileHashes_SecondRunSkipsWhatFirstRunFlagged(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.m4b")
	require.NoError(t, os.WriteFile(present, []byte("bytes here"), 0o644))
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: present},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "gone.m4b")},
	)

	_, c1 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{hashed: 1, newlyMissing: 1}, c1)
	assert.Equal(t, hbSum("bytes here"), h.get("f1").FileHash)
	callsAfterFirst := h.modifyCalls

	_, c2 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{skipped: 1}, c2, "run 2: the hashed row is done, the missing one is skipped")
	assert.Equal(t, callsAfterFirst, h.modifyCalls, "run 2 writes nothing")
}

// A non-not-found failure is a read error: counted, logged, the row is NOT
// marked missing, and it is retried on the next run. A directory in place of a
// file opens fine and fails on read (EISDIR), which is such an error.
func TestBackfillFileHashes_ReadErrorRetriedNotMarked(t *testing.T) {
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: t.TempDir()})

	rep, c1 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{readErrors: 1}, c1)
	assert.False(t, h.get("f1").Missing, "an unreadable file is not a missing file")
	assert.Zero(t, h.modifyCalls)
	warned := false
	for _, l := range rep.logs {
		warned = warned || strings.Contains(l, "Hash computation failed")
	}
	assert.True(t, warned, "read errors keep their per-file warning")

	_, c2 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{readErrors: 1}, c2, "and it is retried every run")
}

// A missing file that comes back is hashed on the next run and its flag cleared.
func TestBackfillFileHashes_ReappearedFileHashedAndFlagCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "back.m4b")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: path})

	_, c1 := runHashBackfill(t, h.store(), false)
	require.Equal(t, hbCounts{newlyMissing: 1}, c1)
	_, c2 := runHashBackfill(t, h.store(), false)
	require.Equal(t, hbCounts{skipped: 1}, c2)

	require.NoError(t, os.WriteFile(path, []byte("restored bytes"), 0o644))
	_, c3 := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{hashed: 1}, c3)
	got := h.get("f1")
	assert.Equal(t, hbSum("restored bytes"), got.FileHash)
	assert.False(t, got.Missing, "a hashed file is present: the marker is cleared")
}

// Dry run reports what an apply would do and writes nothing, including the flag.
func TestBackfillFileHashes_DryRunWritesNoMarker(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.m4b")
	require.NoError(t, os.WriteFile(present, []byte("bytes"), 0o644))
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: present},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "gone.m4b")},
		database.BookFile{ID: "f3", BookID: "b1", Missing: true, FilePath: filepath.Join(dir, "gone2.m4b")},
	)

	rep, c := runHashBackfill(t, h.store(), true)

	assert.Equal(t, hbCounts{hashed: 1, skipped: 1, newlyMissing: 1}, c)
	assert.Zero(t, h.modifyCalls, "dry run: no marker writes")
	assert.Empty(t, h.hashCalls, "dry run: no hash writes")
	assert.False(t, h.get("f2").Missing)
	assert.Contains(t, rep.logs[len(rep.logs)-1], "Dry run")
}

// The marker write re-checks the row under its lock: a row that was repointed
// (or hashed) between this run's read and the write is not flagged for the old
// path's absence.
func TestBackfillFileHashes_RowChangedUnderRunIsNotFlagged(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "old-path.m4b")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: stale})
	store := h.store()
	// Repoint the stored row right after the job's snapshot is taken.
	snapshot := store.GetAllBookFilesCoreFunc
	store.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		rows, err := snapshot()
		h.mu.Lock()
		h.rows["f1"].FilePath = filepath.Join(dir, "new-path.m4b")
		h.mu.Unlock()
		return rows, err
	}

	_, c := runHashBackfill(t, store, false)

	assert.Equal(t, hbCounts{}, c, "a row that moved is neither counted nor flagged")
	assert.False(t, h.get("f1").Missing)
}

// Real store, three runs: the marker the job writes survives the store's write
// path (memdb projection, merge rule) and is what the next run reads; the row
// count never changes.
func TestBackfillFileHashes_PebbleStoreRoundTrip(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	dir := t.TempDir()
	book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: dir})
	require.NoError(t, err)
	present := filepath.Join(dir, "present.m4b")
	require.NoError(t, os.WriteFile(present, []byte("present bytes"), 0o644))
	back := filepath.Join(dir, "back.m4b")
	for _, p := range []string{present, back, filepath.Join(dir, "never.m4b")} {
		require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: p}))
	}
	byPath := func() map[string]database.BookFile {
		files, err := ps.GetBookFiles(book.ID)
		require.NoError(t, err)
		m := map[string]database.BookFile{}
		for _, f := range files {
			m[filepath.Base(f.FilePath)] = f
		}
		return m
	}

	_, c1 := runHashBackfill(t, ps, false)
	assert.Equal(t, hbCounts{hashed: 1, newlyMissing: 2}, c1)
	rows := byPath()
	require.Len(t, rows, 3, "no row is ever deleted")
	assert.Equal(t, hbSum("present bytes"), rows["present.m4b"].FileHash)
	assert.False(t, rows["present.m4b"].Missing)
	assert.True(t, rows["back.m4b"].Missing)
	assert.True(t, rows["never.m4b"].Missing)

	_, c2 := runHashBackfill(t, ps, false)
	assert.Equal(t, hbCounts{skipped: 2}, c2)

	require.NoError(t, os.WriteFile(back, []byte("back bytes"), 0o644))
	_, c3 := runHashBackfill(t, ps, false)
	assert.Equal(t, hbCounts{hashed: 1, skipped: 1}, c3)
	rows = byPath()
	require.Len(t, rows, 3)
	assert.Equal(t, hbSum("back bytes"), rows["back.m4b"].FileHash)
	assert.False(t, rows["back.m4b"].Missing, "reappeared + hashed: marker cleared in the real store")
	assert.True(t, rows["never.m4b"].Missing)
}
