// file: pkg/ops/ops_test.go
// version: 1.5.0
// guid: eaba1ee0-42be-4790-9727-c2d336c273de
// last-edited: 2026-10-14

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeHost records what a run asked of the executor. Children "run" by
// looking their outcome up in outcomes (keyed by def id).
type fakeHost struct {
	mu        sync.Mutex
	ckpt      json.RawMessage
	enqueued  []string // def ids in enqueue order
	params    map[string]json.RawMessage
	outcomes  map[string]Outcome
	notes     []string
	touches   int
	phases    []string
	enqueueTo func(def Definition) error
}

func (h *fakeHost) Progress(done int64, _ Total, note string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if note != "" {
		h.notes = append(h.notes, note)
	}
}
func (h *fakeHost) Touch()            { h.mu.Lock(); h.touches++; h.mu.Unlock() }
func (h *fakeHost) Phase(name string) { h.mu.Lock(); h.phases = append(h.phases, name); h.mu.Unlock() }
func (h *fakeHost) Checkpoint(s json.RawMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ckpt = s
	return nil
}
func (h *fakeHost) Restore() (json.RawMessage, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ckpt, h.ckpt != nil, nil
}
func (h *fakeHost) Enqueue(def Definition, params json.RawMessage, _ EnqueueConfig) (RunRef, error) {
	if h.enqueueTo != nil {
		if err := h.enqueueTo(def); err != nil {
			return RunRef{}, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enqueued = append(h.enqueued, def.ID())
	if h.params == nil {
		h.params = map[string]json.RawMessage{}
	}
	h.params[def.ID()] = params
	return RunRef{ID: "run-" + def.ID()}, nil
}
func (h *fakeHost) Await(_ context.Context, ref RunRef) (Outcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outcomes[strings.TrimPrefix(ref.ID, "run-")], nil
}
func (h *fakeHost) StandDown(context.Context) (Hold, error) { return nil, errors.New("no scan here") }

func newTestRC(h Host, mode Mode) *RC {
	return NewRC(RunEnv{Context: context.Background(), OpID: "op1", DefID: "def", Mode: mode, Host: h})
}

func readOnlyCommon(title string) Common {
	return Common{Title: title, Effects: ReadOnly(ResBooks), Permission: auth.PermLibraryView}
}

// runSet runs every item of a set in order, the way one worker runs a chunk.
func runSet(t *testing.T, rc *RC, set ItemSet) (skips []string, errs []error) {
	t.Helper()
	skips, errs, _ = runSetMarks(t, rc, set)
	return skips, errs
}

// runSetMarks is runSet that also folds rc.Mark calls, as the ledger does.
func runSetMarks(t *testing.T, rc *RC, set ItemSet) (skips []string, errs []error, marks map[string][]string) {
	t.Helper()
	marks = map[string][]string{}
	for i := 0; i < set.Len(); i++ {
		tally := &Tally{}
		err := set.Run(NewItemRC(rc, rc.Context(), tally, nil), i)
		skips = append(skips, tally.SkipReason())
		errs = append(errs, err)
		for _, m := range tally.Marks() {
			marks[m] = append(marks[m], set.Key(i))
		}
	}
	return skips, errs, marks
}

func TestCompileDefaults(t *testing.T) {
	task := Task("t.one", TaskSpec[NoParams, NoResult]{Common: readOnlyCommon("T"),
		Run: func(*RC, NoParams) (NoResult, error) { return NoResult{}, nil }})
	c := Compile(task)
	if c.Common.Exclusive != "t.one" || c.Common.Priority != PriorityNormal || c.Common.Timeout != 30*time.Minute {
		t.Fatalf("task defaults = %+v", c.Common)
	}
	if c.Resume != ResumeDrop || c.RunTask == nil || c.OpenBatch != nil {
		t.Fatalf("task compile = resume %v task %v batch %v", c.Resume, c.RunTask != nil, c.OpenBatch != nil)
	}
	b := Batch("b.one", BatchSpec[NoParams, string, NoResult]{Common: readOnlyCommon("B"),
		Source: func(*RC, NoParams) (Source[string], error) { return Source[string]{}, nil },
		Item:   func(*ItemRC, NoParams, string) error { return nil }})
	cb := Compile(b)
	if cb.Common.Timeout != 24*time.Hour || cb.Resume != ResumeContinue || cb.OpenBatch == nil {
		t.Fatalf("batch compile = %+v resume %v", cb.Common, cb.Resume)
	}
	if cb.Concurrency.Workers(nil) != runtime.NumCPU() {
		t.Fatalf("an omitted Concurrency must be CPU(), got %d workers", cb.Concurrency.Workers(nil))
	}
}

func TestConcurrencyWorkers(t *testing.T) {
	cases := []struct {
		c      Concurrency
		params string
		want   int
	}{
		{CPU(), "", runtime.NumCPU()},
		{Network(4, "rate limited"), "", 4},
		{Network(1000, "typo"), "", MaxNetworkWorkers},
		{Sequential("ordered"), "", 1},
		{FromParam("workers", 3, 8), `{}`, 3},
		{FromParam("workers", 3, 8), `{"workers":6}`, 6},
		{FromParam("workers", 3, 8), `{"workers":60}`, 8},
		{FromParam("workers", 3, 8), `{"workers":0}`, 1},
	}
	for _, tc := range cases {
		if got := tc.c.Workers(json.RawMessage(tc.params)); got != tc.want {
			t.Errorf("%+v with %s = %d, want %d", tc.c, tc.params, got, tc.want)
		}
	}
}

func TestBuildJobRefusesBeforeFirstItem(t *testing.T) {
	ran := false
	item := func(*ItemRC, NoParams, string) error { ran = true; return nil }
	key := func(s string) string { return s }
	cont := ResumeContinue
	cases := map[string]BatchSpec[NoParams, string, NoResult]{
		"partition on append-only": {Common: readOnlyCommon("x"), Item: item, PartitionBy: key,
			Source: func(*RC, NoParams) (Source[string], error) {
				return Source[string]{Items: []string{"a"}, Key: key, Order: AppendOnly}, nil
			}},
		"continue on unordered": {Common: Common{Title: "x", Effects: ReadOnly(), Permission: auth.PermLibraryView, Resume: &cont}, Item: item,
			Source: func(*RC, NoParams) (Source[string], error) {
				return Source[string]{Items: []string{"a"}, Key: key, Order: Unordered}, nil
			}},
		"no key": {Common: readOnlyCommon("x"), Item: item,
			Source: func(*RC, NoParams) (Source[string], error) { return Source[string]{Items: []string{"a"}}, nil }},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(Batch("b", spec)).OpenBatch(newTestRC(nil, Live), nil)
			if err == nil {
				t.Fatal("OpenBatch accepted it")
			}
		})
	}
	if ran {
		t.Fatal("an item ran")
	}
}

