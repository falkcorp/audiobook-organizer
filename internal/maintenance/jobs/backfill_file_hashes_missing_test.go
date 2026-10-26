// file: internal/maintenance/jobs/backfill_file_hashes_missing_test.go
// version: 2.1.0
// guid: 5e1c7a42-93b8-4d06-a7f1-2c84e0b9d315
// last-edited: 2026-10-25

package jobs_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hbCounts is the parsed "hashed=1 skipped_known_missing=2 ..." tally the job
// logs as its last line and stores on the operation log, zero entries dropped so
// a test names only what it expects.
type hbCounts map[string]int

var (
	hbPairRe   = regexp.MustCompile(`(\w+)=(\d+)`)
	hbReportRe = regexp.MustCompile(`report=(\S+)`)
)

// hbOutcomeKeys are the nine row-outcome counters; every un-hashed row the run
// reaches lands in exactly one of them.
var hbOutcomeKeys = []string{
	"hashed", "skipped_known_missing", "newly_missing", "read_errors", "write_errors",
	"skipped_no_path", "unverified_absent", "skipped_changed", "marker_errors",
}

func (c hbCounts) outcomes() int {
	n := 0
	for _, k := range hbOutcomeKeys {
		n += c[k]
	}
	return n
}

// hbSummary finds the job's closing summary line among the reporter's logs.
func hbSummary(t *testing.T, rep *noopReporter) (hbCounts, string) {
	t.Helper()
	for i := len(rep.logs) - 1; i >= 0; i-- {
		if !strings.Contains(rep.logs[i], "hashed=") {
			continue
		}
		c := hbCounts{}
		for _, m := range hbPairRe.FindAllStringSubmatch(rep.logs[i], -1) {
			v, err := strconv.Atoi(m[2])
			require.NoError(t, err)
			if v != 0 {
				c[m[1]] = v
			}
		}
		report := ""
		if m := hbReportRe.FindStringSubmatch(rep.logs[i]); m != nil {
			report = m[1]
		}
		return c, report
	}
	t.Fatalf("no summary line with counts in logs: %q", rep.logs)
	return nil, ""
}

// hbLibrary makes a populated library root, points config at it for the test,
// and returns it. The Gotham directory keeps the mount check satisfied and is
// where the fixtures' files live.
func hbLibrary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	return root
}

func hbPath(root, name string) string { return filepath.Join(root, "Gotham", name) }

func hbWrite(t *testing.T, root, name, content string) string {
	t.Helper()
	p := hbPath(root, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

type hbBatch struct {
	bookID string
	ids    []string
}

// hbRows is an in-memory book_file table behind a MockStore. Its batch writer
// follows the real store's contract: each row is read and rewritten under one
// lock, the callback sees a copy, a failing row is skipped and its error joined,
// and a recompute error is joined last when anything was written.
type hbRows struct {
	mu           sync.Mutex
	rows         map[string]*database.BookFile
	modifyCalls  int // single-row ModifyBookFile calls (the per-row recompute path)
	batches      []hbBatch
	hashCalls    []string
	failWrite    func(id string, before *database.BookFile, after *database.BookFile) error
	recomputeErr error
	state        map[string][]byte
	phases       []string
	activeOps    []database.OperationV2Row
}

func newHBRows(rows ...database.BookFile) *hbRows {
	h := &hbRows{rows: map[string]*database.BookFile{}, state: map[string][]byte{}}
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
				if errors.Is(err, database.ErrSkipBookFileWrite) {
					return stored, nil
				}
				return nil, err
			}
			*stored = row
			return &row, nil
		},
		ModifyBookFilesFunc: func(ctx context.Context, bookID string, ids []string, fn func(*database.BookFile) error) ([]string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.batches = append(h.batches, hbBatch{bookID, append([]string(nil), ids...)})
			var (
				changed []string
				errs    []error
			)
			for _, id := range ids {
				stored := h.rows[id]
				if stored == nil || stored.BookID != bookID {
					continue
				}
				row := *stored
				if err := fn(&row); err != nil {
					if !errors.Is(err, database.ErrSkipBookFileWrite) {
						errs = append(errs, fmt.Errorf("book file %s: %w", id, err))
					}
					continue
				}
				if h.failWrite != nil {
					if err := h.failWrite(id, stored, &row); err != nil {
						errs = append(errs, fmt.Errorf("book file %s: write: %w", id, err))
						continue
					}
				}
				*stored = row
				changed = append(changed, id)
			}
			if len(changed) > 0 && h.recomputeErr != nil {
				errs = append(errs, fmt.Errorf("%w for book %s: %w", database.ErrBookAggregatesRecompute, bookID, h.recomputeErr))
			}
			return changed, errors.Join(errs...)
		},
		SaveOperationStateFunc: func(opID string, data []byte) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			var st operations.OperationState
			if err := json.Unmarshal(data, &st); err == nil {
				h.phases = append(h.phases, fmt.Sprintf("%s@%d", st.Phase, st.PhaseIndex))
			}
			h.state[opID] = data
			return nil
		},
		GetOperationStateFunc: func(opID string) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.state[opID], nil
		},
		DeleteOperationStateFunc: func(opID string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.state, opID)
			return nil
		},
		ListActiveOperationsV2Func: func() ([]database.OperationV2Row, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]database.OperationV2Row(nil), h.activeOps...), nil
		},
	}
}

func (h *hbRows) get(id string) database.BookFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.rows[id]
}

func (h *hbRows) batchCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.batches)
}

// hbRun is one Run's options.
type hbRun struct {
	dryRun bool
	params string // raw JSON params, "" for none
	opID   string
}

func (r hbRun) ctx() context.Context {
	ctx := context.Background()
	if r.params != "" {
		ctx = maintenance.WithRawParams(ctx, json.RawMessage(r.params))
	}
	if r.opID != "" {
		ctx = maintenance.WithOperationID(ctx, r.opID)
	}
	return ctx
}

func hbRunJob(t *testing.T, store maintenance.JobStore, opts hbRun) (*noopReporter, error) {
	t.Helper()
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	rep := &noopReporter{}
	return rep, j.Run(opts.ctx(), store, rep, opts.dryRun)
}

