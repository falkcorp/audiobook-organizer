// file: internal/maintenance/jobs/backfill_file_hashes_missing_test.go
// version: 7.1.0
// guid: 5e1c7a42-93b8-4d06-a7f1-2c84e0b9d315
// last-edited: 2026-10-26

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
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hbCounts is the parsed "hashed=1 not_found=2 ..." tally the job logs as its
// last line and stores on the operation log, zero entries dropped so a test
// names only what it expects.
type hbCounts map[string]int

var (
	hbPairRe   = regexp.MustCompile(`(\w+)=(\d+)`)
	hbReportRe = regexp.MustCompile(`report=(\S+)`)
)

// hbOutcomeKeys are the seven row-outcome counters; every un-hashed row the run
// reaches lands in exactly one of them.
var hbOutcomeKeys = []string{
	"hashed", "not_found", "unverified_outside_root", "read_errors", "write_errors", "busy", "skipped_no_path",
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

// hbConfigRoot points config at root for the test.
func hbConfigRoot(t *testing.T, root string) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}

// hbLibrary makes a populated library root, points config at it for the test,
// and returns it. The Gotham directory keeps the mount check satisfied and is
// where the fixtures' files live.
func hbLibrary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	hbConfigRoot(t, root)
	return root
}

func hbPath(root, name string) string { return filepath.Join(root, "Gotham", name) }

func hbWrite(t *testing.T, root, name, content string) string {
	t.Helper()
	p := hbPath(root, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func hbSum(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

// hbStore is a MockStore over an in-memory set of rows. Every write method other
// than ModifyBookFile fails the test: the job writes nothing for a file that is
// not on disk, and nothing but a hash, through ModifyBookFile, for one that is.
type hbStore struct {
	t *testing.T

	mu        sync.Mutex
	files     []database.BookFileCore
	hashCalls map[string]string     // id -> hash
	writes    map[string]int        // id -> how many times SetBookFileHash stored a hash for it
	failHash  func(id string) error // an error to return for the hash write of this row (nil result = no error)
	// beforeModify, when set, runs just before a row is read for ModifyBookFile,
	// with h.mu NOT held; a test uses it to change the row between hash and store.
	beforeModify func(h *hbStore, id string)
	state        map[string][]byte
	phases       []string
	// saveStateErr / getStateErr make the checkpoint (started marker) writes /
	// reads fail; journalSaveErr / journalGetErr do the same for the chunk
	// journal, which lives under the "<op id>:params" state key.
	saveStateErr, getStateErr     error
	journalSaveErr, journalGetErr error
}

// edit changes a row under the store's lock.
func (h *hbStore) edit(id string, fn func(*database.BookFileCore)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.files {
		if h.files[i].ID == id {
			fn(&h.files[i])
		}
	}
}

func newHBStore(t *testing.T, files ...database.BookFileCore) *hbStore {
	return &hbStore{t: t, files: files, hashCalls: map[string]string{}, writes: map[string]int{}, state: map[string][]byte{}}
}

func (h *hbStore) store() *database.MockStore {
	forbid := func(name string) {
		h.t.Errorf("the job must not call %s", name)
	}
	return &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]database.BookFileCore(nil), h.files...), nil
		},
		SetBookFileHashFunc: func(string, string) error {
			forbid("SetBookFileHash (the job stores hashes conditionally, through ModifyBookFile)")
			return nil
		},
		ModifyBookFileFunc: func(bookID, id string, fn func(*database.BookFile) error) (*database.BookFile, error) {
			if h.beforeModify != nil {
				h.beforeModify(h, id)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			for i := range h.files {
				c := &h.files[i]
				if c.ID != id || c.BookID != bookID {
					continue
				}
				row := database.BookFile{ID: c.ID, BookID: c.BookID, FilePath: c.FilePath, FileSize: c.FileSize,
					LastScanMtime: c.LastScanMtime, FileHash: c.FileHash}
				if err := fn(&row); err != nil {
					if errors.Is(err, database.ErrSkipBookFileWrite) {
						return &row, nil
					}
					return nil, err
				}
				if h.failHash != nil {
					if err := h.failHash(id); err != nil {
						return nil, err
					}
				}
				c.FileHash = row.FileHash // a later GetAllBookFilesCore sees the stored hash, as the real store does
				h.hashCalls[id] = row.FileHash
				h.writes[id]++
				return &row, nil
			}
			return nil, nil
		},
		UpdateBookFileFunc: func(string, *database.BookFile) error { forbid("UpdateBookFile"); return nil },
		DeleteBookFileFunc: func(string) error { forbid("DeleteBookFile"); return nil },
		PatchBookFileFieldsFunc: func(string, string, database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
			forbid("PatchBookFileFields")
			return nil, nil, nil
		},
		BatchUpsertBookFilesFunc: func([]*database.BookFile) error { forbid("BatchUpsertBookFiles"); return nil },
		UpdateBookFunc: func(string, *database.Book) (*database.Book, error) {
			forbid("UpdateBook")
			return nil, nil
		},
		SaveOperationStateFunc: func(opID string, data []byte) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if strings.HasSuffix(opID, ":params") {
				if h.journalSaveErr != nil {
					return h.journalSaveErr
				}
				h.state[opID] = data
				return nil
			}
			if h.saveStateErr != nil {
				return h.saveStateErr
			}
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
			if strings.HasSuffix(opID, ":params") {
				if h.journalGetErr != nil {
					return nil, h.journalGetErr
				}
				return h.state[opID], nil
			}
			if h.getStateErr != nil {
				return nil, h.getStateErr
			}
			return h.state[opID], nil
		},
		DeleteOperationStateFunc: func(opID string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.state, opID)
			delete(h.state, opID+":params") // the real store removes both keys
			return nil
		},
	}
}

func (h *hbStore) hashed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.hashCalls))
	for id := range h.hashCalls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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

