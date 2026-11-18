// file: core/internal/logjournal/prune_test.go
// version: 1.0.1
// guid: 740b55f6-69cc-4f61-abda-08b91342f365
// last-edited: 2026-11-18

package logjournal_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// erasedTask is the executor's view of a definition (tasks.ExecutorRunners).
type erasedTask interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

func runPrune(ctx context.Context, t *testing.T, def tasks.Definition) (logjournal.PruneResult, error) {
	t.Helper()
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
	raw, err := task.Run(taskstest.NewFakeRun(ctx, def.Kind()).RC(), p)
	var res logjournal.PruneResult
	if raw != nil {
		if jerr := json.Unmarshal(raw, &res); jerr != nil {
			t.Fatalf("result %s: %v", raw, jerr)
		}
	}
	return res, err
}

func settings(days int) func() logjournal.Settings {
	return func() logjournal.Settings {
		return logjournal.Settings{Enabled: true, MinLevel: "info", RetentionDays: days, QueueSize: 1024}
	}
}

func pruneDef(s log.JournalStore, set func() logjournal.Settings) tasks.Definition {
	return logjournal.PruneDefinition(s, set, func() time.Time { return epoch })
}

func day(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestPruneTask_Conformance(t *testing.T) {
	s, _ := openStore(t)
	taskstest.Conformance(t, pruneDef(s, settings(90)))
}

// The kind (05-logging §8) is scheduled from core.log.schedules.journal_prune (the key
// logjournal.Schedules types; TestJournal_PruneScheduleKeyDeclared pins the field), writing the journal, started by a scope the
// scheduler's system actor can hold (SEC2-15).
func TestPruneTask_Declaration(t *testing.T) {
	s, _ := openStore(t)
	d := pruneDef(s, settings(90))
	if d.Kind() != logjournal.KindPrune || logjournal.KindPrune != "core.log.journal.prune" {
		t.Errorf("kind = %q", d.Kind())
	}
	if p := tasks.Problems(d); len(p) != 0 {
		t.Fatalf("definition problems: %v", p)
	}
	c := tasks.CommonOf(d)
	if c.Schedule == nil {
		t.Fatal("no schedule")
	}
	key := c.Schedule.ConfigKey()
	if key != "core.log.schedules.journal_prune" {
		t.Errorf("schedule key = %q", key)
	}
	if logjournal.PruneScheduleKey != string(logjournal.SchedulesPath)+".journal_prune" {
		t.Errorf("PruneScheduleKey = %q is not the Schedules section's path plus its tag", logjournal.PruneScheduleKey)
	}
	// The key is the field logjournal.Schedules declares, typed ScheduleConfig.
	f, ok := reflect.TypeFor[logjournal.Schedules]().FieldByName("JournalPrune")
	if !ok || f.Tag.Get("json") != "journal_prune" || f.Type != reflect.TypeFor[tasks.ScheduleConfig]() {
		t.Errorf("Schedules.JournalPrune = %+v", f)
	}
	if !c.Schedule.IsLive() || c.Schedule.IsPreview() {
		t.Errorf("Schedule live=%v preview=%v, want live", c.Schedule.IsLive(), c.Schedule.IsPreview())
	}
	if !slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "log")}) || c.Effects.IsReadOnly() {
		t.Errorf("writes = %v, want one write on core/log", c.Effects.WriteSet())
	}
	if got := c.Perms.Start.Permissions(); !slices.Equal(got, []rbac.Permission{"core:tasks.manage"}) {
		t.Errorf("Perms.Start = %v, want core:tasks.manage (reachable by the system actor)", got)
	}
	if c.Resume != tasks.ResumeFromZero || c.Priority != tasks.PriorityLow {
		t.Errorf("Resume %v Priority %v", c.Resume, c.Priority)
	}
}

func TestPruneTask_NilStoreOrSettingsFailsClosed(t *testing.T) {
	s, _ := openStore(t)
	if len(tasks.Problems(logjournal.PruneDefinition(nil, settings(90), nil))) == 0 {
		t.Error("no problem reported for a nil store")
	}
	if len(tasks.Problems(logjournal.PruneDefinition(s, nil, nil))) == 0 {
		t.Error("no problem reported for nil settings")
	}
}

func appendAt(t *testing.T, s *logjournal.Store, ds ...time.Duration) {
	t.Helper()
	for i, d := range ds {
		if err := s.Append(t.Context(), []log.Record{rec(d, "case "+string(rune('a'+i)))}); err != nil {
			t.Fatal(err)
		}
	}
}

