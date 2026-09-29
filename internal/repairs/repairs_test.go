// file: internal/repairs/repairs_test.go
// version: 1.3.0
// guid: e4b7c2a9-1d63-4f58-9a0e-8c3f6d2b7a41
// last-edited: 2026-09-29

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ---- fakes ----

type memStore struct {
	mu      sync.Mutex
	books   map[string]*database.Book
	files   map[string][]database.BookFile
	history []database.MetadataChangeRecord
	ops     map[string]*database.OperationV2Row
	// failHistory makes RecordMetadataChange fail for this field.
	failHistory string
}

func newMemStore() *memStore {
	return &memStore{books: map[string]*database.Book{}, files: map[string][]database.BookFile{},
		ops: map[string]*database.OperationV2Row{}}
}

func (s *memStore) add(id, title, path string, seriesID *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.books[id] = &database.Book{ID: id, Title: title, FilePath: path, SeriesID: seriesID}
}

func (s *memStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b
	return &cp, nil
}

func (s *memStore) GetBookFiles(id string) ([]database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookFile(nil), s.files[id]...), nil
}

func (s *memStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b
	if err := fn(&cp); err != nil {
		return nil, err
	}
	s.books[id] = &cp
	out := cp
	return &out, nil
}

func (s *memStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failHistory != "" && r.Field == s.failHistory {
		return errors.New("history store down")
	}
	s.history = append(s.history, *r)
	return nil
}

func (s *memStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ops[id], nil
}

func (s *memStore) title(id string) string {
	b, _ := s.GetBookByID(id)
	return b.Title
}

type nopReporter struct{}