func hbCountLogs(rep *noopReporter, prefix string) int {
	n := 0
	for _, l := range rep.logs {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// Every row without a hash lands in exactly one outcome, a not-found writes
// nothing to the database, and the report lists each row the run did not hash.
func TestBackfillFileHashes_NotFoundIsCountedAndWritesNothing(t *testing.T) {
	root := hbLibrary(t)
	here := hbWrite(t, root, "The Long Halloween.m4b", "long halloween bytes")
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},                 // absent
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: here},                                         // present
		database.BookFileCore{ID: "f3", BookID: "b2", FilePath: hbPath(root, "Hush.m4b"), Missing: true},      // absent, flagged
		database.BookFileCore{ID: "f4", BookID: "b2", FilePath: "  "},                                         // no path
		database.BookFileCore{ID: "f5", BookID: "b2", FilePath: hbPath(root, "Done.m4b"), FileHash: "have"},   // already hashed
		database.BookFileCore{ID: "f6", BookID: "b3", FilePath: "/lib/Gotham/Elsewhere/The Killing Joke.m4b"}, // absent, outside root
		database.BookFileCore{ID: "f7", BookID: "b3", FilePath: filepath.Join(here, "inside.m4b")},            // ENOTDIR
		database.BookFileCore{ID: "f8", BookID: "b3", FilePath: filepath.Join(root, "Gotham")},                // a directory: read error
	)

	rep, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1, "not_found": 3, "unverified_outside_root": 1, "read_errors": 1, "skipped_no_path": 1}, c)
	assert.Equal(t, 7, c.outcomes(), "seven rows without a hash, each in exactly one outcome; f5 already had one and is not counted")
	assert.Equal(t, []string{"f2"}, h.hashed(), "only the file that exists is stored; nothing is written for the absent ones")
	assert.Equal(t, hbSum("long halloween bytes"), h.hashCalls["f2"])

	rows := hbReadReport(t, report)
	assert.Equal(t, map[string][]string{
		"hashed":                 {"f2"},
		"not-found":              {"f1", "f3", "f7"},
		"not-found-outside-root": {"f6"},
		"read-error":             {"f8"},
		"no-path":                {"f4"},
	}, hbBuckets(rows))
	for _, r := range rows {
		if r.fileID == "f3" {
			assert.Contains(t, r.reason, "flagged missing")
		}
		if r.fileID == "f1" {
			assert.Equal(t, hbPath(root, "Year One.m4b"), r.path)
			assert.NotContains(t, r.reason, "flagged missing")
		}
	}
	assert.Equal(t, filepath.Join(root, ".reports"), filepath.Dir(report))
	assert.Equal(t, 1, hbCountLogs(rep, "Hash computation failed for"), "the unreadable file is still warned about")
}

// A dry run counts the same rows and stores nothing, but still writes the report.
func TestBackfillFileHashes_DryRunStoresNothing(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "here.m4b", "bytes")},
	)

	rep, c, report := runHB(t, h.store(), hbRun{dryRun: true})

	assert.Equal(t, hbCounts{"hashed": 1, "not_found": 1}, c)
	assert.Empty(t, h.hashed(), "a dry run stores no hash")
	assert.Contains(t, rep.logs[len(rep.logs)-1], "Dry run, no hashes stored")
	assert.NotContains(t, rep.logs[len(rep.logs)-1], "nothing written", "a dry run writes the report and a checkpoint")
	assert.Equal(t, map[string][]string{"hashed": {"f2"}, "not-found": {"f1"}}, hbBuckets(hbReadReport(t, report)))
}

// Not-found paths are all counted and reported, but only the first 50 reach the
// operation log, and none is a per-file slog warning.
func TestBackfillFileHashes_NotFoundLogsAreCapped(t *testing.T) {
	root := hbLibrary(t)
	files := make([]database.BookFileCore, 120)
	for i := range files {
		files[i] = database.BookFileCore{ID: fmt.Sprintf("f%03d", i), BookID: "b1", FilePath: hbPath(root, fmt.Sprintf("gone%03d.m4b", i))}
	}
	h := newHBStore(t, files...)

	rep, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"not_found": 120}, c)
	assert.Equal(t, 50, hbCountLogs(rep, "File not found:"), "the log is capped at 50 paths")
	assert.Len(t, hbReadReport(t, report), 120, "the report lists every one")
}

// A permission or I/O failure is not "the file is gone": it stays a read error,
// retried next run, and is never counted as not_found.
func TestBackfillFileHashes_OnlyNotFoundIsNotFound(t *testing.T) {
	root := hbLibrary(t)
	dir := filepath.Join(root, "Gotham", "Arkham")
	require.NoError(t, os.Mkdir(dir, 0o755))
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: dir})

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"read_errors": 1}, c)
	assert.Equal(t, map[string][]string{"read-error": {"f1"}}, hbBuckets(hbReadReport(t, report)))
}

// The hash write is unchanged: a failed save is a write_error, reported, and
// the run goes on.
func TestBackfillFileHashes_WriteErrorIsCounted(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "a.m4b", "a")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "b.m4b", "b")},
	)
	h.failHash = func(id string) error {
		if id == "f1" {
			return errors.New("disk full")
		}
		return nil
	}

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1, "write_errors": 1}, c)
	assert.Equal(t, []string{"f2"}, h.hashed())
	assert.Equal(t, map[string][]string{"hashed": {"f2"}, "write-error": {"f1"}}, hbBuckets(hbReadReport(t, report)))
}

// book_ids limits the run to those books' rows.
func TestBackfillFileHashes_BookIDsScope(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "a.m4b", "a")},
		database.BookFileCore{ID: "f2", BookID: "b2", FilePath: hbWrite(t, root, "b.m4b", "b")},
		database.BookFileCore{ID: "f3", BookID: "b2", FilePath: hbPath(root, "gone.m4b")},
	)

	_, c, _ := runHB(t, h.store(), hbRun{params: `{"book_ids":["b2"]}`})

	assert.Equal(t, hbCounts{"hashed": 1, "not_found": 1}, c)
	assert.Equal(t, []string{"f2"}, h.hashed())
}