// runHashBackfill runs the job and returns its parsed summary and report path.
func runHashBackfill(t *testing.T, store maintenance.JobStore, dryRun bool) (*noopReporter, hbCounts) {
	t.Helper()
	rep, c, _ := runHB(t, store, hbRun{dryRun: dryRun})
	return rep, c
}

func runHB(t *testing.T, store maintenance.JobStore, opts hbRun) (*noopReporter, hbCounts, string) {
	t.Helper()
	rep, err := hbRunJob(t, store, opts)
	require.NoError(t, err)
	c, report := hbSummary(t, rep)
	return rep, c, report
}

// hbReportRow is one data line of the TSV report.
type hbReportRow struct{ bucket, fileID, bookID, path, reason string }

func hbReadReport(t *testing.T, path string) []hbReportRow {
	t.Helper()
	require.NotEmpty(t, path, "the run must name its report")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	require.Equal(t, "bucket\tfile_id\tbook_id\tpath\treason", lines[0])
	var out []hbReportRow
	for _, l := range lines[1:] {
		f := strings.Split(l, "\t")
		require.Len(t, f, 5, "line %q", l)
		out = append(out, hbReportRow{f[0], f[1], f[2], f[3], f[4]})
	}
	return out
}

func hbBuckets(rows []hbReportRow) map[string][]string {
	m := map[string][]string{}
	for _, r := range rows {
		m[r.bucket] = append(m[r.bucket], r.fileID)
	}
	for _, v := range m {
		sort.Strings(v)
	}
	return m
}

func hbSum(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

// A file that does not exist is recorded on its row in hash_absent_at (and NOT
// in Missing, which other readers own), not hashed, not deleted, and counted as
// newly_missing and listed in the report.
func TestBackfillFileHashes_NotFoundRecordsHashAbsentNotMissing(t *testing.T) {
	root := hbLibrary(t)
	absent := hbPath(root, "Batman Year One 01.m4b")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: absent})

	rep, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"newly_missing": 1}, c)
	got := h.get("f1")
	require.NotNil(t, got.HashAbsentAt, "a not-found file must be recorded on the row")
	assert.WithinDuration(t, time.Now(), *got.HashAbsentAt, time.Minute)
	assert.False(t, got.Missing, "Missing belongs to the scanner, mark-missing and the election code; this job never sets it")
	assert.Empty(t, got.FileHash)
	assert.Equal(t, absent, got.FilePath, "the path is never touched")
	assert.Empty(t, h.hashCalls, "nothing to store a hash for")
	assert.Len(t, h.rows, 1, "the row must never be deleted")
	assert.NotEmpty(t, rep.logs)
	assert.Equal(t, map[string][]string{"marked-absent": {"f1"}}, hbBuckets(hbReadReport(t, report)))
}

// ENOTDIR (a path component is a regular file) is also "not there".
func TestBackfillFileHashes_NotADirectoryRecordsHashAbsent(t *testing.T) {
	root := hbLibrary(t)
	blocker := hbWrite(t, root, "blocker", "x")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: filepath.Join(blocker, "child.m4b")})

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"newly_missing": 1}, c)
	assert.NotNil(t, h.get("f1").HashAbsentAt)
}

// A row already known absent whose file is still gone is skipped: no hash
// attempt, no write, no warning, counted as skipped_known_missing. Both ways a
// row can be known count: this job's own marker, and the Missing flag the
// scanner owns (which this job only READS).
func TestBackfillFileHashes_KnownAbsentSkipped(t *testing.T) {
	root := hbLibrary(t)
	when := time.Now().Add(-time.Hour)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", HashAbsentAt: &when, FilePath: hbPath(root, "The Long Halloween 01.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", Missing: true, FilePath: hbPath(root, "The Long Halloween 02.m4b")},
	)

	rep, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"skipped_known_missing": 2}, c)
	assert.Zero(t, h.modifyCalls)
	assert.Zero(t, h.batchCount(), "a skipped row is not rewritten")
	assert.Empty(t, h.hashCalls)
	for _, l := range rep.logs {
		assert.NotContains(t, l, "Hash computation failed", "a known-absent file must not warn again")
		assert.NotContains(t, l, "File not found")
	}
	assert.True(t, h.get("f2").Missing)
	assert.Nil(t, h.get("f2").HashAbsentAt, "a Missing row is not given a marker as well")
	assert.WithinDuration(t, when, *h.get("f1").HashAbsentAt, time.Second, "the marker keeps its first-seen time")
}

// The whole point end to end across runs: run 1 records the absent file, run 2
// skips it (no rewrite), and a present file is hashed in run 1 only.
func TestBackfillFileHashes_SecondRunSkipsWhatFirstRunRecorded(t *testing.T) {
	root := hbLibrary(t)
	present := hbWrite(t, root, "Hush 01.m4b", "bytes here")
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: present},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush 02.m4b")},
	)

	_, c1 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{"hashed": 1, "newly_missing": 1}, c1)
	assert.Equal(t, hbSum("bytes here"), h.get("f1").FileHash)
	batchesAfterFirst := h.batchCount()

	_, c2 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{"skipped_known_missing": 1}, c2, "run 2: the hashed row is done, the absent one is skipped")
	assert.Equal(t, batchesAfterFirst, h.batchCount(), "run 2 writes nothing")
}

// A non-not-found failure is a read error: counted, logged, listed in the
// report, the row is NOT recorded absent, and it is retried on the next run. A
// directory in place of a file opens fine and fails on read (EISDIR).
func TestBackfillFileHashes_ReadErrorRetriedNotRecorded(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham")})

	rep, c1, report := runHB(t, h.store(), hbRun{})
	assert.Equal(t, hbCounts{"read_errors": 1}, c1)
	assert.Nil(t, h.get("f1").HashAbsentAt, "an unreadable file is not an absent file")
	assert.Zero(t, h.batchCount())
	warned := false
	for _, l := range rep.logs {
		warned = warned || strings.Contains(l, "Hash computation failed")
	}
	assert.True(t, warned, "read errors keep their per-file warning")
	assert.Equal(t, map[string][]string{"read-error": {"f1"}}, hbBuckets(hbReadReport(t, report)))

	_, c2 := runHashBackfill(t, h.store(), false)
	assert.Equal(t, hbCounts{"read_errors": 1}, c2, "and it is retried every run")
}

