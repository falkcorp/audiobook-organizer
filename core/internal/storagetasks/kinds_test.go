// file: core/internal/storagetasks/kinds_test.go
// version: 1.0.3
// guid: 2a99dbbf-d3e7-4586-b768-b26eb0d5d60c
// last-edited: 2026-11-18

package storagetasks

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// erasedTask is the executor's view of a definition (tasks.ExecutorRunners).
type erasedTask interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

// runKind runs def's task body the way the executor does, over host, and
// returns the stored result.
func runKind[R any](t *testing.T, def tasks.Definition, host tasks.RunHost) (R, error) {
	t.Helper()
	var res R
	e, ok := def.(erasedTask)
	if !ok {
		t.Fatalf("%T does not expose its runners", def)
	}
	decode, task, _, _ := e.ExecutorRunners()
	if task == nil || task.Run == nil {
		t.Fatalf("%s has no task body", def.Kind())
	}
	p, err := decode(nil)
	if err != nil {
		t.Fatalf("decode params: %v", err)
	}
	raw, err := task.Run(tasks.NewRC(host), p)
	if raw != nil {
		if jerr := json.Unmarshal(raw, &res); jerr != nil {
			t.Fatalf("result %s: %v", raw, jerr)
		}
	}
	return res, err
}

// host is a FakeRun that records what the liveness-bearing calls were: Touch
// (a bare stamp) and Note (a status line, which stamps too), and every
// record the run's logger received.
type host struct {
	*taskstest.FakeRun
	mu    sync.Mutex
	notes []string
	touch int
	lines []string
}

func newHost(ctx context.Context, kind tasks.Kind) *host {
	return &host{FakeRun: taskstest.NewFakeRun(ctx, kind)}
}

func (h *host) Note(msg string) { h.mu.Lock(); h.notes = append(h.notes, msg); h.mu.Unlock() }
func (h *host) Touch()          { h.mu.Lock(); h.touch++; h.mu.Unlock() }
func (h *host) Log() *slog.Logger {
	return slog.New(&lineHandler{h: h})
}

func (h *host) stamps() (notes, touches int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.notes), h.touch
}

func (h *host) logged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.lines)
}

type lineHandler struct{ h *host }

func (lineHandler) Enabled(context.Context, slog.Level) bool { return true }
func (l *lineHandler) Handle(_ context.Context, r slog.Record) error {
	l.h.mu.Lock()
	l.h.lines = append(l.h.lines, r.Message)
	l.h.mu.Unlock()
	return nil
}
func (l *lineHandler) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *lineHandler) WithGroup(string) slog.Handler      { return l }

// fakeSurface is the Surface the kinds run over. Compact blocks for compactFor
// (or until its context ends) and then returns compactErr; stats is read once
// per sample, in order, the last repeated.
type fakeSurface struct {
	compactFor time.Duration
	compactErr error
	stats      []storage.CompactionStats
	census     []storage.CensusEntry
	censusErr  error
	// cancelDuringCensus cancels the run while Census is counting, then
	// returns the entries anyway: a count that finished as the run was stopped.
	cancelDuringCensus context.CancelFunc

	mu        sync.Mutex
	samples   int
	compacted [][2][]byte
}

func (f *fakeSurface) Compact(ctx context.Context, start, end []byte) error {
	f.mu.Lock()
	f.compacted = append(f.compacted, [2][]byte{start, end})
	f.mu.Unlock()
	select {
	case <-time.After(f.compactFor):
	case <-ctx.Done():
		return ctx.Err()
	}
	return f.compactErr
}

func (f *fakeSurface) CompactionStats() storage.CompactionStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.samples, len(f.stats)-1)
	f.samples++
	if i < 0 {
		return storage.CompactionStats{}
	}
	return f.stats[i]
}

func (f *fakeSurface) Census(ctx context.Context) ([]storage.CensusEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.cancelDuringCensus != nil {
		f.cancelDuringCensus()
	}
	return slices.Clone(f.census), f.censusErr
}

// memSink is a CensusSink that keeps what it was given.
type memSink struct {
	mu   sync.Mutex
	got  []Census
	fail error
}