// An empty root directory is an unmounted share: every path under it is
// not-found, and the run would report the whole library as gone. Refused, with
// nothing hashed. A root holding only app-owned dot entries (a report directory
// from an earlier run, the wav cache) is the same bare mountpoint.
func TestBackfillFileHashes_RefusesAnUnmountedRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		dots []string
	}{
		{"empty root", nil},
		{"only dot entries", []string{".reports", ".wav-cache"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range tc.dots {
				require.NoError(t, os.Mkdir(filepath.Join(root, d), 0o755))
			}
			hbConfigRoot(t, root)
			h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "gone.m4b")})

			for _, dry := range []bool{false, true} {
				_, err := hbRunJob(t, h.store(), hbRun{dryRun: dry})
				require.Error(t, err, "dry=%v", dry)
				assert.Contains(t, err.Error(), "no library content")
			}
			assert.Empty(t, h.hashed())
			ents, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Len(t, ents, len(tc.dots), "the refusal creates nothing in the bare root")
		})
	}
}

// With no library root configured there is nothing to check and nowhere to put
// the report: the run hashes as it always did, without one. No mount was
// checked, so a not-found is unverified, not a not_found.
func TestBackfillFileHashes_NoRootConfiguredRunsWithoutAReport(t *testing.T) {
	hbConfigRoot(t, "")
	dir := t.TempDir()
	p := filepath.Join(dir, "here.m4b")
	require.NoError(t, os.WriteFile(p, []byte("bytes"), 0o644))
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: p},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: filepath.Join(dir, "gone.m4b")},
	)

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1, "unverified_outside_root": 1}, c)
	assert.Empty(t, report)
	assert.Equal(t, []string{"f1"}, h.hashed())
}

// Two runs with no operation ID get different reports instead of truncating one
// another's; a run with an ID is named for it.
func TestBackfillFileHashes_ReportNameIsUniquePerRun(t *testing.T) {
	root := hbLibrary(t)
	rows := []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")}}

	_, _, first := runHB(t, newHBStore(t, rows...).store(), hbRun{})
	_, _, second := runHB(t, newHBStore(t, rows...).store(), hbRun{})
	_, _, named := runHB(t, newHBStore(t, rows...).store(), hbRun{opID: "01OPID/with:odd chars"})

	assert.NotEqual(t, first, second)
	for _, p := range []string{first, second, named} {
		assert.Len(t, hbReadReport(t, p), 1, p)
	}
	assert.Equal(t, "backfill-file-hashes-01OPID_with_odd_chars.tsv", filepath.Base(named))
}

// A run with an operation ID that finds that operation's report appends to it.
// The row the report lists is carried over and not redone.
func TestBackfillFileHashes_ResumeAppendsToTheReport(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "first.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "second.m4b")},
	)
	st := h.store()
	const opID = "01RESUME"

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, os.WriteFile(reportFile,
		[]byte("bucket\tfile_id\tbook_id\tpath\treason\nnot-found\tf1\tb1\t"+hbPath(root, "first.m4b")+"\tfile not found\n"), 0o664))

	_, c, report := runHB(t, st, hbRun{opID: opID})

	assert.Equal(t, hbCounts{"not_found": 2}, c, "the first row comes from the report, the second is processed now")
	assert.Equal(t, reportFile, report)
	assert.Equal(t, map[string][]string{"not-found": {"f1", "f2"}}, hbBuckets(hbReadReport(t, report)), "the first part's row is kept")
}

// cancelReporter cancels the run's context when the Nth row is reached. The job
// serialises reporter calls, so the plain counter is safe.
type cancelReporter struct {
	noopReporter
	n, at  int
	cancel context.CancelFunc
}

func (r *cancelReporter) Increment() {
	r.n++
	if r.n == r.at {
		r.cancel()
	}
}

// hbMixedLibrary is n rows: every tenth has a real file (hashed), one row in
// seven has an empty path, the rest are not found. IDs are f0000, f0001, ...
func hbMixedLibrary(t *testing.T, root string, n int) []database.BookFileCore {
	t.Helper()
	files := make([]database.BookFileCore, n)
	for i := range files {
		f := database.BookFileCore{ID: fmt.Sprintf("f%04d", i), BookID: fmt.Sprintf("b%d", i%9)}
		switch {
		case i%10 == 0:
			f.FilePath = hbWrite(t, root, fmt.Sprintf("Knightfall %04d.m4b", i), fmt.Sprintf("knightfall bytes %d", i))
		case i%7 == 0:
			f.FilePath = ""
		default:
			f.FilePath = hbPath(root, fmt.Sprintf("Arkham %04d.m4b", i))
		}
		files[i] = f
	}
	return files
}

// hbDistinctIDs fails on a repeated file ID and returns the report's IDs.
func hbDistinctIDs(t *testing.T, rows []hbReportRow) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, r := range rows {
		require.Falsef(t, seen[r.fileID], "file %s is in the report twice", r.fileID)
		seen[r.fileID] = true
	}
	return seen
}