// A file recorded absent that comes back is hashed on the next run and its
// marker cleared, in one batch.
func TestBackfillFileHashes_ReappearedFileHashedAndMarkerCleared(t *testing.T) {
	root := hbLibrary(t)
	path := hbPath(root, "Knightfall 01.m4b")
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: path})

	_, c1 := runHashBackfill(t, h.store(), false)
	require.Equal(t, hbCounts{"newly_missing": 1}, c1)
	_, c2 := runHashBackfill(t, h.store(), false)
	require.Equal(t, hbCounts{"skipped_known_missing": 1}, c2)

	require.NoError(t, os.WriteFile(path, []byte("restored bytes"), 0o644))
	_, c3, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1, "markers_cleared": 1}, c3)
	got := h.get("f1")
	assert.Equal(t, hbSum("restored bytes"), got.FileHash)
	assert.Nil(t, got.HashAbsentAt, "a hashed file is present: the marker is cleared")
	assert.Equal(t, map[string][]string{"cleared-marker": {"f1"}}, hbBuckets(hbReadReport(t, report)))
	assert.Zero(t, h.modifyCalls, "the clear is a batch, not a per-row ModifyBookFile")
}

// Missing is READ-ONLY here: a Missing row whose file is back is hashed, but the
// flag stays for the scanner to clear.
func TestBackfillFileHashes_MissingFlagIsReadOnly(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", Missing: true, FilePath: hbWrite(t, root, "Arkham Asylum 01.m4b", "back")})

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"hashed": 1}, c)
	assert.Equal(t, hbSum("back"), h.get("f1").FileHash)
	assert.True(t, h.get("f1").Missing, "this job never clears Missing")
	assert.Zero(t, h.batchCount())
}

// Dry run reports what an apply would do and writes nothing, the report
// included in the would-* spelling.
func TestBackfillFileHashes_DryRunWritesNothingAndReports(t *testing.T) {
	root := hbLibrary(t)
	when := time.Now().Add(-time.Hour)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Hush 01.m4b", "bytes")},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush 02.m4b")},
		database.BookFile{ID: "f3", BookID: "b1", Missing: true, FilePath: hbPath(root, "Hush 03.m4b")},
		database.BookFile{ID: "f4", BookID: "b1", HashAbsentAt: &when, FilePath: hbWrite(t, root, "Hush 04.m4b", "back")},
	)

	rep, c, report := runHB(t, h.store(), hbRun{dryRun: true})

	assert.Equal(t, hbCounts{"hashed": 2, "skipped_known_missing": 1, "newly_missing": 1, "markers_cleared": 1}, c)
	assert.Zero(t, h.batchCount(), "dry run: no marker writes")
	assert.Zero(t, h.modifyCalls)
	assert.Empty(t, h.hashCalls, "dry run: no hash writes")
	assert.Nil(t, h.get("f2").HashAbsentAt)
	assert.NotNil(t, h.get("f4").HashAbsentAt)
	assert.Contains(t, rep.logs[len(rep.logs)-1], "Dry run")
	assert.Equal(t, map[string][]string{"would-mark-absent": {"f2"}, "would-clear-marker": {"f4"}}, hbBuckets(hbReadReport(t, report)),
		"the preview is reviewable before anything is written")
}

// The marker write re-checks the row under its lock: a row repointed between
// this run's read and the write is not recorded absent for the old path, and
// that is counted and reported as skipped_changed, not dropped.
func TestBackfillFileHashes_RowChangedUnderRunIsNotRecorded(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "old-path.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "also-absent.m4b")},
	)
	store := h.store()
	snapshot := store.GetAllBookFilesCoreFunc
	store.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		rows, err := snapshot()
		h.mu.Lock()
		h.rows["f1"].FilePath = hbPath(root, "new-path.m4b") // repointed after the snapshot
		h.mu.Unlock()
		return rows, err
	}

	_, c, report := runHB(t, store, hbRun{})

	assert.Equal(t, hbCounts{"newly_missing": 1, "skipped_changed": 1}, c)
	assert.Nil(t, h.get("f1").HashAbsentAt)
	assert.NotNil(t, h.get("f2").HashAbsentAt)
	assert.Equal(t, map[string][]string{"marked-absent": {"f2"}, "skipped-changed": {"f1"}}, hbBuckets(hbReadReport(t, report)))
}

// Real store, three runs: the marker the job writes survives the store's write
// path (memdb projection, merge rule) and is what the next run reads, Missing is
// never set, and the row count never changes.
func TestBackfillFileHashes_PebbleStoreRoundTrip(t *testing.T) {
	root := hbLibrary(t)
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	book, err := ps.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: filepath.Join(root, "Gotham")})
	require.NoError(t, err)
	present := hbWrite(t, root, "present.m4b", "present bytes")
	back := hbPath(root, "back.m4b")
	for _, p := range []string{present, back, hbPath(root, "never.m4b")} {
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
	assert.Equal(t, hbCounts{"hashed": 1, "newly_missing": 2}, c1)
	rows := byPath()
	require.Len(t, rows, 3, "no row is ever deleted")
	assert.Equal(t, hbSum("present bytes"), rows["present.m4b"].FileHash)
	assert.Nil(t, rows["present.m4b"].HashAbsentAt)
	assert.NotNil(t, rows["back.m4b"].HashAbsentAt)
	assert.NotNil(t, rows["never.m4b"].HashAbsentAt)
	for name, r := range rows {
		assert.False(t, r.Missing, "%s: the job must not touch Missing", name)
	}

	_, c2 := runHashBackfill(t, ps, false)
	assert.Equal(t, hbCounts{"skipped_known_missing": 2}, c2)

	require.NoError(t, os.WriteFile(back, []byte("back bytes"), 0o644))
	_, c3 := runHashBackfill(t, ps, false)
	assert.Equal(t, hbCounts{"hashed": 1, "skipped_known_missing": 1, "markers_cleared": 1}, c3)
	rows = byPath()
	require.Len(t, rows, 3)
	assert.Equal(t, hbSum("back bytes"), rows["back.m4b"].FileHash)
	assert.Nil(t, rows["back.m4b"].HashAbsentAt, "reappeared + hashed: marker cleared in the real store")
	assert.NotNil(t, rows["never.m4b"].HashAbsentAt)
}