func (s *memSink) Put(_ context.Context, c Census) error {
	if s.fail != nil {
		return s.fail
	}
	s.mu.Lock()
	s.got = append(s.got, c)
	s.mu.Unlock()
	return nil
}

func (s *memSink) puts() []Census {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

func fastOptions() Options {
	return Options{Interval: 5 * time.Millisecond, Now: func() time.Time { return time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC) }}
}

func kindsOf(t *testing.T, s Surface, sink CensusSink) (compact, census tasks.Definition) {
	t.Helper()
	return CompactTask(s, fastOptions()), CensusTask(s, sink, fastOptions())
}

// Both kinds pass the shared conformance suite (invariant 9).
func TestStorageKinds_Conformance(t *testing.T) {
	compact, census := kindsOf(t, &fakeSurface{}, &memSink{})
	taskstest.Conformance(t, compact, census)
	if compact.Kind() != KindCompact || census.Kind() != KindCensus {
		t.Errorf("kinds = %s, %s", compact.Kind(), census.Kind())
	}
}

// Invariant 4: compaction shares its key with backup, is one blocking pass,
// writes core storage only, and is scheduled from the declared key into the
// window, in the live mode (a scheduled writer states its mode, D8).
func TestStorageKinds_CompactHoldsExclusiveKey(t *testing.T) {
	compact, _ := kindsOf(t, &fakeSurface{}, &memSink{})
	if compact.Shape() != tasks.ShapeTask {
		t.Errorf("shape = %v, want ShapeTask", compact.Shape())
	}
	c := tasks.CommonOf(compact)
	if c.ConcurrencyKey != KeyStorageExclusive || KeyStorageExclusive != "core.storage.exclusive" {
		t.Errorf("ConcurrencyKey = %q (const %q), want core.storage.exclusive", c.ConcurrencyKey, KeyStorageExclusive)
	}
	if !c.Effects.Declared() || c.Effects.IsReadOnly() ||
		!slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "storage")}) ||
		len(c.Effects.ReadSet()) != 0 || len(c.Effects.DeleteSet()) != 0 {
		t.Errorf("Effects: writes %v reads %v deletes %v, want one write on core/storage",
			c.Effects.WriteSet(), c.Effects.ReadSet(), c.Effects.DeleteSet())
	}
	if c.Schedule == nil || c.Schedule.ConfigKey() != "core.storage.schedules.compact" || CompactScheduleKey != "core.storage.schedules.compact" {
		t.Fatalf("Schedule = %+v, want ScheduleFromConfig(core.storage.schedules.compact)", c.Schedule)
	}
	if !c.Schedule.IsLive() || c.Schedule.IsPreview() || c.Schedule.RunsOnStart() {
		t.Errorf("Schedule live=%v preview=%v onStart=%v, want live only", c.Schedule.IsLive(), c.Schedule.IsPreview(), c.Schedule.RunsOnStart())
	}
	if c.Resume != tasks.ResumeDrop || c.Priority != tasks.PriorityLow || c.Timeout != 2*time.Hour {
		t.Errorf("Resume %v Priority %v Timeout %v, want ResumeDrop, PriorityLow, 2h", c.Resume, c.Priority, c.Timeout)
	}
	// A scheduled kind is started by the system actor, which the executor
	// strips of every AuthRequired scope (SEC2-15): core:system.manage would
	// leave it unable to fire on its schedule.
	if got := c.Perms.Start.Permissions(); !slices.Equal(got, []rbac.Permission{"core:tasks.manage"}) {
		t.Errorf("Perms.Start = %v, want core:tasks.manage (reachable by the system actor)", got)
	}
}