// A run cancelled in the middle of a chunk (not at a checkpoint boundary) and
// then resumed ends with exactly the report and totals an uninterrupted run
// has: no row twice, none lost, every outcome counted once, each hash stored
// once.
func TestBackfillFileHashes_CancelMidChunkThenResumeIsExact(t *testing.T) {
	const total = 1300 // chunks: [0,512) [512,1024) [1024,1300)
	for _, tc := range []struct {
		name     string
		dryRun   bool
		cancelAt int // the Nth row reached cancels; rows 512 and 1024 start new chunks
	}{
		{"apply, cancelled in chunk 2", false, 700},
		{"dry run, cancelled in chunk 2", true, 700},
		{"apply, cancelled in chunk 1, before any checkpoint", false, 100},
		{"apply, cancelled one row into chunk 3", false, 1025},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t, hbMixedLibrary(t, root, total)...)
			const opID = "01CANCELMIDCHUNK"

			// What an uninterrupted run reports, from the rows' shape.
			want := hbCounts{}
			for i := 0; i < total; i++ {
				switch {
				case i%10 == 0:
					want["hashed"]++
				case i%7 == 0:
					want["skipped_no_path"]++
				default:
					want["not_found"]++
				}
			}

			ctx, cancel := context.WithCancel(maintenance.WithOperationID(context.Background(), opID))
			defer cancel()
			j, err := maintenance.Get("backfill-file-hashes")
			require.NoError(t, err)
			first := &cancelReporter{at: tc.cancelAt, cancel: cancel}
			err = j.Run(ctx, h.store(), first, tc.dryRun)
			require.ErrorIs(t, err, context.Canceled)

			// Whatever reached disk before the resume is whole chunks only.
			reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
			onDisk := hbReadReport(t, reportFile)
			assert.Zero(t, len(onDisk)%jobs.HashBackfillChunkSize, "rows on disk are whole chunks, never part of one: %d", len(onDisk))
			hbDistinctIDs(t, onDisk)

			rep, c, report := runHB(t, h.store(), hbRun{opID: opID, dryRun: tc.dryRun})

			assert.Equal(t, reportFile, report)
			assert.Equal(t, want, c, "the summary counts the whole run, not just the resumed part")
			assert.Equal(t, total, c.outcomes(), "every row is accounted for")
			rows := hbReadReport(t, report)
			ids := hbDistinctIDs(t, rows)
			assert.Len(t, ids, len(rows))
			assert.Len(t, rows, c.outcomes(), "one report row per counted outcome")
			assert.NotContains(t, rep.logs[len(rep.logs)-1], "counts cover only")
			if tc.dryRun {
				assert.Empty(t, h.hashed())
			} else {
				assert.Len(t, h.hashed(), want["hashed"])
				for id, n := range h.writes {
					assert.Equal(t, 1, n, "hash for %s stored %d times", id, n)
				}
			}
		})
	}
}

// A hard kill can cut the report's last line in half. The resumed run drops the
// fragment, redoes that row, and ends with a report every line of which parses.
func TestBackfillFileHashes_ResumeDropsAHalfWrittenLastLine(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush.m4b")},
		database.BookFileCore{ID: "f3", BookID: "b1", FilePath: hbPath(root, "The Court of Owls.m4b")},
	)
	const opID = "01CUTMIDLINE"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, os.WriteFile(reportFile, []byte("bucket\tfile_id\tbook_id\tpath\treason\n"+
		"not-found\tf1\tb1\t"+hbPath(root, "Year One.m4b")+"\tfile not found\n"+
		"not-found\tf2\tb1\t/lib/Gotham/"+strings.Repeat("Hush ", 200)), 0o664)) // a fragment longer than the rows written after it

	_, c, report := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"not_found": 3}, c)
	assert.Equal(t, map[string][]string{"not-found": {"f1", "f2", "f3"}}, hbBuckets(hbReadReport(t, report)))
}

// A resumed run whose report has gone says so in its summary instead of
// presenting a segment's counts as the run's.
func TestBackfillFileHashes_ResumeWithoutAReportSaysTheCountsArePartial(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush.m4b")},
	)
	const opID = "01REPORTGONE"
	data, err := json.Marshal(operations.OperationState{OperationID: opID, Phase: "started", PhaseTotal: 2, UpdatedAt: time.Now()})
	require.NoError(t, err)
	h.state[opID] = data // the op was started before, but its report is gone

	rep, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"not_found": 2}, c)
	assert.Contains(t, rep.logs[len(rep.logs)-1], "counts cover only the work done in this run")
}

// A report whose lines cannot be read back is refused rather than guessed at.
func TestBackfillFileHashes_ResumeRefusesACorruptReport(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush.m4b")})
	const opID = "01CORRUPT"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv"),
		[]byte("bucket\tfile_id\tbook_id\tpath\treason\nnot-found\tonly-three\tfields\n"), 0o664))

	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
}

// Every refusal the op makes before hashing, a dry run included, names its
// cause: a root that is unreadable, not absolute, or whose .reports directory
// cannot be written (here a file squats on the name). The fourth, a root with
// only dot entries, is TestBackfillFileHashes_RefusesAnUnmountedRoot.
func TestBackfillFileHashes_RefusalsNameTheirCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string // returns the RootDir
		want  string
	}{
		{"unreadable root", func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-share") }, "unreadable"},
		{"relative root", func(t *testing.T) string { return "." }, "not absolute"}, // "." exists and holds the package's files
		{"reports directory not writable", func(t *testing.T) string {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".reports"), []byte("a file, not a directory"), 0o644))
			return root
		}, "not writable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hbConfigRoot(t, tc.setup(t))
			h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: "/lib/Gotham/Year One.m4b"})
			for _, dry := range []bool{false, true} {
				_, err := hbRunJob(t, h.store(), hbRun{dryRun: dry})
				require.Error(t, err, "dry=%v", dry)
				assert.Contains(t, err.Error(), "refusing to run")
				assert.Contains(t, err.Error(), tc.want)
			}
			assert.Empty(t, h.hashed())
		})
	}
}

// A report directory left group-writable by an older build is tightened to 0750
// and the run goes ahead and writes its report there; nothing has to be done by
// hand first.
func TestBackfillFileHashes_TightensAGroupWritableReportsDirAndRuns(t *testing.T) {
	root := hbLibrary(t)
	here := hbWrite(t, root, "The Long Halloween.m4b", "long halloween bytes")
	dir := filepath.Join(root, ".reports")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o775)) // Chmod is not subject to the umask
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: here})

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1}, c)
	assert.Equal(t, dir, filepath.Dir(report))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
	assert.Equal(t, []string{"f1"}, h.hashed())
}