func TestDirtySetPartitionIsTheKey(t *testing.T) {
	def := Batch("d", BatchSpec[NoParams, string, NoResult]{Common: readOnlyCommon("d"),
		Source: func(*RC, NoParams) (Source[string], error) {
			return DirtySet("idx:x:dirty:", func(_ context.Context, ids []string) ([]string, error) {
				out := make([]string, len(ids))
				for i, id := range ids {
					out[i] = "book " + id
				}
				return out, nil
			}), nil
		},
		PartitionBy: func(s string) string { return "ignored" },
		Item:        func(*ItemRC, NoParams, string) error { return nil }})
	job, err := Compile(def).OpenBatch(newTestRC(nil, Live), nil)
	if err != nil {
		t.Fatalf("a DirtySet may partition: %v", err)
	}
	if job.Dirty == nil || job.Order != Unordered || job.Resume != ResumeFromZero {
		t.Fatalf("dirty job = %+v", job)
	}
	set, err := job.Dirty.Load(context.Background(), []string{"b1", "b2"})
	if err != nil {
		t.Fatal(err)
	}
	if set.Key(1) != "b2" || set.Partition(1) != "b2" || set.Label(0) != "b1" {
		t.Fatalf("key %q partition %q label %q", set.Key(1), set.Partition(1), set.Label(0))
	}
	if _, err := job.Dirty.Load(context.Background(), nil); err != nil {
		t.Fatalf("empty page: %v", err)
	}
}