// The cost the marker writes must not have: N absent files of ONE book are one
// aggregate recompute for that book, not N. Real store, real recompute counter;
// the control shows the same N rows through per-row ModifyBookFile would be N
// recomputes, so a 1 here is not a counter that never counts.
func TestBackfillFileHashes_ManyAbsentFilesOfOneBookRecomputeOnce(t *testing.T) {
	root := hbLibrary(t)
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	const n = 20
	book, err := ps.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: filepath.Join(root, "Gotham")})
	require.NoError(t, err)
	other, err := ps.CreateBook(&database.Book{Title: "The Killing Joke", FilePath: filepath.Join(root, "Gotham")})
	require.NoError(t, err)
	for i := range n {
		require.NoError(t, ps.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: hbPath(root, fmt.Sprintf("gone%02d.m4b", i)), Duration: 30, FileSize: 1000,
		}))
	}
	require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: hbPath(root, "other-gone.m4b")}))

	logs := aggtest.Capture(t)
	_, c := runHashBackfill(t, ps, false)

	assert.Equal(t, hbCounts{"newly_missing": n + 1}, c)
	assert.Equal(t, 1, aggtest.CountInvocations(logs(), book.ID), "20 markers of one book: one recompute")
	assert.Equal(t, 1, aggtest.CountInvocations(logs(), other.ID))
	files, err := ps.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, n)
	for _, f := range files {
		assert.NotNil(t, f.HashAbsentAt)
	}

	third, err := ps.CreateBook(&database.Book{Title: "Hush", FilePath: filepath.Join(root, "Gotham")})
	require.NoError(t, err)
	for i := range 5 {
		require.NoError(t, ps.CreateBookFile(&database.BookFile{BookID: third.ID, FilePath: hbPath(root, fmt.Sprintf("third%d.m4b", i))}))
	}
	tf, err := ps.GetBookFiles(third.ID)
	require.NoError(t, err)
	logs = aggtest.Capture(t)
	for _, f := range tf {
		_, err := ps.ModifyBookFile(third.ID, f.ID, func(r *database.BookFile) error { r.Missing = true; return nil })
		require.NoError(t, err)
	}
	assert.Equal(t, len(tf), aggtest.CountInvocations(logs(), third.ID), "control: the per-row path recomputes once per row")
}

// hbBigLibrary builds rows of three books across more than one chunk, in an
// order that interleaves the books, all absent. It returns the rows and, per
// book, how many chunks the (book, file)-sorted order touches it in, which is
// how many batches it must take.
func hbBigLibrary(t *testing.T, root string) (*hbRows, map[string]int) {
	t.Helper()
	const total = 2*jobs.HashBackfillChunkSize + 100
	h := newHBRows()
	bookOf := func(i int) string {
		switch m := i % 11; {
		case m <= 3:
			return "bookA"
		case m <= 8:
			return "bookB"
		default:
			return "bookC"
		}
	}
	type row struct{ book, id string }
	var order []row
	for i := range total {
		id := fmt.Sprintf("f%05d", i)
		h.rows[id] = &database.BookFile{ID: id, BookID: bookOf(i), FilePath: hbPath(root, id+".m4b")}
		order = append(order, row{bookOf(i), id})
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].book != order[j].book {
			return order[i].book < order[j].book
		}
		return order[i].id < order[j].id
	})
	chunksOf := map[string]map[int]bool{}
	for pos, r := range order {
		if chunksOf[r.book] == nil {
			chunksOf[r.book] = map[int]bool{}
		}
		chunksOf[r.book][pos/jobs.HashBackfillChunkSize] = true
	}
	want := map[string]int{}
	for b, c := range chunksOf {
		want[b] = len(c)
	}
	return h, want
}

// A run bigger than one chunk, with the store answering interleaved by book:
// every row is recorded, and each book is written in exactly as many batches as
// the (book, file) order puts chunk boundaries through it. Without the job's own
// sort the chunks would each hold all three books and the counts would be higher,
// so this fails if the sort is removed (mutation-checked).
func TestBackfillFileHashes_ChunkBoundaryBatchesFollowTheBookOrder(t *testing.T) {
	root := hbLibrary(t)
	h, wantBatches := hbBigLibrary(t, root)
	require.Greater(t, wantBatches["bookA"]+wantBatches["bookB"]+wantBatches["bookC"], 3, "the fixture must put a boundary through a book")

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"newly_missing": len(h.rows)}, c)
	got := map[string]int{}
	for _, b := range h.batches {
		got[b.bookID]++
		assert.True(t, sort.StringsAreSorted(b.ids), "a batch is in file-ID order: %v", b.ids[:min(3, len(b.ids))])
	}
	assert.Equal(t, wantBatches, got)
	for id, r := range h.rows {
		assert.NotNil(t, r.HashAbsentAt, id)
	}
}

// Checkpoints are written at chunk boundaries under the new phase name.
func TestBackfillFileHashes_CheckpointPhaseAndIndex(t *testing.T) {
	root := hbLibrary(t)
	h, _ := hbBigLibrary(t, root)

	_, _, _ = runHB(t, h.store(), hbRun{opID: "op-batman-1"})

	assert.Equal(t, []string{
		fmt.Sprintf("%s@%d", jobs.HashBackfillPhase, jobs.HashBackfillChunkSize),
		fmt.Sprintf("%s@%d", jobs.HashBackfillPhase, 2*jobs.HashBackfillChunkSize),
		fmt.Sprintf("%s@%d", jobs.HashBackfillPhase, len(h.rows)),
	}, h.phases)
	assert.Equal(t, "scanning-by-book", jobs.HashBackfillPhase)
	assert.Empty(t, h.state, "state is cleared on clean completion")
}