// A kill inside a chunk's commit, between its steps, leaves the journal behind;
// the resumed run replays it. Whatever the kill point, the resumed run ends with
// the report and totals of an uninterrupted run: no row twice, none lost, each
// hash stored once. "after-store" is the window the report alone cannot cover:
// the hashes are stored, so the rows are skipped on resume, yet none is in the
// report.
func TestBackfillFileHashes_KillInsideACommitThenResumeIsExact(t *testing.T) {
	const total = 1300
	for _, tc := range []struct {
		name        string
		dryRun      bool
		stage       string
		chunkStart  int
		wantOnDisk  int  // report rows on disk when the process "dies"
		wantStored  bool // whether the dead run had stored any hash
		wantJournal bool
	}{
		{"apply, after the journal", false, "after-journal", 512, 512, false, true},
		{"apply, after the hashes are stored", false, "after-store", 512, 512, true, true},
		{"apply, after the report is flushed", false, "after-flush", 512, 1024, true, true},
		{"apply, first chunk, after the hashes are stored", false, "after-store", 0, 0, true, true},
		{"dry run, after the journal", true, "after-journal", 512, 512, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t, hbMixedLibrary(t, root, total)...)
			const opID = "01KILLINSIDECOMMIT"
			errKill := errors.New("killed")
			restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
				if stage == tc.stage && start == tc.chunkStart {
					return errKill
				}
				return nil
			})
			_, err := hbRunJob(t, h.store(), hbRun{opID: opID, dryRun: tc.dryRun})
			restore()
			require.ErrorIs(t, err, errKill)

			reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
			journalKey := opID + ":params"
			assert.Len(t, hbReadReport(t, reportFile), tc.wantOnDisk, "what the dead run left in the report")
			storedInChunk := false
			for id := range h.writes {
				n, perr := strconv.Atoi(strings.TrimPrefix(id, "f"))
				require.NoError(t, perr)
				if n >= tc.chunkStart && n < tc.chunkStart+jobs.HashBackfillChunkSize {
					storedInChunk = true
				}
			}
			assert.Equal(t, tc.wantStored, storedInChunk, "whether the dead run had stored hashes of the chunk it was killed in")
			_, hasJournal := h.state[journalKey]
			assert.Equal(t, tc.wantJournal, hasJournal, "journal present in the operation state after the kill")

			rep, c, report := runHB(t, h.store(), hbRun{opID: opID, dryRun: tc.dryRun})

			want := hbCounts{}
			for i := 0; i < total; i++ {
				switch {
				case i%10 == 0:
					want["hashed"]++
				case i%7 == 0:
					want["skipped_no_path"]++
				default:
					want["not_found"]++
				}
			}
			assert.Equal(t, want, c)
			rows := hbReadReport(t, report)
			assert.Len(t, hbDistinctIDs(t, rows), total)
			assert.Len(t, rows, total)
			assert.NotContains(t, rep.logs[len(rep.logs)-1], "counts cover only")
			_, hasJournal = h.state[journalKey]
			assert.False(t, hasJournal, "the journal is gone after the resumed run")
			if tc.dryRun {
				assert.Empty(t, h.hashed())
			} else {
				assert.Len(t, h.hashed(), want["hashed"])
				for id, n := range h.writes {
					assert.Equal(t, 1, n, "hash for %s stored %d times", id, n)
				}
			}
		})
	}
}

// hbWant is what an uninterrupted run over hbMixedLibrary reports.
func hbWant(total int) hbCounts {
	want := hbCounts{}
	for i := 0; i < total; i++ {
		switch {
		case i%10 == 0:
			want["hashed"]++
		case i%7 == 0:
			want["skipped_no_path"]++
		default:
			want["not_found"]++
		}
	}
	return want
}

// hbAssertExact checks the end state every resume test shares: the summary and
// report equal an uninterrupted run's, no ID is listed twice, and each hash was
// stored exactly once.
func hbAssertExact(t *testing.T, h *hbStore, c hbCounts, report string, total int, dryRun bool) {
	t.Helper()
	assert.Equal(t, hbWant(total), c)
	rows := hbReadReport(t, report)
	assert.Len(t, hbDistinctIDs(t, rows), total)
	assert.Len(t, rows, total)
	if dryRun {
		assert.Empty(t, h.hashed())
		return
	}
	assert.Len(t, h.hashed(), hbWant(total)["hashed"])
	for id, n := range h.writes {
		assert.Equal(t, 1, n, "hash for %s stored %d times", id, n)
	}
}

// hbCancelRun runs the job and cancels it when the Nth row is reached.
func hbCancelRun(t *testing.T, st maintenance.JobStore, opID string, at int) error {
	t.Helper()
	ctx, cancel := context.WithCancel(maintenance.WithOperationID(context.Background(), opID))
	defer cancel()
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	return j.Run(ctx, st, &cancelReporter{at: at, cancel: cancel}, false)
}

func hbAnyLogContains(rep *noopReporter, sub string) bool {
	for _, l := range rep.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// Reach path 1: the process dies right after chunk 1 is fully committed (report
// fsynced, journal removed), before anything marks progress. The rerun must
// keep that chunk's rows, hashed rows included, not start the report over.
func TestBackfillFileHashes_KillAfterTheFirstChunkCommitsThenRerunKeepsItsRows(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	const opID = "01KILLAFTERFIRSTCOMMIT"
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-commit" && start == 0 {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)
	reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.Len(t, hbReadReport(t, reportFile), jobs.HashBackfillChunkSize)

	rep, c, report := runHB(t, h.store(), hbRun{opID: opID})

	hbAssertExact(t, h, c, report, total, false)
	assert.False(t, hbAnyLogContains(rep, "counts cover only"))
}

// Reach path 2: the operation's saved state cannot be written at all (every
// checkpoint save fails), the run is interrupted after a chunk, and the rerun
// still ends exact: progress lives in the report and the stored hashes, not in
// the saved state. The failures are logged, not swallowed.
func TestBackfillFileHashes_FailedStateWritesDoNotLoseProgress(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	const opID = "01STATEWRITESFAIL"
	h.saveStateErr = errors.New("disk on fire")
	err := hbCancelRun(t, h.store(), opID, 700)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, h.state, "nothing could be saved")

	rep, c, report := runHB(t, h.store(), hbRun{opID: opID})

	hbAssertExact(t, h, c, report, total, false)
	assert.True(t, hbAnyLogContains(rep, "Could not save this operation's started marker"), "the failure is logged")
	assert.False(t, hbAnyLogContains(rep, "counts cover only"), "the report carried the earlier part, so the counts are whole")
}