func TestTaskCheckpointRestoreAndProgress(t *testing.T) {
	type state struct{ N int }
	h := &fakeHost{}
	def := Task("t", TaskSpec[struct{ Start int }, int]{Common: readOnlyCommon("t"),
		Run: func(rc *RC, p struct{ Start int }) (int, error) {
			var st state
			if ok, err := rc.Restore(&st); err != nil || ok {
				t.Fatalf("first attempt restored %v %v", ok, err)
			}
			end := rc.Phase("walk")
			rc.Progress().Note("walking")
			rc.Progress().Tick()
			end()
			return p.Start + 1, rc.Checkpoint(state{N: 7})
		}})
	out, err := Compile(def).RunTask(newTestRC(h, Preview), json.RawMessage(`{"Start":41,"dry_run":true}`))
	if err != nil || string(out) != "42" {
		t.Fatalf("result %s, %v", out, err)
	}
	var st state
	if ok, err := newTestRC(h, Preview).Restore(&st); !ok || err != nil || st.N != 7 {
		t.Fatalf("restore = %v %v %+v", ok, err, st)
	}
	if h.touches != 1 || !reflect.DeepEqual(h.phases, []string{"walk", ""}) || !reflect.DeepEqual(h.notes, []string{"walking"}) {
		t.Fatalf("host saw touches=%d phases=%v notes=%v", h.touches, h.phases, h.notes)
	}
}

func TestItemRCCountAndSkip(t *testing.T) {
	tally := &Tally{}
	irc := NewItemRC(newTestRC(nil, Live), context.Background(), tally, nil)
	irc.Count("indexed", 2)
	irc.Count("indexed", 1)
	irc.Skip("")
	irc.Mark("seen")
	irc.Mark("seen")
	if !slices.Equal(tally.Marks(), []string{"seen"}) {
		t.Fatalf("marks = %v, want one seen", tally.Marks())
	}
	if tally.Counters()["indexed"] != 3 || tally.SkipReason() != "unspecified" {
		t.Fatalf("tally = %v %q", tally.Counters(), tally.SkipReason())
	}
	tally.Reset()
	if len(tally.Counters()) != 0 || tally.SkipReason() != "" || len(tally.Marks()) != 0 {
		t.Fatal("Reset left state behind")
	}
}

func TestProgressSnapshotEncodesLikeTheRunRecord(t *testing.T) {
	total := int64(10)
	eta := time.Second
	now := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	ours := ProgressSnapshot{Phase: "p", Phases: []string{"p"}, Done: 3, Total: &total, Unit: "files", Failed: 1,
		Counters: map[string]int64{"ok": 2}, Chunks: &ChunkProgress{Done: 1, Total: 2, Size: 3},
		Workers:  []WorkerProgress{{Worker: 1, Chunk: 2, Item: "x", ItemIndex: 3, ChunkSize: 4, LeasedAt: now, Heartbeat: now, Stuck: true}},
		RatePerS: 1.5, ETA: &eta, Current: "x", UpdatedAt: now}
	a, err := json.Marshal(ours)
	if err != nil {
		t.Fatal(err)
	}
	var theirs database.ProgressSnapshot
	if err := json.Unmarshal(a, &theirs); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(theirs)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("encodings differ:\n ops %s\n db  %s", a, b)
	}
}

func TestScheduleValidation(t *testing.T) {
	bad := []*Schedule{Every(0), DailyAt("25:00"), DailyAt("3:00"), Cron("* * *"), Cron("61 * * * *"),
		Cron("5-1 * * * *"), Cron("*/0 * * * *"), InMaintenanceWindow(-1)}
	for _, s := range bad {
		if s.Err() == nil {
			t.Errorf("%s validated", s)
		}
	}
	good := []*Schedule{Every(time.Hour).OnStart(), DailyAt("03:30").Live(), Cron("*/15 2-4 1,15 * 0"),
		InMaintenanceWindow(30).When(func() bool { return true }).Params(map[string]int{"limit": 5})}
	for _, s := range good {
		if s.Err() != nil {
			t.Errorf("%s: %v", s, s.Err())
		}
	}
	if !good[1].IsLive() || string(good[3].RunParams()) != `{"limit":5}` || !good[3].Enabled() {
		t.Fatal("modifiers lost")
	}
}

func TestFingerprint(t *testing.T) {
	a := Row{ID: "r", Inputs: map[string]string{"title": "x", "author": "y"}}
	b := Row{ID: "r2", Title: "other", Inputs: map[string]string{"author": "y", "title": "x"}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("the fingerprint depends on map order or display fields")
	}
	held := a
	held.Hold = "index not built"
	if held.Fingerprint() == a.Fingerprint() {
		t.Fatal("a hold must change the fingerprint")
	}
	held.Retry = true
	if held.Fingerprint() != a.Fingerprint() {
		t.Fatal("a transient (Retry) hold must not change the fingerprint")
	}
}