func (nopReporter) UpdateProgress(_, _ int, _ string) error          { return nil }
func (nopReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (nopReporter) Logger() *slog.Logger                             { return slog.Default() }
func (nopReporter) Checkpoint(_ any) error                           { return nil }
func (nopReporter) IsCanceled() bool                                 { return false }
func (r nopReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (nopReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (nopReporter) SetCurrentItem(_ string)                          {}

// trimFixer proposes trimming whitespace from every title that has any. Its
// fingerprint is the current title, so any title change moves it.
type trimFixer struct {
	s       *memStore
	applied sync.Map
}

func (f *trimFixer) ID() string          { return "trim-titles" }
func (f *trimFixer) Title() string       { return "Trim titles" }
func (f *trimFixer) Description() string { return "test fixer" }

func (f *trimFixer) rowFor(id string) Row {
	t := f.s.title(id)
	r := Row{RowID: id, BookIDs: []string{id}, Title: t, Current: map[string]string{"title": t},
		Proposed: map[string]string{"title": strings.TrimSpace(t)}, Reason: "untrimmed", Risk: RiskLow,
		Fingerprint: "fp:" + t}
	if strings.TrimSpace(t) == t {
		r.Skipped, r.SkipReason = "already_clean", "nothing to trim"
	}
	return r
}

func (f *trimFixer) Plan(_ context.Context, _ json.RawMessage, _ registry.Reporter) ([]Row, error) {
	f.s.mu.Lock()
	var ids []string
	for id := range f.s.books {
		ids = append(ids, id)
	}
	f.s.mu.Unlock()
	var rows []Row
	for _, id := range ids {
		rows = append(rows, f.rowFor(id))
	}
	return rows, nil
}

func (f *trimFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	return f.rowFor(planned.RowID), nil
}

func (f *trimFixer) Apply(_ context.Context, w *Writer, fresh Row) error {
	want := fresh.Current["title"]
	_, err := w.Modify(fresh.RowID, func(b *database.Book) error {
		if b.Title != want {
			return ErrChangedSincePlan
		}
		b.Title = strings.TrimSpace(b.Title)
		return nil
	})
	if err == nil {
		f.applied.Store(fresh.RowID, true)
	}
	return err
}

// fakeStandDown is a scan controller whose scan parks on acquire.
type fakeStandDown struct {
	mu            sync.Mutex
	failAcquires  int // this many acquires fail before one succeeds
	acquires      int
	released      int
	renewsLeft    int // -1 = unlimited
	scanRunning   bool
	scanWasPaused bool
}

func (s *fakeStandDown) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	if holder == "" {
		return nil, errors.New("empty holder")
	}
	if s.failAcquires > 0 {
		s.failAcquires--
		return nil, errors.New("scan stand-down: scan did not park within 60s")
	}
	if s.scanRunning {
		s.scanRunning, s.scanWasPaused = false, true
	}
	return func() {
		s.mu.Lock()
		s.released++
		s.scanRunning = s.scanWasPaused
		s.mu.Unlock()
	}, nil
}

func (s *fakeStandDown) RenewScanStandDown(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewsLeft < 0 {
		return true
	}
	if s.renewsLeft == 0 {
		return false
	}
	s.renewsLeft--
	return true
}

func (s *fakeStandDown) ScanStandDownValid(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewsLeft != 0
}

var immediate = WaitOptions{Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}

func seed(s *memStore) {
	s.add("b1", " One ", "/lib/Author/One/one.m4b", nil)
	s.add("b2", " Two ", "/lib/Author/Two/two.m4b", nil)
	s.add("b3", "Three", "/lib/Author/Three/three.m4b", nil)
	s.add("b4", " iTunes Copy ", "/lib/Author/Four/four.m4b", nil)
	s.files["b4"] = []database.BookFile{{ID: "f4", BookID: "b4", FilePath: "/mnt/books/itunes/Music/four.m4b", Missing: true}}
	s.add("b5", " Big Finish Drama ", "/lib/Big Finish/Five/five.m4b", nil)
	dw := 7
	s.add("b6", " Series Guarded ", "/lib/Other/Six/six.m4b", &dw)
}

func seriesNamer(id int) string {
	if id == 7 {
		return "Doctor Who: The Eighth Doctor"
	}
	return ""
}

func planFor(t *testing.T, s *memStore, f Fixer) *PlanResult {
	t.Helper()
	res, err := RunPlan(context.Background(), f, nil, PlanDeps{Guard: s, Series: seriesNamer, Concurrency: 3}, nopReporter{})
	require.NoError(t, err)
	return res
}

func deps(s *memStore, sd StandDown) ApplyDeps {
	return ApplyDeps{Guard: s, Series: seriesNamer, StandDown: sd, OpID: "op-apply",
		Writer: NewWriter(s, s, "trim-titles", "bulk_update", "rp-"), Wait: immediate, Concurrency: 3}
}

// ---- plan + guards ----

func TestRunPlan_FrameworkGuardsSkipITunesAndOwnerManual(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	res := planFor(t, s, f)
	require.Equal(t, 6, res.Total)
	byID := map[string]Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.True(t, byID["b1"].Applicable())
	require.True(t, byID["b2"].Applicable())
	require.Equal(t, "already_clean", byID["b3"].Skipped)
	require.Equal(t, SkipITunes, byID["b4"].Skipped, "a MISSING book_file row under books/itunes still guards")
	require.Equal(t, SkipOwnerManual, byID["b5"].Skipped, "Big Finish by path")
	require.Equal(t, SkipOwnerManual, byID["b6"].Skipped, "Doctor Who by series name")
	require.Equal(t, 2, res.Applicable)
	require.Equal(t, map[string]int{"already_clean": 1, SkipITunes: 1, SkipOwnerManual: 2}, res.SkippedByKind)
	// Sorted by row id.
	for i := 1; i < len(res.Rows); i++ {
		require.Less(t, res.Rows[i-1].RowID, res.Rows[i].RowID)
	}
}

func TestGuardBookPaths_TorchwoodAndDoctorWhoSeparators(t *testing.T) {
	for _, p := range []string{"/lib/Torchwood/x.m4b", "/lib/Doctor_Who/x.m4b", "/lib/DoctorWho - Y/x.m4b"} {
		k, _ := GuardBookPaths("b", []string{p}, "")
		require.Equal(t, SkipOwnerManual, k, p)
	}
	k, _ := GuardBookPaths("b", nil, "Torchwood")
	require.Equal(t, SkipOwnerManual, k, "series alone, no paths")
	k, _ = GuardBookPaths("b", []string{"/lib/Author/Title/t.m4b"}, "Discworld")
	require.Empty(t, k)
}

// TestPathResolver_SymlinksIntoITunes (N6): a folder link, a file link and a
// missing file under a linked folder all resolve into books/itunes/**, and
// the folder is resolved once for the whole run.
func TestPathResolver_SymlinksIntoITunes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	itunes := filepath.Join(root, "books", "itunes", "Real")
	require.NoError(t, os.MkdirAll(itunes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itunes, "a.m4b"), []byte("x"), 0o644))
	lib := filepath.Join(root, "lib")
	require.NoError(t, os.MkdirAll(filepath.Join(lib, "Plain"), 0o755))
	require.NoError(t, os.Symlink(itunes, filepath.Join(lib, "Link")))
	require.NoError(t, os.Symlink(filepath.Join(itunes, "a.m4b"), filepath.Join(lib, "Plain", "file-link.m4b")))
	require.NoError(t, os.Symlink(filepath.Join(root, "nowhere.m4b"), filepath.Join(itunes, "dead.m4b")))

	res := NewPathResolver()
	for _, p := range []string{
		filepath.Join(lib, "Link", "a.m4b"),
		filepath.Join(lib, "Link", "gone.m4b"), // missing, folder is a link
		filepath.Join(lib, "Link", "dead.m4b"), // dangling link, folder is a link
		filepath.Join(lib, "Plain", "file-link.m4b"),
	} {
		k, why := GuardBookPathsWith(res, "b", []string{p}, "")
		require.Equal(t, SkipITunes, k, "%s: %s", p, why)
	}
	k, _ := GuardBookPathsWith(res, "b", []string{filepath.Join(lib, "Plain", "own.m4b")}, "")
	require.Empty(t, k, "a plain folder is not iTunes")
	res.mu.Lock()
	n := len(res.dirs)
	res.mu.Unlock()
	require.Equal(t, 2, n, "Link and Plain resolved once each")
	// Unresolvable: checked lexically only.
	k, _ = GuardBookPathsWith(res, "b", []string{"/nonexistent-192.0.2.1/books/itunes/x.m4b"}, "")
	require.Equal(t, SkipITunes, k)
}