// Reach path 3: the saved state cannot be READ on the resume. The report is
// found anyway and never truncated; the failure is logged.
func TestBackfillFileHashes_UnreadableStateDoesNotTruncateTheReport(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	const opID = "01STATEREADFAILS"
	require.ErrorIs(t, hbCancelRun(t, h.store(), opID, 700), context.Canceled)
	h.getStateErr = errors.New("transient read error")

	rep, c, report := runHB(t, h.store(), hbRun{opID: opID})

	hbAssertExact(t, h, c, report, total, false)
	assert.True(t, hbAnyLogContains(rep, "Could not read this operation's saved state"))
	assert.False(t, hbAnyLogContains(rep, "counts cover only"))
	assert.False(t, hbAnyLogContains(rep, "counts may cover only"))
}

// With no report to carry the earlier counts and a saved state that cannot be
// read, the summary says it cannot vouch for the whole operation.
func TestBackfillFileHashes_UnreadableStateAndNoReportSaysSo(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")})
	h.getStateErr = errors.New("transient read error")

	rep, _, _ := runHB(t, h.store(), hbRun{opID: "01NOTHINGTOGOON"})

	assert.Contains(t, rep.logs[len(rep.logs)-1], "saved state could not be read")
}

// Reach path 4: the run finishes and clears its state, but the process dies
// before the registry marks the op completed, so boot re-queues the same op ID.
// The rerun finds a complete report and every hash stored: it changes nothing
// and its summary is the whole run's, not "hashed=0".
func TestBackfillFileHashes_RerunAfterACompletedRunChangesNothing(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	const opID = "01REQUEUEDAFTERCLEAR"
	_, first, report := runHB(t, h.store(), hbRun{opID: opID})
	require.Equal(t, hbWant(total), first)
	require.Empty(t, h.state, "the finished run cleared its state")
	before, err := os.ReadFile(report)
	require.NoError(t, err)

	rep, c, report2 := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, report, report2)
	hbAssertExact(t, h, c, report2, total, false)
	after, err := os.ReadFile(report2)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the report is untouched")
	assert.False(t, hbAnyLogContains(rep, "counts cover only"))
}

// The checkpoint index is gone: a rerun walks every row from the start, so rows
// retired between the attempts (which shifts the later rows down) are not
// skipped.
func TestBackfillFileHashes_RowsRetiredBetweenAttemptsAreNotSkipped(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	files := hbMixedLibrary(t, root, total)
	h := newHBStore(t, files...)
	const opID = "01ROWSSHIFTED"
	require.ErrorIs(t, hbCancelRun(t, h.store(), opID, 700), context.Canceled) // chunk 1 committed
	// Rows 3 and 5 (both below the old checkpoint) disappear from the live list.
	h.mu.Lock()
	h.files = append(append(append([]database.BookFileCore(nil), h.files[:3]...), h.files[4:5]...), h.files[6:]...)
	h.mu.Unlock()

	_, c, report := runHB(t, h.store(), hbRun{opID: opID})

	rows := hbReadReport(t, report)
	assert.Len(t, hbDistinctIDs(t, rows), total, "every original row is listed once: the two retired ones from attempt 1, the rest by whichever attempt reached them")
	assert.Equal(t, hbWant(total), c, "the seeded counts include the retired rows' lines")
}

// A hash is stored only if the row still points at the file that was hashed.
// The row's path changes between the hash and the store: nothing is stored, the
// row is busy and retried next run.
func TestBackfillFileHashes_RowRepointedBeforeTheStoreIsBusyNotHashed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*database.BookFileCore)
	}{
		{"path changed", func(c *database.BookFileCore) { c.FilePath = hbPath("/lib", "The Long Halloween.m4b") }},
		{"size changed", func(c *database.BookFileCore) { c.FileSize = 12345 }},
		{"scan mtime changed", func(c *database.BookFileCore) { m := int64(99); c.LastScanMtime = &m }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t,
				database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
				database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
			)
			h.beforeModify = func(h *hbStore, id string) {
				if id == "f1" {
					h.edit("f1", tc.change)
				}
			}

			rep, c, report := runHB(t, h.store(), hbRun{opID: "01REPOINTED"})

			assert.Equal(t, hbCounts{"hashed": 1, "busy": 1}, c)
			assert.Equal(t, []string{"f2"}, h.hashed(), "nothing is stored for the row that changed")
			rows := hbReadReport(t, report)
			assert.Equal(t, map[string][]string{"busy": {"f1"}, "hashed": {"f2"}}, hbBuckets(rows))
			for _, r := range rows {
				if r.fileID == "f1" {
					assert.Contains(t, r.reason, "row changed since the file was hashed, retried next run")
				}
			}
			assert.Contains(t, rep.logs[len(rep.logs)-1], "busy=1")
		})
	}
}

// A journal replayed after a restart is conditional too: the row was repointed
// while the process was down, so the dead run's hash is not stored on it.
func TestBackfillFileHashes_ReplayDoesNotStoreAHashOnARepointedRow(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
	)
	const opID = "01REPLAYREPOINT"
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-journal" {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)
	require.Empty(t, h.hashed())
	h.edit("f1", func(c *database.BookFileCore) { c.FilePath = hbPath("/lib", "The Long Halloween.m4b") }) // repointed while down

	_, c, report := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"hashed": 1, "busy": 1}, c)
	assert.Equal(t, []string{"f2"}, h.hashed())
	assert.Equal(t, map[string][]string{"busy": {"f1"}, "hashed": {"f2"}}, hbBuckets(hbReadReport(t, report)))
}

// A row that got a hash from another writer before the store keeps it: the job
// writes nothing, and counts the row as hashed.
func TestBackfillFileHashes_RowHashedByAnotherWriterIsNotOverwritten(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
	h.beforeModify = func(h *hbStore, id string) {
		h.edit(id, func(c *database.BookFileCore) { c.FileHash = "someone-elses-hash" })
	}

	_, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1}, c)
	assert.Empty(t, h.writes, "the job stored nothing")
	assert.Equal(t, "someone-elses-hash", h.files[0].FileHash)
	rows := hbReadReport(t, report)
	require.Len(t, rows, 1)
	assert.Equal(t, "hash already stored", rows[0].reason)
}