func TestEffects(t *testing.T) {
	e := Writes(ResBooks, Deletes(ResBookFiles, ResSeries)).AlsoReads(ResAuthors)
	sort.Slice(e.writes, func(i, j int) bool { return e.writes[i] < e.writes[j] })
	if !reflect.DeepEqual(e.WriteSet(), []Resource{ResBookFiles, ResBooks, ResSeries}) {
		t.Fatalf("writes = %v", e.WriteSet())
	}
	if !reflect.DeepEqual(e.DeleteSet(), []Resource{ResBookFiles, ResSeries}) || e.IsReadOnly() {
		t.Fatalf("deletes = %v", e.DeleteSet())
	}
	if e.Ready() != ReadyStore || ReadOnly().NeedsReady(ReadyNone).Ready() != ReadyNone {
		t.Fatal("readiness default is ReadyStore and NeedsReady overrides it")
	}
}

// TestAlreadyApplied: a re-run row counts as applied only when the plan
// proposed a real change and the fresh state holds every proposed value.
func TestAlreadyApplied(t *testing.T) {
	m := func(kv ...string) map[string]string {
		out := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	planned := Row{Current: m("title", "old", "year", "1999"), Proposed: m("title", "new", "year", "1999")}
	cases := []struct {
		name    string
		planned Row
		fresh   Row
		want    bool
	}{
		{"applied", planned, Row{Current: m("title", "new", "year", "1999")}, true},
		{"not yet applied", planned, Row{Current: m("title", "old", "year", "1999")}, false},
		{"partly different", planned, Row{Current: m("title", "new", "year", "2000")}, false},
		{"proposed field missing", planned, Row{Current: m("year", "1999")}, false},
		{"nothing proposed", Row{Current: m("title", "x")}, Row{Current: m("title", "x")}, false},
		{"no-op plan", Row{Current: m("title", "x"), Proposed: m("title", "x")}, Row{Current: m("title", "x")}, false},
	}
	for _, c := range cases {
		if got := alreadyApplied(c.planned, c.fresh); got != c.want {
			t.Errorf("%s: alreadyApplied = %v, want %v", c.name, got, c.want)
		}
	}
}

// A mark name that cannot be stored (empty, or holding the NUL the store
// splits on) is a programming error: Mark panics, and the runner reports
// the panic as the item's error.
func TestMarkRefusesBadNames(t *testing.T) {
	for _, name := range []string{"", "a\x00b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Mark(%q) did not panic", name)
				}
			}()
			irc := NewItemRC(NewRC(RunEnv{Context: context.Background()}), context.Background(), &Tally{}, nil)
			irc.Mark(name)
		}()
	}
}

// NoteRefusal flags the live item whose context it is given (or a context
// derived from it) and keeps the first refusal; once the item is sealed (it
// returned), or with no item in the context, the refusal goes to the run's
// latch NewRC installed; with neither it reports false. Reset clears it.
func TestNoteRefusal(t *testing.T) {
	rc := NewRC(RunEnv{Context: context.Background()})
	latch := RefusalLatchFrom(rc.Context())
	if latch == nil {
		t.Fatal("NewRC installed no refusal latch")
	}
	tally := &Tally{}
	irc := NewItemRC(rc, rc.Context(), tally, nil)
	first, second := errors.New("fenced"), errors.New("lease lost")
	child, cancel := context.WithCancel(irc.Context())
	defer cancel()
	if !NoteRefusal(child, first) || !NoteRefusal(irc.Context(), second) {
		t.Fatal("a live item did not take the refusal")
	}
	if !errors.Is(tally.Refused(), first) || latch.Err() != nil {
		t.Fatalf("Refused = %v, latch = %v; want the first refusal on the item only", tally.Refused(), latch.Err())
	}
	tally.Seal()
	if !NoteRefusal(irc.Context(), second) || !errors.Is(latch.Err(), second) {
		t.Fatalf("a sealed item's late refusal: latch = %v, want it latched on the run", latch.Err())
	}
	if !NoteRefusal(rc.Context(), first) || !errors.Is(latch.Err(), second) {
		t.Fatal("the run context lost its latch, or the latch did not keep the first refusal")
	}
	if NoteRefusal(context.Background(), first) {
		t.Fatal("a context with no item and no latch took a refusal")
	}
	tally.Reset()
	if tally.Refused() != nil {
		t.Fatal("Reset kept the refusal")
	}
}