// ---- paging ----

func TestPlanResult_Page(t *testing.T) {
	s := newMemStore()
	seed(s)
	res := planFor(t, s, &trimFixer{s: s})

	p, err := res.Page("op-plan", FilterAll, "", 0, 4)
	require.NoError(t, err)
	require.Equal(t, 6, p.Total)
	require.Len(t, p.Rows, 4)
	require.Equal(t, "b1", p.Rows[0].RowID)

	p, err = res.Page("op-plan", FilterAll, "", 4, 4)
	require.NoError(t, err)
	require.Len(t, p.Rows, 2)
	require.Equal(t, "b5", p.Rows[0].RowID)

	p, err = res.Page("op-plan", FilterAll, "", 10, 4)
	require.NoError(t, err)
	require.NotNil(t, p.Rows)
	require.Empty(t, p.Rows)

	p, err = res.Page("op-plan", FilterApplicable, "", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	p, err = res.Page("op-plan", FilterSkipped, "", 1, 50)
	require.NoError(t, err)
	require.Equal(t, 4, p.Total)
	require.Len(t, p.Rows, 3)

	// A class filter narrows within the filter; the in-filter tally counts
	// exactly the rows each class chip lists.
	res.Rows[0].Class, res.Rows[1].Class = "moved", "moved"
	p, err = res.Page("op-plan", FilterAll, "moved", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	require.Equal(t, 2, p.ByClassInFilter["moved"])
	for _, r := range p.Rows {
		require.Equal(t, "moved", r.Class)
	}

	// Every per-kind skip count pages exactly the rows it counts.
	sum := 0
	for kind, n := range res.SkippedByKind {
		p, err = res.Page("op-plan", FilterSkippedKindPrefix+kind, "", 0, 50)
		require.NoError(t, err)
		require.Equal(t, n, p.Total, kind)
		for _, r := range p.Rows {
			require.Equal(t, kind, r.Skipped)
		}
		sum += p.Total
	}
	require.Equal(t, 4, sum)

	_, err = res.Page("op-plan", "bogus", "", 0, 1)
	require.Error(t, err)
	_, err = res.Page("op-plan", FilterSkippedKindPrefix, "", 0, 1)
	require.Error(t, err, "an empty kind is not a filter")
}

func TestLoadPlan_ChecksDefStatusAndFixer(t *testing.T) {
	s := newMemStore()
	seed(s)
	res := planFor(t, s, &trimFixer{s: s})
	data, err := json.Marshal(res)
	require.NoError(t, err)
	str := string(data)
	s.ops["op-plan"] = &database.OperationV2Row{ID: "op-plan", DefID: PlanOpID, Status: "completed", ResultData: &str}
	s.ops["op-running"] = &database.OperationV2Row{ID: "op-running", DefID: PlanOpID, Status: "running"}
	s.ops["op-other"] = &database.OperationV2Row{ID: "op-other", DefID: "library.scan", Status: "completed", ResultData: &str}

	plan, err := LoadPlan(s, "op-plan", "trim-titles")
	require.NoError(t, err)
	require.Len(t, plan.Rows, 6)
	require.Equal(t, res.Rows[0].Fingerprint, plan.Rows[0].Fingerprint)

	_, err = LoadPlan(s, "op-plan", "other-fixer")
	require.ErrorIs(t, err, ErrPlanOtherFixer)
	_, err = LoadPlan(s, "op-running", "trim-titles")
	require.ErrorIs(t, err, ErrPlanNotComplete)
	_, err = LoadPlan(s, "op-other", "trim-titles")
	require.ErrorIs(t, err, ErrNotAPlan)
	_, err = LoadPlan(s, "op-missing", "trim-titles")
	require.ErrorIs(t, err, ErrPlanNotFound)
}

// ---- apply ----

func TestRunApply_AppliesSelectedRowsAndWritesHistory(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b3", "b4", "nope"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, []string{"nope"}, res.NotInPlan)
	require.Equal(t, map[string]int{OutcomeApplied: 1, OutcomeNotApplicable: 2}, res.ByOutcome)
	require.Equal(t, "One", s.title("b1"))
	require.Equal(t, " Two ", s.title("b2"), "a row not selected is untouched")
	require.Equal(t, " iTunes Copy ", s.title("b4"), "a guarded row is never written")

	// Every applied change has a history row, after the write.
	require.Len(t, s.history, 1)
	h := s.history[0]
	require.Equal(t, "b1", h.BookID)
	require.Equal(t, "title", h.Field)
	require.Equal(t, `" One "`, *h.PreviousValue)
	require.Equal(t, `"One"`, *h.NewValue)
	require.Equal(t, "trim-titles", h.Source)
	require.True(t, strings.HasPrefix(h.BatchID, "rp-"))
	require.Equal(t, 1, res.HistoryRows)
	require.Equal(t, 1, sd.released, "the stand-down is released")
}

// TestRunApply_CheckpointsAndResumes: every settled row is checkpointed, and
// a run resumed from that checkpoint reports those rows as they were without
// applying them again, and applies only the rest.
func TestRunApply_CheckpointsAndResumes(t *testing.T) {
	old := checkpointEvery
	checkpointEvery = 1
	t.Cleanup(func() { checkpointEvery = old })

	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	var mu sync.Mutex
	var last ApplyCheckpoint
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Checkpoint = func(cp ApplyCheckpoint) error {
		mu.Lock()
		defer mu.Unlock()
		last = cp
		return nil
	}
	_, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Len(t, last.Settled, 1)
	require.Equal(t, RowResult{RowID: "b1", Outcome: OutcomeApplied}, last.Settled[0])

	// Undo b1 by hand: a resumed run must NOT re-apply it.
	_, err = s.ModifyBook("b1", func(b *database.Book) error { b.Title = " One "; return nil })
	require.NoError(t, err)
	d2 := deps(s, &fakeStandDown{renewsLeft: -1})
	cp := last
	cp.Settled = append(cp.Settled, RowResult{RowID: "b2", Outcome: OutcomeAborted})
	d2.Resumed = &cp
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, d2, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, res.Applied, "b1 from the checkpoint, b2 (aborted, never written) applied now")
	require.Equal(t, " One ", s.title("b1"), "a checkpointed row is not applied again")
	require.Equal(t, "Two", s.title("b2"))
}

func TestRunApply_RefusesRowWhoseFingerprintChanged(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// Someone edits b2 after the plan.
	_, err := s.ModifyBook("b2", func(b *database.Book) error { b.Title = " Two (edited) "; return nil })
	require.NoError(t, err)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, 1, res.ChangedSincePlan)
	require.Equal(t, " Two (edited) ", s.title("b2"), "the changed row is left exactly as the other writer left it")
	for _, h := range s.history {
		require.NotEqual(t, "b2", h.BookID)
	}
}