// The census shares the key (it is the other store-wide pass), reads core
// storage and writes its record there, has no timer (an operator runs it),
// and declares the silence of its one blocking call.
func TestStorageKinds_CensusDeclaration(t *testing.T) {
	_, census := kindsOf(t, &fakeSurface{}, &memSink{})
	if census.Shape() != tasks.ShapeTask {
		t.Errorf("shape = %v, want ShapeTask", census.Shape())
	}
	c := tasks.CommonOf(census)
	if c.ConcurrencyKey != KeyStorageExclusive {
		t.Errorf("ConcurrencyKey = %q, want %q", c.ConcurrencyKey, KeyStorageExclusive)
	}
	if !slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "storage")}) {
		t.Errorf("writes = %v, want core/storage", c.Effects.WriteSet())
	}
	if c.Schedule != nil {
		t.Errorf("Schedule = %+v, want none", c.Schedule)
	}
	if got := c.Perms.Start.Permissions(); !slices.Equal(got, []rbac.Permission{"core:system.manage"}) {
		t.Errorf("Perms.Start = %v, want core:system.manage (operator-run storage administration)", got)
	}
	if c.Resume != tasks.ResumeFromZero {
		t.Errorf("Resume = %v, want ResumeFromZero (a pass recounts and overwrites)", c.Resume)
	}
	if c.Timeout != CensusTimeout || c.ProgressTimeout != CensusTimeout {
		t.Errorf("Timeout %v ProgressTimeout %v, want both %v: Admin.Census reports no progress, so the watchdog gap is the attempt's own limit", c.Timeout, c.ProgressTimeout, CensusTimeout)
	}
}

// A surface that is nil yields definitions with no body, which registration
// refuses (fail closed).
func TestStorageKinds_NilSurfaceFailsClosed(t *testing.T) {
	for _, d := range []tasks.Definition{CompactTask(nil, Options{}), CensusTask(nil, nil, Options{})} {
		if len(tasks.Problems(d)) == 0 {
			t.Errorf("%s: no problem reported for a nil surface", d.Kind())
		}
	}
	// A census with no sink has nowhere to record its result.
	_, census := kindsOf(t, &fakeSurface{}, nil)
	if len(tasks.Problems(census)) == 0 {
		t.Errorf("%s: no problem reported for a nil sink", census.Kind())
	}
}

func statsMoving() []storage.CompactionStats {
	return []storage.CompactionStats{
		{LiveTableSize: 100, DiskSpaceUsage: 100},
		{LiveTableSize: 100, DiskSpaceUsage: 120, InProgressBytes: 10, NumInProgress: 1, EstimatedDebt: 50},
		{LiveTableSize: 100, DiskSpaceUsage: 140, InProgressBytes: 30, NumInProgress: 1, EstimatedDebt: 50},
		{LiveTableSize: 80, DiskSpaceUsage: 140, CompletedBytes: 60, Count: 1, EstimatedDebt: 10},
	}
}

func runCompact(t *testing.T, s *fakeSurface) (*host, CompactResult, error) {
	t.Helper()
	compact, _ := kindsOf(t, s, &memSink{})
	h := newHost(t.Context(), KindCompact)
	res, err := runKind[CompactResult](t, compact, h)
	return h, res, err
}

// The pass compacts the whole key range, once.
func TestCompact_CompactsTheWholeKeyRange(t *testing.T) {
	s := &fakeSurface{}
	if _, _, err := runCompact(t, s); err != nil {
		t.Fatal(err)
	}
	if len(s.compacted) != 1 || s.compacted[0][0] != nil || s.compacted[0][1] != nil {
		t.Errorf("Compact calls = %v, want one over (nil, nil)", s.compacted)
	}
}

// HEAD's heartbeat rule: a status line that stamps liveness goes out only
// when an engine counter moved, so a wedged compaction is still caught.
func TestCompact_ReportsWhenCompactionIsMakingProgress(t *testing.T) {
	s := &fakeSurface{compactFor: 120 * time.Millisecond, stats: statsMoving()}
	h, _, err := runCompact(t, s)
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := h.stamps()
	if notes == 0 {
		t.Fatalf("no liveness-bearing status line while the counters moved; log: %v", h.logged())
	}
}