func remaining(t *testing.T, s *logjournal.Store) []string {
	t.Helper()
	page, err := s.Query(t.Context(), all())
	if err != nil {
		t.Fatal(err)
	}
	return messages(page.Records)
}

// Retention is days: a record older than now minus retention_days goes, one
// inside the window stays.
func TestJournal_PruneByRetentionDays(t *testing.T) {
	s, _ := openStore(t)
	appendAt(t, s, -day(120), -day(100), -day(91), -day(89), -day(1), 0)
	res, err := runPrune(t.Context(), t, pruneDef(s, settings(90)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 3 || res.Kept || res.Disabled {
		t.Errorf("result = %+v, want 3 deleted", res)
	}
	got := remaining(t, s)
	slices.Sort(got)
	if want := []string{"case d", "case e", "case f"}; !slices.Equal(got, want) {
		t.Errorf("remaining = %v, want %v", got, want)
	}
}

// retention_days 0 keeps everything (05-logging §8).
func TestJournal_PruneZeroRetentionKeepsEverything(t *testing.T) {
	s, _ := openStore(t)
	appendAt(t, s, -day(4000), -day(1))
	res, err := runPrune(t.Context(), t, pruneDef(s, settings(0)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 || !res.Kept {
		t.Errorf("result = %+v, want Kept", res)
	}
	if n := len(remaining(t, s)); n != 2 {
		t.Errorf("%d records remain, want 2", n)
	}
}

// A journal that is off is neither written nor pruned (Settings.Enabled).
func TestJournal_PruneDisabledJournalIsLeftAlone(t *testing.T) {
	s, _ := openStore(t)
	appendAt(t, s, -day(4000))
	off := func() logjournal.Settings {
		return logjournal.Settings{Enabled: false, MinLevel: "info", RetentionDays: 90, QueueSize: 1024}
	}
	res, err := runPrune(t.Context(), t, pruneDef(s, off))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 || !res.Disabled {
		t.Errorf("result = %+v, want Disabled", res)
	}
	if n := len(remaining(t, s)); n != 1 {
		t.Errorf("%d records remain, want 1", n)
	}
}

// Zero keeps everything (05-logging §8); a negative value, which the schema
// refuses, is read the same fail-closed way: nothing is deleted.
func TestJournal_PruneNegativeRetentionKeepsEverything(t *testing.T) {
	s := &scriptStore{steps: []step{{10_000, true, nil}}}
	res, err := runPrune(t.Context(), t, pruneDef(s, settings(-1)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Kept || res.Deleted != 0 || len(s.got()) != 0 {
		t.Errorf("result %+v, %d Prune calls; want Kept and no calls", res, len(s.got()))
	}
}

// A retention past the maximum overflowed time.Duration arithmetic into a
// cutoff in the future, which prunes the whole journal. It now fails the run
// before the store is touched.
func TestJournal_PruneRefusesRetentionPastTheMaximum(t *testing.T) {
	for _, days := range []int{logjournal.MaxRetentionDays + 1, 106_752, 1 << 40, math.MaxInt} {
		s := &scriptStore{steps: []step{{10_000, true, nil}}}
		_, err := runPrune(t.Context(), t, pruneDef(s, settings(days)))
		if !errors.Is(err, logjournal.ErrRetentionOutOfRange) {
			t.Errorf("retention_days %d: err = %v, want ErrRetentionOutOfRange", days, err)
		}
		if n := len(s.got()); n != 0 {
			t.Errorf("retention_days %d: Prune called %d times, want 0", days, n)
		}
	}
}

// The maximum itself works, and its cutoff is strictly before now.
func TestJournal_PruneAtTheMaximumUsesAPastCutoff(t *testing.T) {
	s := &scriptStore{steps: []step{{0, false, nil}}}
	if _, err := runPrune(t.Context(), t, pruneDef(s, settings(logjournal.MaxRetentionDays))); err != nil {
		t.Fatal(err)
	}
	calls := s.got()
	if len(calls) != 1 || !calls[0].before.Before(epoch) {
		t.Fatalf("calls = %+v, want one with a cutoff before %v", calls, epoch)
	}
	if want := epoch.AddDate(0, 0, -logjournal.MaxRetentionDays); !calls[0].before.Equal(want) {
		t.Errorf("cutoff = %v, want %v", calls[0].before, want)
	}
}

// Whatever the clock, a cutoff is never at or after now: one day back is
// before it.
func TestJournal_PruneCutoffIsBeforeNow(t *testing.T) {
	for _, now := range []time.Time{epoch, {}, time.Unix(0, 0), epoch.AddDate(1000, 0, 0)} {
		s := &scriptStore{steps: []step{{0, false, nil}}}
		def := logjournal.PruneDefinition(s, settings(1), func() time.Time { return now })
		if _, err := runPrune(t.Context(), t, def); err != nil {
			t.Fatal(err)
		}
		if c := s.got(); len(c) != 1 || !c[0].before.Before(now) {
			t.Errorf("now %v: calls %+v, want a cutoff before now", now, c)
		}
	}
}

// The schema maximum of Settings.RetentionDays is the constant the task
// enforces, so config refuses what the task would.
func TestSettings_RetentionDaysSchemaMaximumMatchesTheTask(t *testing.T) {
	f, ok := reflect.TypeFor[logjournal.Settings]().FieldByName("RetentionDays")
	if !ok {
		t.Fatal("no RetentionDays field")
	}
	want := "maximum=" + strconv.Itoa(logjournal.MaxRetentionDays)
	if tag := f.Tag.Get("schema"); !strings.Contains(tag, want) || !strings.Contains(tag, "minimum=0") {
		t.Errorf("schema tag = %q, want minimum=0 and %s", tag, want)
	}
}

// scriptStore is a JournalStore whose Prune answers from a script.
type scriptStore struct {
	mu    sync.Mutex
	steps []step
	calls []call
	hook  func(n int)
}

type step struct {
	deleted int
	more    bool
	err     error
}

type call struct {
	before time.Time
	limit  int
}

func (s *scriptStore) Append(context.Context, []log.Record) error { return nil }
func (s *scriptStore) Query(context.Context, log.JournalQuery) (log.JournalPage, error) {
	return log.JournalPage{}, nil
}

func (s *scriptStore) Prune(_ context.Context, before time.Time, limit int) (int, bool, error) {
	s.mu.Lock()
	s.calls = append(s.calls, call{before, limit})
	n := len(s.calls)
	var st step
	if n <= len(s.steps) {
		st = s.steps[n-1]
	}
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return st.deleted, st.more, st.err
}

func (s *scriptStore) got() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// The task calls Prune until more is false, with the store's own batch cap,
// and reports the total (05-logging §10 TestJournal_BatchAndRetention).
func TestJournal_PruneCallsUntilMoreIsFalse(t *testing.T) {
	s := &scriptStore{steps: []step{{10_000, true, nil}, {10_000, true, nil}, {5, false, nil}}}
	res, err := runPrune(t.Context(), t, pruneDef(s, settings(30)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 20_005 {
		t.Errorf("deleted = %d, want 20005", res.Deleted)
	}
	calls := s.got()
	if len(calls) != 3 {
		t.Fatalf("Prune called %d times, want 3", len(calls))
	}
	want := epoch.Add(-30 * day(1))
	for _, c := range calls {
		if c.limit != logjournal.MaxPruneBatch || !c.before.Equal(want) {
			t.Errorf("call = %+v, want limit %d before %v", c, logjournal.MaxPruneBatch, want)
		}
	}
}

// A call that reports more but deleted nothing cannot make progress: the
// task fails rather than call it forever.
func TestJournal_PruneStopsWhenMoreButNothingDeleted(t *testing.T) {
	s := &scriptStore{steps: []step{{0, true, nil}, {0, true, nil}, {0, true, nil}}}
	_, err := runPrune(t.Context(), t, pruneDef(s, settings(30)))
	if err == nil || !strings.Contains(err.Error(), "deleted none") {
		t.Fatalf("err = %v, want the deleted-none error", err)
	}
	if n := len(s.got()); n != 1 {
		t.Errorf("Prune called %d times, want 1", n)
	}
}

func TestJournal_PruneErrorStopsAndSurfaces(t *testing.T) {
	boom := errors.New("journal scan wedged")
	s := &scriptStore{steps: []step{{100, true, nil}, {0, false, boom}, {7, false, nil}}}
	_, err := runPrune(t.Context(), t, pruneDef(s, settings(30)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if n := len(s.got()); n != 2 {
		t.Errorf("Prune called %d times, want 2", n)
	}
}

func TestJournal_PruneCancelStopsBetweenCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &scriptStore{hook: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	for range 50 {
		s.steps = append(s.steps, step{10_000, true, nil})
	}
	if _, err := runPrune(ctx, t, pruneDef(s, settings(30))); err == nil {
		t.Fatal("a canceled run returned no error")
	}
	if n := len(s.got()); n > 3 {
		t.Errorf("Prune called %d times after a cancel at call 2", n)
	}
}