func TestRunApply_ReGuardsOnFreshReads(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// After the plan, b1 gains a file under books/itunes.
	s.files["b1"] = []database.BookFile{{ID: "f1", BookID: "b1", FilePath: "/Volumes/books/iTunes/x/one.m4b"}}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, OutcomeGuarded, res.Rows[0].Outcome)
	require.Equal(t, SkipITunes, res.Rows[0].Skipped)
	require.Equal(t, " One ", s.title("b1"))
	require.Empty(t, s.history)
}

func TestRunApply_DryRunWritesNothingAndTakesNoStandDown(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, true, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, map[string]int{OutcomeWouldApply: 2}, res.ByOutcome)
	require.Equal(t, " One ", s.title("b1"))
	require.Zero(t, sd.acquires, "a preview never pauses the scan")
	require.Empty(t, s.history)
}

func TestRunApply_PausesARunningScanAndProceeds(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err, "a running scan is paused, never a refusal")
	require.Equal(t, 2, res.Applied)
	require.True(t, res.StandDownHeld)
	require.True(t, sd.scanWasPaused)
	require.True(t, sd.scanRunning, "the scan resumes on release")
}

func TestRunApply_AcquireFailureWaitsAndRetries(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, failAcquires: 2, scanRunning: true}
	var slept int
	d := deps(s, sd)
	d.Wait = WaitOptions{Sleep: func(context.Context, time.Duration) error { slept++; return nil }}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 3, sd.acquires)
	require.Equal(t, 2, slept)
	require.Equal(t, 1, res.Applied)
}

