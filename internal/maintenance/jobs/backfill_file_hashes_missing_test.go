// file: internal/maintenance/jobs/backfill_file_hashes_missing_test.go
// version: 3.1.0
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
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
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

// hbOutcomeKeys are the six row-outcome counters; every un-hashed row the run
// reaches lands in exactly one of them.
var hbOutcomeKeys = []string{
	"hashed", "not_found", "unverified_outside_root", "read_errors", "write_errors", "skipped_no_path",
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
// than SetBookFileHash fails the test: the job writes nothing for a file that is
// not on disk, and nothing but a hash for one that is.
type hbStore struct {
	t *testing.T

	mu        sync.Mutex
	files     []database.BookFileCore
	hashCalls map[string]string // id -> hash
	failHash  func(id string) error
	state     map[string][]byte
	phases    []string
}

func newHBStore(t *testing.T, files ...database.BookFileCore) *hbStore {
	return &hbStore{t: t, files: files, hashCalls: map[string]string{}, state: map[string][]byte{}}
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
		SetBookFileHashFunc: func(id, hash string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.failHash != nil {
				if err := h.failHash(id); err != nil {
					return err
				}
			}
			h.hashCalls[id] = hash
			return nil
		},
		UpdateBookFileFunc: func(string, *database.BookFile) error { forbid("UpdateBookFile"); return nil },
		DeleteBookFileFunc: func(string) error { forbid("DeleteBookFile"); return nil },
		PatchBookFileFieldsFunc: func(string, string, database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error) {
			forbid("PatchBookFileFields")
			return nil, nil, nil
		},
		ModifyBookFileFunc: func(string, string, func(*database.BookFile) error) (*database.BookFile, error) {
			forbid("ModifyBookFile")
			return nil, nil
		},
		BatchUpsertBookFilesFunc: func([]*database.BookFile) error { forbid("BatchUpsertBookFiles"); return nil },
		UpdateBookFunc: func(string, *database.Book) (*database.Book, error) {
			forbid("UpdateBook")
			return nil, nil
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
	assert.Contains(t, rep.logs[len(rep.logs)-1], "Dry run, nothing written")
	assert.Equal(t, map[string][]string{"not-found": {"f1"}}, hbBuckets(hbReadReport(t, report)))
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
	assert.Equal(t, map[string][]string{"write-error": {"f1"}}, hbBuckets(hbReadReport(t, report)))
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
// the report: the run hashes as it always did, without one.
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

	assert.Equal(t, hbCounts{"hashed": 1, "not_found": 1}, c)
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

// A resumed run appends to its operation's report, and the checkpoint phase is
// the one this job has always written.
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
	data, err := json.Marshal(operations.OperationState{OperationID: opID, Phase: "scanning", PhaseIndex: 1, PhaseTotal: 2, UpdatedAt: time.Now()})
	require.NoError(t, err)
	h.state[opID] = data

	_, c, report := runHB(t, st, hbRun{opID: opID})

	assert.Equal(t, hbCounts{"not_found": 1}, c, "only the second row is processed")
	assert.Equal(t, reportFile, report)
	assert.Equal(t, map[string][]string{"not-found": {"f1", "f2"}}, hbBuckets(hbReadReport(t, report)), "the first part's row is kept")
	assert.Contains(t, h.phases, "scanning@2")
}

// The report is the run's deliverable. When it cannot be written the run stops
// with an error instead of checkpointing past rows whose report lines were lost.
// The report path is a FIFO whose reader goes away, so a flush fails with EPIPE;
// three chunks of rows make sure a flush happens after the reader has gone.
func TestBackfillFileHashes_ReportWriteFailureStopsTheRun(t *testing.T) {
	root := hbLibrary(t)
	files := make([]database.BookFileCore, 3*512)
	for i := range files {
		files[i] = database.BookFileCore{ID: fmt.Sprintf("f%04d", i), BookID: "b1", FilePath: hbPath(root, fmt.Sprintf("gone%04d.m4b", i))}
	}
	h := newHBStore(t, files...)
	const opID = "01BROKENREPORT"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o755))
	fifo := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	require.NoError(t, syscall.Mkfifo(fifo, 0o664))
	go func() { // the job's open blocks until a reader arrives; the reader leaves at once
		if f, err := os.Open(fifo); err == nil {
			_ = f.Close()
		}
	}()

	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "report")
	assert.Less(t, len(h.phases), 3, "the run stops instead of checkpointing every chunk")
}