func hbSortedIDs(h *hbRows) []string {
	var ids []database.BookFileCore
	for _, r := range h.rows {
		ids = append(ids, r.Core())
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].BookID != ids[j].BookID {
			return ids[i].BookID < ids[j].BookID
		}
		return ids[i].ID < ids[j].ID
	})
	out := make([]string, len(ids))
	for i, r := range ids {
		out[i] = r.ID
	}
	return out
}

// Resume: a checkpoint at the first chunk boundary under the current phase
// starts the run after those rows (which an earlier part of the run handled),
// and the report of the earlier part is appended to, not truncated.
func TestBackfillFileHashes_ResumesFromACheckpoint(t *testing.T) {
	root := hbLibrary(t)
	h, _ := hbBigLibrary(t, root)
	store := h.store()
	sorted := hbSortedIDs(h)
	const opID = "op-batman-2"
	require.NoError(t, operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", jobs.HashBackfillPhase, jobs.HashBackfillChunkSize, len(sorted)))

	reportDir := filepath.Join(root, ".reports")
	require.NoError(t, os.MkdirAll(reportDir, 0o775))
	earlier := "bucket\tfile_id\tbook_id\tpath\treason\nmarked-absent\tEARLIER\tb\t/lib/Gotham/x.m4b\tfrom the first part of the run\n"
	reportFile := filepath.Join(reportDir, "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, os.WriteFile(reportFile, []byte(earlier), 0o664))

	_, c, report := runHB(t, store, hbRun{opID: opID})

	assert.Equal(t, reportFile, report)
	assert.Equal(t, hbCounts{"newly_missing": len(sorted) - jobs.HashBackfillChunkSize}, c)
	for i, id := range sorted {
		if i < jobs.HashBackfillChunkSize {
			assert.Nil(t, h.rows[id].HashAbsentAt, "row %d is below the checkpoint and was not re-processed", i)
		} else {
			assert.NotNil(t, h.rows[id].HashAbsentAt, "row %d is above the checkpoint", i)
		}
	}
	b, err := os.ReadFile(reportFile)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), earlier), "the earlier part of the report survives a resume")
	assert.Equal(t, 1, strings.Count(string(b), "bucket\tfile_id"), "no second header")
}

// A checkpoint from before the order changed (phase "scanning") indexes a
// different set of rows and must be ignored, not resumed from.
func TestBackfillFileHashes_OldPhaseCheckpointIgnored(t *testing.T) {
	root := hbLibrary(t)
	h, _ := hbBigLibrary(t, root)
	store := h.store()
	const opID = "op-batman-3"
	require.NoError(t, operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "scanning", jobs.HashBackfillChunkSize, len(h.rows)))

	_, c, _ := runHB(t, store, hbRun{opID: opID})

	assert.Equal(t, hbCounts{"newly_missing": len(h.rows)}, c, "every row is processed from index 0")
}

// A row with no path is neither statted nor hashed: counted, reported, and not
// recorded as an absent file (there is no file to be absent).
func TestBackfillFileHashes_EmptyPathSkipped(t *testing.T) {
	hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: ""},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: "   "},
	)

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"skipped_no_path": 2}, c)
	assert.Zero(t, h.batchCount())
	assert.Nil(t, h.get("f1").HashAbsentAt)
	assert.Nil(t, h.get("f2").HashAbsentAt)
	assert.Equal(t, map[string][]string{"no-path": {"f1", "f2"}}, hbBuckets(hbReadReport(t, report)))
}

// An unset library root: the run refuses before it reads, writes or reports
// anything. With mark_absent=false there is nothing to record, so it runs.
func TestBackfillFileHashes_RefusesWithoutALibraryRoot(t *testing.T) {
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: filepath.Join(t.TempDir(), "gone.m4b")})

	for _, dry := range []bool{false, true} {
		_, err := hbRunJob(t, h.store(), hbRun{dryRun: dry})
		require.Error(t, err, "dry=%v", dry)
		assert.Contains(t, err.Error(), "library root")
	}
	assert.Zero(t, h.batchCount())
	assert.Nil(t, h.get("f1").HashAbsentAt)

	_, c, _ := runHB(t, h.store(), hbRun{params: `{"mark_absent":false}`})
	assert.Equal(t, hbCounts{"newly_missing": 1}, c, "hash-only: counted, not recorded")
	assert.Nil(t, h.get("f1").HashAbsentAt)
}

// An empty root directory is an unmounted share: every path under it is
// not-found and none of that is evidence. Refused, nothing recorded, and no
// report directory created in it (creating one would make the empty root look
// mounted to the next check).
func TestBackfillFileHashes_RefusesAnUnmountedRoot(t *testing.T) {
	root := t.TempDir() // empty
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "gone.m4b")})

	for _, dry := range []bool{false, true} {
		_, err := hbRunJob(t, h.store(), hbRun{dryRun: dry})
		require.Error(t, err, "dry=%v", dry)
		assert.Contains(t, err.Error(), "no library content")
	}
	assert.Zero(t, h.batchCount())
	assert.Nil(t, h.get("f1").HashAbsentAt)
	ents, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, ents, "the refusal must not create .reports in the unmounted root")
}

// A flush that is cancelled leaves rows whose marker write never ran. They are
// not "changed since read" (the row did not change), so they are neither counted
// nor reported as skipped-changed; the checkpoint was not written, so a resume
// reconsiders them.
func TestBackfillFileHashes_CancelledFlushDoesNotReportSkippedChanged(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone1.m4b")},
		database.BookFile{ID: "f2", BookID: "b2", FilePath: hbPath(root, "gone2.m4b")},
		database.BookFile{ID: "f3", BookID: "b3", FilePath: hbPath(root, "gone3.m4b")},
	)
	store := h.store()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store.ModifyBookFilesFunc = func(ctx context.Context, bookID string, ids []string, fn func(*database.BookFile) error) ([]string, error) {
		cancel() // the store sees a cancelled context and writes nothing
		return nil, context.Canceled
	}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	err = j.Run(ctx, store, &noopReporter{}, false)

	require.ErrorIs(t, err, context.Canceled)
	rows := hbReadReport(t, filepath.Join(root, ".reports", "backfill-file-hashes-unknown-op.tsv"))
	for _, r := range rows {
		assert.NotEqual(t, "skipped-changed", r.bucket, "row %s never ran, it did not change", r.fileID)
	}
	for _, id := range []string{"f1", "f2", "f3"} {
		assert.Nil(t, h.get(id).HashAbsentAt)
	}
}