func TestCompact_SaysNothingWhenCountersAreFlat(t *testing.T) {
	flat := storage.CompactionStats{LiveTableSize: 100, DiskSpaceUsage: 100, InProgressBytes: 5, NumInProgress: 1, EstimatedDebt: 50}
	s := &fakeSurface{compactFor: 80 * time.Millisecond, stats: []storage.CompactionStats{flat}}
	h, _, err := runCompact(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if notes, touches := h.stamps(); notes != 0 || touches != 0 {
		t.Errorf("a flat engine stamped liveness: %d notes, %d touches", notes, touches)
	}
	// The sampler still logs every tick: logs do not stamp liveness.
	var ticks int
	for _, l := range h.logged() {
		if strings.Contains(l, "elapsed") {
			ticks++
		}
	}
	if ticks == 0 {
		t.Errorf("no per-tick log line from a running compaction; log: %v", h.logged())
	}
}

func TestCompact_ReportsOnCompletedCompactionsAlone(t *testing.T) {
	// No bytes in progress, no debt change: only Count rose.
	s := &fakeSurface{compactFor: 80 * time.Millisecond, stats: []storage.CompactionStats{
		{EstimatedDebt: 10, DiskSpaceUsage: 10},
		{EstimatedDebt: 10, DiskSpaceUsage: 10, Count: 1},
	}}
	h, _, err := runCompact(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if notes, _ := h.stamps(); notes == 0 {
		t.Errorf("a completed compaction did not stamp liveness; log: %v", h.logged())
	}
}

func TestCompact_PropagatesCompactError(t *testing.T) {
	boom := errors.New("batcave flooded")
	_, _, err := runCompact(t, &fakeSurface{compactErr: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestCompact_ReturnsImmediatelyWhenCompactionIsFast(t *testing.T) {
	// An interval far longer than the call: the sampler must not delay the return.
	compactDef := CompactTask(&fakeSurface{}, Options{Interval: time.Hour})
	h := newHost(t.Context(), KindCompact)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := runKind[CompactResult](t, compactDef, h); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a fast compaction waited for the sampling interval")
	}
}

// A canceled run stops the compaction through its context.
func TestCompact_CancelStopsTheCompaction(t *testing.T) {
	s := &fakeSurface{compactFor: time.Hour}
	compact, _ := kindsOf(t, s, &memSink{})
	ctx, cancel := context.WithCancel(t.Context())
	h := newHost(ctx, KindCompact)
	var runErr atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := runKind[CompactResult](t, compact, h)
		runErr.Store(&err)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the compaction")
	}
	if p, _ := runErr.Load().(*error); p == nil || !errors.Is(*p, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", p)
	}
}

func entries() []storage.CensusEntry {
	return []storage.CensusEntry{
		{Owner: "core/tasks", Start: []byte("c/tasks/"), End: []byte("c/tasks0"), Keys: 7, Bytes: 700},
		{Owner: "books", Start: []byte("m/books/"), End: []byte("m/books0"), Mode: storage.ClaimReadWrite, Keys: 3, Bytes: 90},
		{Start: []byte("c/"), End: []byte("c0"), Keys: 2, Bytes: 10},
	}
}

// The census records every partition the surface counted, with totals, at the
// run's clock, and returns the totals.
func TestCensus_RecordsPartitionsAndTotals(t *testing.T) {
	sink := &memSink{}
	s := &fakeSurface{census: entries()}
	_, census := kindsOf(t, s, sink)
	res, err := runKind[CensusResult](t, census, newHost(t.Context(), KindCensus))
	if err != nil {
		t.Fatal(err)
	}
	if res.Partitions != 3 || res.Keys != 12 || res.Bytes != 800 {
		t.Errorf("result = %+v, want 3 partitions, 12 keys, 800 bytes", res)
	}
	got := sink.puts()
	if len(got) != 1 {
		t.Fatalf("sink got %d records, want 1", len(got))
	}
	c := got[0]
	if !c.At.Equal(fastOptions().Now()) || c.Keys != 12 || c.Bytes != 800 || len(c.Partitions) != 3 {
		t.Errorf("record = %+v", c)
	}
	p := c.Partitions[1]
	if p.Owner != "books" || p.Mode != "read-write" || string(p.Start) != "m/books/" || p.Keys != 3 || p.Bytes != 90 {
		t.Errorf("partition 1 = %+v", p)
	}
	if got := c.Partitions[2]; got.Owner != "" || got.Mode != "" {
		t.Errorf("an unowned, unclaimed partition carries owner %q mode %q, want none", got.Owner, got.Mode)
	}
}

// A failed count records nothing: the last good census stays the latest.
func TestCensus_FailedCountRecordsNothing(t *testing.T) {
	boom := errors.New("iterator wedged")
	sink := &memSink{}
	_, census := kindsOf(t, &fakeSurface{censusErr: boom}, sink)
	if _, err := runKind[CensusResult](t, census, newHost(t.Context(), KindCensus)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if n := len(sink.puts()); n != 0 {
		t.Errorf("sink got %d records after a failed count", n)
	}
}

func TestCensus_SinkFailureFailsTheRun(t *testing.T) {
	boom := errors.New("disk full")
	_, census := kindsOf(t, &fakeSurface{census: entries()}, &memSink{fail: boom})
	if _, err := runKind[CensusResult](t, census, newHost(t.Context(), KindCensus)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestCensus_CanceledRunRecordsNothing(t *testing.T) {
	sink := &memSink{}
	_, census := kindsOf(t, &fakeSurface{census: entries()}, sink)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runKind[CensusResult](t, census, newHost(ctx, KindCensus)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(sink.puts()); n != 0 {
		t.Errorf("sink got %d records from a canceled run", n)
	}
}

// A run canceled while the count was finishing must not record it: the last
// good census stays the latest.
func TestCensus_CanceledWhileCountingRecordsNothing(t *testing.T) {
	sink := &memSink{}
	ctx, cancel := context.WithCancel(t.Context())
	_, census := kindsOf(t, &fakeSurface{census: entries(), cancelDuringCensus: cancel}, sink)
	if _, err := runKind[CensusResult](t, census, newHost(ctx, KindCensus)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(sink.puts()); n != 0 {
		t.Errorf("sink got %d records from a run canceled while counting", n)
	}
}

// openStore opens a real store on MemFS with core storage's own keyspace and
// returns its admin surface and the keyspace c/storage/.
func openStore(t *testing.T) (*storage.Admin, storage.Keyspace) {
	t.Helper()
	ctx := t.Context()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            "/batcave/main.pebble",
		Build:           "storagetasks+test",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace("core/storage")
	if !ok {
		t.Fatal("no core/storage keyspace")
	}
	return db.Admin(), ks
}

// The real census, run through the kind into the real c/storage/census/
// record: every key of the store is counted exactly once, and the latest
// record reads back.
func TestCensus_CountsEveryKeyOnceIntoTheStore(t *testing.T) {
	ctx := t.Context()
	admin, ks := openStore(t)
	for _, k := range []string{"wayne", "grayson", "todd", "drake"} {
		if err := ks.Set(ctx, []byte("probe/"+k), []byte("batman-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	var want uint64
	for _, err := range ks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		want++
	}
	store := NewCensusStore(ks)
	censusDef := CensusTask(admin, store, fastOptions())
	res, err := runKind[CensusResult](t, censusDef, newHost(ctx, KindCensus))
	if err != nil {
		t.Fatal(err)
	}
	// The census counts the whole store, which holds more than this keyspace
	// (the level stamp and sidecars live elsewhere), and it counts the keys
	// of c/storage/ before its own record exists.
	if res.Keys < want {
		t.Errorf("census counted %d keys, fewer than the %d of c/storage/ alone", res.Keys, want)
	}
	rec, ok, err := store.Latest(ctx)
	if err != nil || !ok {
		t.Fatalf("Latest = %v, %v", ok, err)
	}
	var sum uint64
	for _, p := range rec.Partitions {
		sum += p.Keys
	}
	if sum != rec.Keys || rec.Keys != res.Keys || len(rec.Partitions) != res.Partitions {
		t.Errorf("record keys %d (partitions sum %d) vs result %+v", rec.Keys, sum, res)
	}
	// A second pass counts its predecessor's record: the record is one row,
	// overwritten, never accumulated.
	res2, err := runKind[CensusResult](t, censusDef, newHost(ctx, KindCensus))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Keys != res.Keys+1 {
		t.Errorf("second census counted %d keys, want %d (the first pass's one record)", res2.Keys, res.Keys+1)
	}
}

func TestCensusStore_LatestOfNothingIsAbsent(t *testing.T) {
	_, ks := openStore(t)
	if _, ok, err := NewCensusStore(ks).Latest(t.Context()); err != nil || ok {
		t.Fatalf("Latest of an empty store = %v, %v; want absent", ok, err)
	}
}