// evidenceWriter is a Writer whose only working method is Wrote: it reports
// the subjects in have as written by an earlier attempt (all: every subject
// set), or fails with err. Its writes fail, so a test sees any Apply.
type evidenceWriter struct {
	mu    sync.Mutex
	have  map[Subject]bool
	all   bool
	err   error
	calls int
}

func (w *evidenceWriter) ModifyBook(context.Context, string, func(*database.Book) error) (Changed, error) {
	return Changed{}, errors.New("evidenceWriter writes nothing")
}
func (w *evidenceWriter) ModifyBookFile(context.Context, string, string, func(*database.BookFile) error) (Changed, error) {
	return Changed{}, errors.New("evidenceWriter writes nothing")
}
func (w *evidenceWriter) RepointBookFile(context.Context, string, string, database.BookFileLocation, database.BookFileLocation) error {
	return errors.New("evidenceWriter writes nothing")
}
func (w *evidenceWriter) ModifyBookAuthors(context.Context, string, func([]database.BookAuthor) ([]database.BookAuthor, error)) (Changed, error) {
	return Changed{}, errors.New("evidenceWriter writes nothing")
}
func (w *evidenceWriter) Effect(context.Context, EffectSpec, func() ([]HistoryRow, error)) error {
	return errors.New("evidenceWriter writes nothing")
}
func (w *evidenceWriter) Wrote(_ context.Context, subjects []Subject) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.err != nil {
		return false, w.err
	}
	if len(subjects) == 0 {
		return w.all, nil
	}
	for _, s := range subjects {
		if !w.all && !w.have[s] {
			return false, nil
		}
	}
	return true, nil
}

func (w *evidenceWriter) wroteCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// S-C (final review): a row whose fresh Current equals the plan's Proposed is
// reported applied without a write only on a resumed attempt, only when its
// fingerprint moved, and only when no hold, owner-only flag or guard objects.
// Otherwise it takes the ordinary path: changed since plan, or the refusal.
// The Writer has this run's history for the row's subject throughout, so
// only the conditions under test decide (TestAlreadyAppliedNeedsEvidence
// covers the evidence).
func TestAlreadyAppliedNeedsResumeAndAdmission(t *testing.T) {
	planned := Row{ID: "r1", Subjects: Books("book-001"), Inputs: map[string]string{"title": "old"},
		Current: map[string]string{"title": "old"}, Proposed: map[string]string{"title": "new"}}
	applied := Row{ID: "r1", Inputs: map[string]string{"title": "new"}, Current: map[string]string{"title": "new"}}
	cases := []struct {
		name    string
		fresh   Row
		guards  []Guard
		resumed bool
		want    bool
		skip    string
	}{
		{"resumed, applied by the earlier attempt", applied, nil, true, true, ""},
		{"first attempt (a foreign actor made the same change)", applied, nil, false, false, SkipChangedSincePlan},
		{"resumed but now held", withHold(applied, "owner hold"), nil, true, false, SkipChangedSincePlan},
		{"resumed but now owner-only", withOwnerOnly(applied), nil, true, false, SkipChangedSincePlan},
		{"resumed but guarded", applied, []Guard{func(Row) string { return "guarded" }}, true, false, SkipChangedSincePlan},
		{"resumed, values differ", Row{ID: "r1", Inputs: map[string]string{"title": "other"},
			Current: map[string]string{"title": "other"}}, nil, true, false, SkipChangedSincePlan},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applyCalled := false
			spec := FixerSpec[NoParams, string]{
				Load:     func(*RC, NoParams, string) (string, bool, error) { return "c", true, nil },
				Evaluate: func(*ItemRC, NoParams, string) (Row, error) { return c.fresh, nil },
				Apply:    func(*ItemRC, Writer, NoParams, Row) error { applyCalled = true; return nil },
				Guards:   c.guards,
			}
			w := &evidenceWriter{have: map[Subject]bool{{Type: SubjectBook, ID: "book-001"}: true}}
			rc := NewRC(RunEnv{Context: context.Background(), Resumed: c.resumed, Writer: w})
			tally := &Tally{}
			irc := NewItemRC(rc, context.Background(), tally, nil)
			ok, err := applyRow(irc, spec, NoParams{}, planned)
			if err != nil || ok != c.want || applyCalled || tally.SkipReason() != c.skip {
				t.Fatalf("applied=%v err=%v applyCalled=%v skip=%q; want applied=%v skip=%q",
					ok, err, applyCalled, tally.SkipReason(), c.want, c.skip)
			}
		})
	}
}