// clear_markers resolves a report under the root too, so it is refused on an
// unmounted root as well, and creates nothing there. A root holding only
// app-owned dot entries (a report directory from an earlier run, the wav cache)
// is the same bare mountpoint and is refused in every mode.
func TestBackfillFileHashes_ClearMarkersRefusesAnUnmountedRoot(t *testing.T) {
	when := hbMarked(time.Now().Add(-time.Hour))
	for _, tc := range []struct {
		name  string
		dots  []string
		param string
	}{
		{"clear on an empty root", nil, `{"clear_markers":true}`},
		{"clear on a root with only dot entries", []string{".reports", ".wav-cache"}, `{"clear_markers":true}`},
		{"record on a root with only dot entries", []string{".reports", ".wav-cache"}, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range tc.dots {
				require.NoError(t, os.Mkdir(filepath.Join(root, d), 0o755))
			}
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })
			h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", HashAbsentAt: when, FilePath: filepath.Join(root, "Gotham", "gone.m4b")})

			for _, dry := range []bool{false, true} {
				_, err := hbRunJob(t, h.store(), hbRun{dryRun: dry, params: tc.param})
				require.Error(t, err, "dry=%v", dry)
				assert.Contains(t, err.Error(), "no library content")
			}
			assert.Zero(t, h.batchCount())
			assert.NotNil(t, h.get("f1").HashAbsentAt, "nothing was cleared")
			ents, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Len(t, ents, len(tc.dots), "the refusal must create nothing in the bare root")
			if len(tc.dots) > 0 {
				rents, err := os.ReadDir(filepath.Join(root, ".reports"))
				if err == nil {
					assert.Empty(t, rents, "no report file written")
				}
			}
		})
	}
}

// The mount can drop mid-run: the check runs again right before each chunk's
// write, so a chunk of not-founds read after the drop is not recorded.
func TestBackfillFileHashes_MountDroppingMidRunIsNotRecorded(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})
	rep := &dropMountReporter{root: root}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	err = j.Run(context.Background(), h.store(), rep, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to record")
	assert.Zero(t, h.batchCount())
	assert.Nil(t, h.get("f1").HashAbsentAt)
}

// dropMountReporter empties the library root on the first row, the way an
// unmounted share looks from underneath.
type dropMountReporter struct {
	noopReporter
	root string
	once sync.Once
}

func (r *dropMountReporter) Increment() {
	r.once.Do(func() {
		ents, _ := os.ReadDir(r.root)
		for _, e := range ents {
			_ = os.RemoveAll(filepath.Join(r.root, e.Name()))
		}
	})
}

// A not-found outside the library root says nothing about the mount that was
// checked, so it is counted and reported but never recorded.
func TestBackfillFileHashes_AbsentOutsideTheRootIsUnverified(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/Gotham/Elsewhere/The Killing Joke.m4b"},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "inside.m4b")},
	)

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"unverified_absent": 1, "newly_missing": 1}, c)
	assert.Nil(t, h.get("f1").HashAbsentAt)
	assert.NotNil(t, h.get("f2").HashAbsentAt)
	assert.Equal(t, map[string][]string{"unverified-absent": {"f1"}, "marked-absent": {"f2"}}, hbBuckets(hbReadReport(t, report)))
}

// mark_absent=false makes the run a pure hash run: absences are counted, and no
// marker is written, set or cleared, and no report is produced. A file that DID
// come back is hashed, and its stale marker is left alone (a run that records
// markers, or the scanner's stat, clears it).
func TestBackfillFileHashes_MarkAbsentFalseRecordsNothing(t *testing.T) {
	root := hbLibrary(t)
	when := time.Now().Add(-time.Hour)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "here.m4b", "bytes")},
		database.BookFile{ID: "f3", BookID: "b1", HashAbsentAt: &when, FilePath: hbWrite(t, root, "back.m4b", "back bytes")},
	)

	for _, dry := range []bool{true, false} {
		rep, c, report := runHB(t, h.store(), hbRun{dryRun: dry, params: `{"mark_absent":false}`})

		assert.Equal(t, hbCounts{"hashed": 2, "newly_missing": 1}, c, "dry=%v", dry)
		assert.Nil(t, h.get("f1").HashAbsentAt, "no absence is recorded")
		assert.NotNil(t, h.get("f3").HashAbsentAt, "a stale marker is not touched by a pure hash run")
		assert.Empty(t, report, "a run that records nothing writes no report")
		assert.Contains(t, rep.logs[len(rep.logs)-1], "no marker was written or cleared")
		assert.Zero(t, h.batchCount(), "no marker write of any kind, so no batch")
		assert.Zero(t, h.modifyCalls)
	}
	assert.NotEmpty(t, h.get("f3").FileHash, "the reappeared file is hashed")
	_, err := os.Stat(filepath.Join(root, ".reports"))
	assert.True(t, os.IsNotExist(err))
}

func hbMarked(t time.Time) *time.Time { return &t }