// A write that fails with the store's changed-since error is busy, not a write
// error: retried next run, listed with the busy reason.
func TestBackfillFileHashes_ChangedSinceConflictIsBusy(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
		database.BookFileCore{ID: "f3", BookID: "b1", FilePath: hbWrite(t, root, "Knightfall.m4b", "knightfall bytes")},
	)
	h.failHash = func(id string) error {
		switch id {
		case "f1":
			return fmt.Errorf("ModifyBookFile: write f1: %w", database.ErrBookFileChangedSince)
		case "f3":
			return errors.New("disk full")
		}
		return nil
	}

	rep, c, report := runHB(t, h.store(), hbRun{})

	assert.Equal(t, hbCounts{"hashed": 1, "busy": 1, "write_errors": 1}, c)
	rows := hbReadReport(t, report)
	assert.Equal(t, map[string][]string{"busy": {"f1"}, "hashed": {"f2"}, "write-error": {"f3"}}, hbBuckets(rows))
	for _, r := range rows {
		if r.fileID == "f1" {
			assert.Equal(t, "row was busy, retried next run", r.reason)
		}
	}
	assert.Contains(t, rep.logs[len(rep.logs)-1], "busy=1")
}

// A report whose last row lacks its newline (a hand edit, or a kill that landed
// exactly on the row boundary) keeps that row; CRLF line endings are tolerated.
func TestBackfillFileHashes_ResumeKeepsAFinalRowWithoutANewlineAndToleratesCRLF(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush.m4b")},
		database.BookFileCore{ID: "f3", BookID: "b1", FilePath: hbPath(root, "Knightfall.m4b")},
	)
	const opID = "01NONEWLINE"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, os.WriteFile(reportFile, []byte("bucket\tfile_id\tbook_id\tpath\treason\r\n"+
		"not-found\tf1\tb1\t"+hbPath(root, "Year One.m4b")+"\tfile not found\r\n"+
		"not-found\tf2\tb1\t"+hbPath(root, "Hush.m4b")+"\tfile not found"), 0o664))

	_, c, report := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"not_found": 3}, c)
	b, err := os.ReadFile(report)
	require.NoError(t, err)
	assert.Contains(t, string(b), "file not found\nnot-found\tf3\t", "the unterminated row was closed before f3's row was appended")
	var ids []string
	for _, line := range strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r", ""), "\n"), "\n")[1:] {
		f := strings.Split(line, "\t")
		require.Equal(t, "not-found", f[0])
		ids = append(ids, f[1])
	}
	assert.Equal(t, []string{"f1", "f2", "f3"}, ids)
}

// The description names what the op does with a not-found, and the conditional
// store.
func TestBackfillFileHashes_DescriptionIsAccurate(t *testing.T) {
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	d := j.Description()
	for _, want := range []string{"unverified_outside_root", "busy", "not_found", "dot-prefixed", "not an absolute path", ".reports directory that cannot be written"} {
		assert.Contains(t, d, want)
	}
}

// The journal carries the row's path, size and scan mtime as they were when the
// file was hashed, and the replay compares all three: each one that changed
// while the process was down makes the row busy, and a row that did not change
// takes its hash.
func TestBackfillFileHashes_ReplayComparesPathSizeAndMtime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*database.BookFileCore)
		busy   bool
	}{
		{"nothing changed", func(*database.BookFileCore) {}, false},
		{"path changed", func(c *database.BookFileCore) { c.FilePath = hbPath("/lib", "The Long Halloween.m4b") }, true},
		{"size changed", func(c *database.BookFileCore) { c.FileSize = 4242 }, true},
		{"scan mtime changed", func(c *database.BookFileCore) { m := int64(7); c.LastScanMtime = &m }, true},
		{"scan mtime cleared", func(c *database.BookFileCore) { c.LastScanMtime = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			mtime := int64(1700000000)
			f1 := database.BookFileCore{ID: "f1", BookID: "b1", FileSize: 14, LastScanMtime: &mtime,
				FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")}
			h := newHBStore(t, f1)
			const opID = "01REPLAYCOMPARES"
			errKill := errors.New("killed")
			restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
				if stage == "after-journal" {
					return errKill
				}
				return nil
			})
			_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
			restore()
			require.ErrorIs(t, err, errKill)
			h.edit("f1", tc.change)

			_, c, _ := runHB(t, h.store(), hbRun{opID: opID})

			if tc.busy {
				assert.Equal(t, hbCounts{"busy": 1}, c)
				assert.Empty(t, h.hashed())
			} else {
				assert.Equal(t, hbCounts{"hashed": 1}, c)
				assert.Equal(t, []string{"f1"}, h.hashed())
			}
		})
	}
}

const hbReportHeaderLine = "bucket\tfile_id\tbook_id\tpath\treason\n"

func hbPlantReport(t *testing.T, root, opID, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	p := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, os.WriteFile(p, []byte(hbReportHeaderLine+body), 0o640))
	return p
}

// The report sits in a directory other users may write, so whatever it says can
// only make the job SKIP and COUNT a row. A forged line naming a row (or a row
// that does not exist) never causes a hash write: the job stores nothing for the
// rows the report lists, and never a value taken from the report.
func TestBackfillFileHashes_ForgedReportLinesNeverCauseAHashWrite(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "gone.m4b")},
	)
	const opID = "01FORGEDREPORT"
	forgedHash := "deadbeef" + strings.Repeat("0", 56)
	hbPlantReport(t, root, opID,
		"hashed\tf1\tb1\t"+hbPath(root, "Year One.m4b")+"\t"+forgedHash+"\n"+
			"hashed\tghost\tb9\t/etc/shadow\t"+forgedHash+"\n")

	_, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Empty(t, h.writes, "no hash write at all: the report only skips and counts")
	assert.Empty(t, h.hashCalls)
	for _, row := range h.files {
		assert.Empty(t, row.FileHash, "no row received a hash from the report")
	}
	assert.Equal(t, hbCounts{"hashed": 2, "not_found": 1}, c, "the forged lines were counted, the real row f2 was processed")
}