// 05-PR5b: on a resumed attempt, a row that is admissible and whose every
// Proposed value now holds is reported applied only when the Writer has this
// run's history (Writer.Wrote) for EVERY subject of the planned row. The
// foreign actor -- someone who made the same change between the attempts,
// leaving no history under this run -- is skipped as changed since plan, as
// is a row with evidence for only some subjects or with no subjects at all.
// A failed read fails the item: it is neither applied nor skipped.
func TestAlreadyAppliedNeedsEvidence(t *testing.T) {
	subjects := append(Books("book-001"), BookFiles("file-001")...)
	planned := Row{ID: "r1", Subjects: subjects, Inputs: map[string]string{"title": "old"},
		Current: map[string]string{"title": "old"}, Proposed: map[string]string{"title": "new"}}
	fresh := Row{ID: "r1", Subjects: subjects, Inputs: map[string]string{"title": "new"},
		Current: map[string]string{"title": "new"}}
	readFailed := errors.New("journal unreadable")
	both := map[Subject]bool{subjects[0]: true, subjects[1]: true}
	cases := []struct {
		name      string
		planned   Row
		w         *evidenceWriter
		noWriter  bool
		resumed   bool
		want      bool
		skip      string
		wantErr   error
		wantCalls int
	}{
		{"evidence for every subject", planned, &evidenceWriter{have: both}, false, true, true, "", nil, 1},
		{"no evidence: a foreign actor between the attempts", planned, &evidenceWriter{}, false, true, false,
			SkipChangedSincePlan, nil, 1},
		{"evidence for only some subjects", planned, &evidenceWriter{have: map[Subject]bool{subjects[0]: true}},
			false, true, false, SkipChangedSincePlan, nil, 1},
		{"a row with no subjects", Row{ID: "r1", Inputs: planned.Inputs, Current: planned.Current,
			Proposed: planned.Proposed}, &evidenceWriter{all: true}, false, true, false, SkipChangedSincePlan, nil, 0},
		{"the evidence read fails", planned, &evidenceWriter{err: readFailed}, false, true, false, "", readFailed, 1},
		{"no Writer to read with", planned, nil, true, true, false, "", errAny, 0},
		{"first attempt: the store is not read", planned, &evidenceWriter{have: both}, false, false, false,
			SkipChangedSincePlan, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applyCalled := false
			spec := FixerSpec[NoParams, string]{
				Load:     func(*RC, NoParams, string) (string, bool, error) { return "c", true, nil },
				Evaluate: func(*ItemRC, NoParams, string) (Row, error) { return fresh, nil },
				Apply:    func(*ItemRC, Writer, NoParams, Row) error { applyCalled = true; return nil },
			}
			env := RunEnv{Context: context.Background(), Resumed: c.resumed}
			if !c.noWriter {
				env.Writer = c.w
			}
			tally := &Tally{}
			irc := NewItemRC(NewRC(env), context.Background(), tally, nil)
			ok, err := applyRow(irc, spec, NoParams{}, c.planned)
			switch {
			case c.wantErr == errAny && err == nil, c.wantErr != errAny && !errors.Is(err, c.wantErr):
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if ok != c.want || applyCalled || tally.SkipReason() != c.skip {
				t.Fatalf("applied=%v applyCalled=%v skip=%q; want applied=%v skip=%q",
					ok, applyCalled, tally.SkipReason(), c.want, c.skip)
			}
			if c.w != nil && c.w.wroteCalls() != c.wantCalls {
				t.Fatalf("Wrote called %d times, want %d", c.w.wroteCalls(), c.wantCalls)
			}
		})
	}
}

// errAny stands for "some error" in a table that otherwise matches with
// errors.Is.
var errAny = errors.New("any error")

func withHold(r Row, hold string) Row { r.Hold = hold; return r }

func withOwnerOnly(r Row) Row { r.OwnerOnly = true; return r }