// clear_markers undoes a recording pass: every in-scope marker is cleared, one
// batch per book, Missing and hashes untouched, rows without a marker not
// written. dry_run is honoured and reports would-clear.
func TestBackfillFileHashes_ClearMarkersMode(t *testing.T) {
	root := hbLibrary(t)
	when := hbMarked(time.Now().Add(-time.Hour))
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", HashAbsentAt: when, FilePath: hbPath(root, "a.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", HashAbsentAt: when, Missing: true, FilePath: hbPath(root, "b.m4b")},
		database.BookFile{ID: "f3", BookID: "b1", FilePath: hbPath(root, "c.m4b")},
		database.BookFile{ID: "f4", BookID: "b2", HashAbsentAt: when, FileHash: "abc", FilePath: hbPath(root, "d.m4b")},
		database.BookFile{ID: "f5", BookID: "b3", HashAbsentAt: when, FilePath: hbPath(root, "e.m4b")},
	)

	_, c, report := runHB(t, h.store(), hbRun{dryRun: true, params: `{"clear_markers":true,"book_ids":["b1","b2"]}`})
	assert.Equal(t, hbCounts{"markers_cleared": 3}, c, "dry run counts what would clear: f1, f2, f4 (b3 is out of scope)")
	assert.Zero(t, h.batchCount())
	assert.Equal(t, map[string][]string{"would-clear-marker": {"f1", "f2", "f4"}}, hbBuckets(hbReadReport(t, report)))

	_, c, report = runHB(t, h.store(), hbRun{params: `{"clear_markers":true,"book_ids":["b1","b2"]}`})
	assert.Equal(t, hbCounts{"markers_cleared": 3}, c)
	assert.Equal(t, 2, h.batchCount(), "one batch per book")
	assert.Nil(t, h.get("f1").HashAbsentAt)
	assert.Nil(t, h.get("f2").HashAbsentAt)
	assert.True(t, h.get("f2").Missing, "Missing is not this job's to change")
	assert.Nil(t, h.get("f4").HashAbsentAt)
	assert.Equal(t, "abc", h.get("f4").FileHash)
	assert.NotNil(t, h.get("f5").HashAbsentAt, "out of scope")
	assert.Equal(t, map[string][]string{"cleared-marker": {"f1", "f2", "f4"}}, hbBuckets(hbReadReport(t, report)))
	assert.Zero(t, len(h.hashCalls), "clear mode hashes nothing")
}

// The reappeared files of one book are cleared in ONE batch for the book, not
// one write per file.
func TestBackfillFileHashes_ReappearedFilesOfOneBookClearInOneBatch(t *testing.T) {
	root := hbLibrary(t)
	when := hbMarked(time.Now().Add(-time.Hour))
	h := newHBRows()
	for i := range 30 {
		id := fmt.Sprintf("f%02d", i)
		h.rows[id] = &database.BookFile{ID: id, BookID: "b1", HashAbsentAt: when, FilePath: hbWrite(t, root, id+".m4b", "bytes "+id)}
	}

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"hashed": 30, "markers_cleared": 30}, c)
	assert.Equal(t, 1, h.batchCount(), "30 clears of one book: one batch")
	assert.Zero(t, h.modifyCalls, "never the per-row path")
}

// A recompute that fails after the markers were written is reported apart from
// a failed write: the rows count as recorded, nothing lands in marker_errors.
func TestBackfillFileHashes_RecomputeFailureReportedSeparately(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "a.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "b.m4b")},
	)
	h.recomputeErr = errors.New("book row locked")

	rep, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{"newly_missing": 2, "recompute_errors": 1}, c)
	assert.NotNil(t, h.get("f1").HashAbsentAt)
	logged := strings.Join(rep.logs, "\n")
	assert.NotContains(t, logged, "Failed to record absent markers", "the flags were written; that is not a failed write")
}

// A row whose marker write fails lands in marker_errors, not newly_missing; the
// rest of the book is still recorded.
func TestBackfillFileHashes_MarkerWriteFailureCountedPerRow(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(
		database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "a.m4b")},
		database.BookFile{ID: "f2", BookID: "b1", FilePath: hbPath(root, "b.m4b")},
		database.BookFile{ID: "f3", BookID: "b1", FilePath: hbPath(root, "c.m4b")},
	)
	h.failWrite = func(id string, _, _ *database.BookFile) error {
		if id == "f2" {
			return errors.New("disk full")
		}
		return nil
	}

	rep, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"newly_missing": 2, "marker_errors": 1}, c)
	assert.Nil(t, h.get("f2").HashAbsentAt)
	assert.NotNil(t, h.get("f1").HashAbsentAt)
	assert.NotNil(t, h.get("f3").HashAbsentAt)
	assert.Equal(t, map[string][]string{"marked-absent": {"f1", "f3"}, "marker-error": {"f2"}}, hbBuckets(hbReadReport(t, report)))
	assert.Contains(t, strings.Join(rep.logs, "\n"), "Failed to record absent markers")
}

// A row lands in exactly one outcome counter. Here the hash is stored but the
// stale marker's clear fails: that is one hashed row and one clear_errors, with
// write_errors untouched, and the outcome counters still add up to the rows.
func TestBackfillFileHashes_OneRowOneOutcomeBucket(t *testing.T) {
	root := hbLibrary(t)
	when := hbMarked(time.Now().Add(-time.Hour))
	h := newHBRows(
		database.BookFile{ID: "f01", BookID: "b1", FilePath: hbWrite(t, root, "ok.m4b", "ok")},                         // hashed
		database.BookFile{ID: "f02", BookID: "b1", HashAbsentAt: when, FilePath: hbWrite(t, root, "back.m4b", "back")}, // hashed, clear fails
		database.BookFile{ID: "f03", BookID: "b1", HashAbsentAt: when, FilePath: hbPath(root, "known.m4b")},            // skipped_known_missing
		database.BookFile{ID: "f04", BookID: "b1", FilePath: hbPath(root, "new-gone.m4b")},                             // newly_missing
		database.BookFile{ID: "f05", BookID: "b1", FilePath: filepath.Join(root, "Gotham")},                            // read_errors
		database.BookFile{ID: "f06", BookID: "b1", FilePath: ""},                                                       // skipped_no_path
		database.BookFile{ID: "f07", BookID: "b1", FilePath: "/lib/Gotham/Elsewhere/Hush.m4b"},                         // unverified_absent
		database.BookFile{ID: "f08", BookID: "b2", FilePath: hbPath(root, "fails.m4b")},                                // marker_errors
		database.BookFile{ID: "f09", BookID: "b1", FileHash: "already", FilePath: hbPath(root, "hashed.m4b")},          // not counted: has a hash
	)
	h.failWrite = func(id string, before, after *database.BookFile) error {
		if id == "f08" || (id == "f02" && before.HashAbsentAt != nil && after.HashAbsentAt == nil) {
			return errors.New("disk full")
		}
		return nil
	}

	_, c := runHashBackfill(t, h.store(), false)

	assert.Equal(t, hbCounts{
		"hashed": 2, "skipped_known_missing": 1, "newly_missing": 1, "read_errors": 1,
		"skipped_no_path": 1, "unverified_absent": 1, "marker_errors": 1, "clear_errors": 1,
	}, c)
	assert.Zero(t, c["write_errors"], "the hash was saved; a failed clear is not a failed hash write")
	assert.Equal(t, 8, c.outcomes(), "eight rows without a hash, each in exactly one outcome bucket")
}