func TestRunApply_AcquireWaitEndsOnlyWithTheContext(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, failAcquires: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	d := deps(s, sd)
	tries := 0
	d.Wait = WaitOptions{Sleep: func(c context.Context, _ time.Duration) error {
		tries++
		if tries == 5 {
			cancel()
		}
		return c.Err()
	}}
	_, err := RunApply(ctx, f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 5, sd.acquires)
	require.Equal(t, " One ", s.title("b1"))
}

// touchReporter counts liveness stamps.
type touchReporter struct {
	nopReporter
	mu      sync.Mutex
	touches int
}

func (r *touchReporter) TouchLiveness() {
	r.mu.Lock()
	r.touches++
	r.mu.Unlock()
}

func (r *touchReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.touches
}

// blockingStandDown's acquire blocks until the test lets the scan park.
type blockingStandDown struct {
	fakeStandDown
	park chan struct{}
}

func (s *blockingStandDown) AcquireScanStandDown(ctx context.Context, holder, reason string) (func(), error) {
	select {
	case <-s.park:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeStandDown.AcquireScanStandDown(ctx, holder, reason)
}

// A scan that takes long to park is waited out in ONE attempt (no per-attempt
// timeout re-queueing it), with liveness stamped while the attempt blocks.
func TestAcquireStandDownWaiting_OneLongAttemptKeepsTheOpAlive(t *testing.T) {
	sd := &blockingStandDown{fakeStandDown: fakeStandDown{renewsLeft: -1, scanRunning: true}, park: make(chan struct{})}
	rep := &touchReporter{}
	done := make(chan error, 1)
	go func() {
		rel, held, err := AcquireStandDownWaiting(context.Background(), sd, "op-1", "test", rep,
			WaitOptions{TouchEvery: time.Millisecond, Sleep: immediate.Sleep})
		if err == nil && held {
			rel()
		}
		done <- err
	}()
	require.Eventually(t, func() bool { return rep.count() >= 3 }, 5*time.Second, time.Millisecond,
		"liveness is stamped while the acquire blocks")
	close(sd.park)
	require.NoError(t, <-done)
	sd.mu.Lock()
	defer sd.mu.Unlock()
	require.Equal(t, 1, sd.acquires, "exactly one attempt")
	require.True(t, sd.scanWasPaused)
}

// partialFixer writes, then reports the row only partly applied.
type partialFixer struct{ trimFixer }

func (f *partialFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	if err := f.trimFixer.Apply(ctx, w, fresh); err != nil {
		return err
	}
	return fmt.Errorf("%w: second half hit a change", ErrPartiallyApplied)
}

func TestRunApply_PartialIsNotReportedAsUnchanged(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &partialFixer{trimFixer{s: s}}
	plan := planFor(t, s, f)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Partial)
	require.Zero(t, res.ChangedSincePlan)
	require.Equal(t, OutcomePartial, res.Rows[0].Outcome)
	require.Contains(t, res.Rows[0].Error, "second half")
}