// An ID that cannot be a row ID (a path-looking string, spaces, a control
// character, too long) makes the report unreadable: the resume stops with an
// error and neither the report nor any row is touched.
func TestBackfillFileHashes_ReportWithAnInvalidIDIsRefused(t *testing.T) {
	type badID struct{ file, book string }
	for name, bad := range map[string]badID{
		"file path traversal": {"../../etc/passwd", "b1"},
		"file space":          {"f1 b2", "b1"},
		"file empty":          {"", "b1"},
		"file too long":       {strings.Repeat("A", 65), "b1"},
		"file control":        {"f1\x01", "b1"},
		"book path traversal": {"f8", "../../etc/passwd"},
		"book space":          {"f8", "b 1"},
		"book too long":       {"f8", strings.Repeat("B", 65)},
	} {
		t.Run(name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
			const opID = "01BADIDREPORT"
			body := "not-found\t" + bad.file + "\t" + bad.book + "\t/lib/x\tfile not found\n" + "not-found\tf9\tb1\t/lib/y\tfile not found\n"
			report := hbPlantReport(t, root, opID, body)
			before, err := os.ReadFile(report)
			require.NoError(t, err)

			_, rerr := hbRunJob(t, h.store(), hbRun{opID: opID})

			require.Error(t, rerr)
			assert.Contains(t, rerr.Error(), "invalid file or book ID")
			assert.Empty(t, h.writes)
			after, err := os.ReadFile(report)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the report is left as it was")
		})
	}
}

// The old chunk journal was a file next to the report. A file planted there is
// no longer read by anything: forged entries in it cannot reach the store.
func TestBackfillFileHashes_APlantedJournalFileIsIgnored(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
	const opID = "01PLANTEDJOURNAL"
	reports := filepath.Join(root, ".reports")
	require.NoError(t, os.MkdirAll(reports, 0o755))
	forged, err := json.Marshal(map[string]any{"bucket": "hashed", "reason": "", "hash": "forged-hash", "file_id": "f1", "book_id": "b1", "path": hbPath(root, "Year One.m4b")})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(reports, "backfill-file-hashes-"+opID+".tsv.pending"), append(forged, '\n'), 0o664))

	_, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"hashed": 1}, c)
	assert.Equal(t, hbSum("year one bytes"), h.hashCalls["f1"], "the hash is the one computed from the file, not the planted one")
}

// A symlink planted at the report's path is refused, not followed: nothing is
// written through it.
func TestBackfillFileHashes_ReportSymlinkIsNotFollowed(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})
	const opID = "01SYMLINKREPORT"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	victim := filepath.Join(t.TempDir(), "precious.txt")
	require.NoError(t, os.WriteFile(victim, []byte("precious"), 0o600))
	require.NoError(t, os.Symlink(victim, filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")))

	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "open report")
	got, rerr := os.ReadFile(victim)
	require.NoError(t, rerr)
	assert.Equal(t, "precious", string(got))
}

// The job creates the reports directory and the report without group write and
// without any access for others, whatever the umask.
func TestBackfillFileHashes_CreatesTheReportWithoutGroupWriteOrWorldAccess(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "gone.m4b")})

	_, _, report := runHB(t, h.store(), hbRun{opID: "01PERMS"})

	dir, err := os.Stat(filepath.Dir(report))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), dir.Mode().Perm())
	file, err := os.Stat(report)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), file.Mode().Perm())
}

// The chunk journal is in the operation's state in the database. A failed
// journal write stops the run before any hash is stored.
func TestBackfillFileHashes_JournalWriteFailureStopsBeforeAnyStore(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
	h.journalSaveErr = errors.New("state store unavailable")

	_, err := hbRunJob(t, h.store(), hbRun{opID: "01JOURNALWRITEFAILS"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunk journal")
	assert.Empty(t, h.writes, "no hash is stored without its journal")
}

// A journal that cannot be read on resume stops the run: proceeding could skip
// rows whose hashes the dead run stored without reporting them.
func TestBackfillFileHashes_JournalReadFailureStopsTheResume(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
	const opID = "01JOURNALREADFAILS"
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, _ int) error {
		if stage == "after-store" {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)
	h.journalGetErr = errors.New("state store unavailable")

	_, rerr := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, rerr)
	assert.Contains(t, rerr.Error(), "chunk journal")
}

// A journal that is not this operation's, or holds an entry that cannot be a
// row, is refused: it is the only source of the hashes a replay stores.
func TestBackfillFileHashes_UnusableJournalIsRefused(t *testing.T) {
	const opID = "01UNUSABLEJOURNAL"
	entry := func(mut func(*map[string]any)) map[string]any {
		e := map[string]any{"bucket": "hashed", "reason": "", "hash": "abc", "file_id": "f1", "book_id": "b1", "path": "/lib/Gotham/Year One.m4b"}
		if mut != nil {
			mut(&e)
		}
		return e
	}
	for name, j := range map[string]map[string]any{
		"another operation": {"version": 1, "op_id": "01SOMEONEELSE", "entries": []any{entry(nil)}},
		"wrong version":     {"version": 2, "op_id": opID, "entries": []any{entry(nil)}},
		"unknown bucket":    {"version": 1, "op_id": opID, "entries": []any{entry(func(e *map[string]any) { (*e)["bucket"] = "root-shell" })}},
		"bad file id":       {"version": 1, "op_id": opID, "entries": []any{entry(func(e *map[string]any) { (*e)["file_id"] = "../x" })}},
		"bad book id":       {"version": 1, "op_id": opID, "entries": []any{entry(func(e *map[string]any) { (*e)["book_id"] = "b 1" })}},
	} {
		t.Run(name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")})
			blob, err := json.Marshal(j)
			require.NoError(t, err)
			h.state[opID+":params"] = blob

			_, rerr := hbRunJob(t, h.store(), hbRun{opID: opID})

			require.Error(t, rerr)
			assert.Contains(t, rerr.Error(), "chunk journal")
			assert.Empty(t, h.writes)
		})
	}
}