// A hash that cannot be saved is write_errors and the row is not also hashed.
func TestBackfillFileHashes_HashSaveFailureIsWriteErrorOnly(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "a.m4b", "a")})
	store := h.store()
	store.SetBookFileHashFunc = func(string, string) error { return errors.New("disk full") }

	_, c := runHashBackfill(t, store, false)

	assert.Equal(t, hbCounts{"write_errors": 1}, c)
}

// A marker write is refused while a library.scan runs (the op registry cannot
// give this job the scan's concurrency key), failing closed; a dry run, which
// writes nothing, and a hash-only run, which records nothing, are not refused.
func TestBackfillFileHashes_MarkerWriteRefusedDuringAScan(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})
	now := time.Now()
	h.activeOps = []database.OperationV2Row{{ID: "scan-1", DefID: "library.scan", Status: "running", QueuedAt: now, StartedAt: &now}}

	_, err := hbRunJob(t, h.store(), hbRun{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backfill-file-hashes: library.scan is running")
	assert.Nil(t, h.get("f1").HashAbsentAt)

	_, err = hbRunJob(t, h.store(), hbRun{params: `{"clear_markers":true}`})
	require.Error(t, err, "the clear mode writes markers too")

	_, c, _ := runHB(t, h.store(), hbRun{dryRun: true})
	assert.Equal(t, hbCounts{"newly_missing": 1}, c)
	_, c, _ = runHB(t, h.store(), hbRun{params: `{"mark_absent":false}`})
	assert.Equal(t, hbCounts{"newly_missing": 1}, c)

	h.activeOps = nil
	_, c, _ = runHB(t, h.store(), hbRun{})
	assert.Equal(t, hbCounts{"newly_missing": 1}, c, "no scan: it records")
}

// A store that cannot batch the marker writes is refused up front, loudly,
// rather than recording nothing or recomputing per file. A dry run writes
// nothing and does not need the capability.
func TestBackfillFileHashes_StoreWithoutBatchModifierRefusedUnlessDryRun(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})
	// Embedding the interface (not the mock) hides MockStore.ModifyBookFiles.
	bare := struct{ maintenance.JobStore }{h.store()}

	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	err = j.Run(context.Background(), bare, &noopReporter{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BookFileBatchModifier")
	assert.Nil(t, h.get("f1").HashAbsentAt)

	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), bare, rep, true))
	c, _ := hbSummary(t, rep)
	assert.Equal(t, hbCounts{"newly_missing": 1}, c)
}

// The report is named for the operation and lives under {root}/.reports; a run
// without an operation id still gets a name.
func TestBackfillFileHashes_ReportName(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})

	_, _, report := runHB(t, h.store(), hbRun{opID: "01M5NV5QDSRPE8MTDK2DP1EKME"})
	assert.Equal(t, filepath.Join(root, ".reports", "backfill-file-hashes-01M5NV5QDSRPE8MTDK2DP1EKME.tsv"), report)

	_, _, report = runHB(t, h.store(), hbRun{})
	assert.Equal(t, filepath.Join(root, ".reports", "backfill-file-hashes-unknown-op.tsv"), report)
}

// A path with a tab or newline cannot corrupt the TSV.
func TestBackfillFileHashes_ReportSanitisesPaths(t *testing.T) {
	root := hbLibrary(t)
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Batman\tYear\nOne.m4b")})

	_, _, report := runHB(t, h.store(), hbRun{})

	rows := hbReadReport(t, report)
	require.Len(t, rows, 1)
	assert.Equal(t, hbPath(root, "Batman Year One.m4b"), rows[0].path)
}

// Whatever another writer did to the row between this run's read and its marker
// write, the marker is written only if the row is still un-hashed, unmarked and
// not flagged Missing (a state someone else already decided), and the row is
// then counted and reported as skipped_changed.
func TestBackfillFileHashes_MarkerWriteRechecksTheStoredRow(t *testing.T) {
	cases := map[string]func(*database.BookFile){
		"hashed meanwhile":        func(r *database.BookFile) { r.FileHash = "abc" },
		"marked by another run":   func(r *database.BookFile) { r.HashAbsentAt = hbMarked(time.Now().Add(-time.Minute)) },
		"flagged Missing by scan": func(r *database.BookFile) { r.Missing = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})
			store := h.store()
			snapshot := store.GetAllBookFilesCoreFunc
			store.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
				rows, err := snapshot()
				h.mu.Lock()
				mutate(h.rows["f1"])
				h.mu.Unlock()
				return rows, err
			}

			_, c, report := runHB(t, store, hbRun{})

			// The snapshot predates the mutation, so the run saw an un-hashed,
			// unmarked, present-flagged row; only the under-lock check can refuse it.
			assert.Equal(t, hbCounts{"skipped_changed": 1}, c)
			assert.Equal(t, map[string][]string{"skipped-changed": {"f1"}}, hbBuckets(hbReadReport(t, report)))
		})
	}
}

// The clear re-checks too: a marker the scanner already cleared (its successful
// stat does that) between the read and the clear is not written again, and is
// not an error.
func TestBackfillFileHashes_ClearRechecksTheStoredRow(t *testing.T) {
	root := hbLibrary(t)
	when := hbMarked(time.Now().Add(-time.Hour))
	h := newHBRows(database.BookFile{ID: "f1", BookID: "b1", HashAbsentAt: when, FilePath: hbWrite(t, root, "back.m4b", "back")})
	store := h.store()
	snapshot := store.GetAllBookFilesCoreFunc
	store.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		rows, err := snapshot()
		h.mu.Lock()
		h.rows["f1"].HashAbsentAt = nil // the scanner got there first
		h.mu.Unlock()
		return rows, err
	}

	_, c := runHashBackfill(t, store, false)

	assert.Equal(t, hbCounts{"hashed": 1}, c, "hashed, nothing left to clear, no error")
}