func TestRunApply_LeaseLapseAbortsRemainingRows(t *testing.T) {
	s := newMemStore()
	for i := 0; i < 5; i++ {
		s.add(fmt.Sprintf("r%d", i), fmt.Sprintf(" T%d ", i), fmt.Sprintf("/lib/A/T%d/t.m4b", i), nil)
	}
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// Two renewals succeed, then the lease is gone.
	sd := &fakeStandDown{renewsLeft: 2}
	d := deps(s, sd)
	d.Concurrency = 1
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"r0", "r1", "r2", "r3", "r4"}, false, d, nopReporter{})
	require.ErrorIs(t, err, ErrStandDownLost)
	require.NotNil(t, res, "the per-row report comes back with the abort")
	require.Equal(t, ErrStandDownLost.Error(), res.Aborted)
	require.Len(t, res.Rows, 5)
	require.Equal(t, 1, res.ByOutcome[OutcomeApplied], "one row renewed, passed the validity check and wrote")
	require.Equal(t, 4, res.ByOutcome[OutcomeAborted])
	written := 0
	for i := 0; i < 5; i++ {
		if s.title(fmt.Sprintf("r%d", i)) == fmt.Sprintf("T%d", i) {
			written++
		}
	}
	require.Equal(t, 1, written)
}

func TestRunApply_NoOpIDRefusesToWrite(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.OpID = ""
	_, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.ErrorIs(t, err, ErrNoHolderID)
	require.Equal(t, " One ", s.title("b1"))
}

func TestRunApply_NeedsRowIDs(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	_, err := RunApply(context.Background(), f, planFor(t, s, f), "op-plan", nil, false, deps(s, nil), nopReporter{})
	require.ErrorContains(t, err, "row_ids")
}

// ---- partitioning ----

func TestPartitionRows_SharedBooksNeverSplit(t *testing.T) {
	rows := []Row{
		{RowID: "a", BookIDs: []string{"1", "2"}},
		{RowID: "b", BookIDs: []string{"3"}},
		{RowID: "c", BookIDs: []string{"2", "4"}},
		{RowID: "d", BookIDs: []string{"4", "5"}},
		{RowID: "e", BookIDs: []string{"6"}},
	}
	parts := partitionRows(rows)
	owner := map[string]int{}
	for pi, part := range parts {
		for _, r := range part {
			for _, b := range r.BookIDs {
				if prev, ok := owner[b]; ok {
					require.Equal(t, prev, pi, "book %s in two partitions", b)
				}
				owner[b] = pi
			}
		}
	}
	require.Len(t, parts, 3)
	require.Equal(t, []string{"a", "c", "d"}, []string{parts[0][0].RowID, parts[0][1].RowID, parts[0][2].RowID})
}

// ---- writer ----

// TestWriter_HasNoDeletePrimitive pins the write surface a fixer gets: the
// framework must never offer a way to delete a book or a book_file row.
func TestWriter_HasNoDeletePrimitive(t *testing.T) {
	typ := reflect.TypeOf(&Writer{})
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		n := typ.Method(i).Name
		names = append(names, n)
		lower := strings.ToLower(n)
		require.False(t, strings.Contains(lower, "delete") || strings.Contains(lower, "remove") ||
			strings.Contains(lower, "purge"), "Writer exposes %s", n)
	}
	require.ElementsMatch(t, []string{"Modify", "Writes", "HistoryRows", "HistoryFailed",
		"WithJournal", "WithLiveness", "Touch", "Journal", "Journaled", "Step",
		"RepointBookFile", "MoveBookFiles", "SetTrackNumber", "Recompute"}, names)
}

func TestWriter_HistoryFailureWritesIncompleteMarker(t *testing.T) {
	s := newMemStore()
	s.add("b1", "Old", "/lib/a.m4b", nil)
	s.failHistory = "title"
	w := NewWriter(s, s, "src", "bulk_update", "rp-")
	changed, err := w.Modify("b1", func(b *database.Book) error {
		b.Title = "New"
		n := "Narr"
		b.Narrator = &n
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"title", "narrator"}, changed)
	require.Equal(t, 1, w.HistoryFailed())
	var marker bool
	for _, h := range s.history {
		if h.ChangeType == ChangeTypeApplyIncomplete {
			marker = true
		}
	}
	require.True(t, marker, "undo must see the batch is incomplete")
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	s := newMemStore()
	require.NoError(t, r.Register(&trimFixer{s: s}))
	require.Error(t, r.Register(&trimFixer{s: s}))
	f, ok := r.Get("trim-titles")
	require.True(t, ok)
	require.Equal(t, "trim-titles", f.ID())
	require.Len(t, r.List(), 1)
	_, ok = r.Get("nope")
	require.False(t, ok)
}
