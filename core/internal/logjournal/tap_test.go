// file: core/internal/logjournal/tap_test.go
// version: 1.0.1
// guid: 6d2a9c05-3e81-4b7f-a4d6-0c8f1e5b7a93
// last-edited: 2026-11-13

package logjournal_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/schedspec"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/tasks"
)

// fakeJournal records the batches it is given and can be made to fail. The tap
// depends on log.JournalStore only, so its timing is tested without a database
// (and so inside a synctest bubble).
type fakeJournal struct {
	mu      sync.Mutex
	batches [][]log.Record
	err     error
}

func (f *fakeJournal) Append(_ context.Context, recs []log.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, append([]log.Record(nil), recs...))
	return nil
}

func (f *fakeJournal) Query(context.Context, log.JournalQuery) (log.JournalPage, error) {
	return log.JournalPage{}, nil
}

func (f *fakeJournal) Prune(context.Context, time.Time, int) (int, bool, error) { return 0, false, nil }

func (f *fakeJournal) sizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.batches))
	for i, b := range f.batches {
		out[i] = len(b)
	}
	return out
}

func (f *fakeJournal) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func newLogging(t *testing.T) *log.Logging {
	t.Helper()
	l, err := log.New(log.Options{Output: io.Discard, Format: log.FormatText, Level: slog.LevelDebug, Redactor: log.NewRedactor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

var enabled = logjournal.Settings{Enabled: true, MinLevel: "info", RetentionDays: 90, QueueSize: 4096}

// emit logs n records through the logger, in order, numbered from first.
func emit(l *log.Logging, first, n int) {
	lg := l.Module("core")
	for i := range n {
		lg.Info(fmt.Sprintf("signal %04d", first+i))
	}
}

// The journal batches 256 records or one second, whichever comes first; it
// declares no migration step and no level (the journal starts empty and has
// nothing before it to migrate); it fails open.
func TestJournal_BatchAndRetention(t *testing.T) {
	t.Run("size_bound_writes_256_without_waiting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			tp, err := logjournal.StartTap(l, fj, enabled)
			if err != nil {
				t.Fatal(err)
			}
			emit(l, 0, 256)
			synctest.Wait()
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{256}) {
				t.Fatalf("after 256 records and no time passing: batches %v, want [256]", got)
			}
			emit(l, 256, 257)
			synctest.Wait()
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{256, 256}) {
				t.Fatalf("after 513 records: batches %v, want [256 256] and one record waiting", got)
			}
			if c := tp.Counters(); c.Pending != 1 {
				t.Fatalf("Pending = %d, want 1", c.Pending)
			}
			time.Sleep(time.Second)
			synctest.Wait()
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{256, 256, 1}) {
				t.Fatalf("one second later: batches %v, want [256 256 1]", got)
			}
			// Order: every record, in the order logged.
			i := 0
			for _, b := range fj.batches {
				for _, r := range b {
					if want := fmt.Sprintf("signal %04d", i); r.Message != want {
						t.Fatalf("record %d is %q, want %q", i, r.Message, want)
					}
					i++
				}
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("age_bound_is_one_second_from_the_first_record", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			tp, _ := logjournal.StartTap(l, fj, enabled)
			emit(l, 0, 1)
			synctest.Wait()
			time.Sleep(600 * time.Millisecond)
			emit(l, 1, 1) // a later record does not push the first one's deadline out
			synctest.Wait()
			if c := tp.Counters(); c.Lag != 600*time.Millisecond || c.Pending != 2 {
				t.Fatalf("Counters = %+v, want Lag 600ms and 2 pending", c)
			}
			time.Sleep(399 * time.Millisecond)
			synctest.Wait()
			if got := fj.sizes(); len(got) != 0 {
				t.Fatalf("batches %v at 999 ms, want none", got)
			}
			time.Sleep(time.Millisecond)
			synctest.Wait()
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{2}) {
				t.Fatalf("batches %v at 1 s, want [2]", got)
			}
			if c := tp.Counters(); c.Lag != 0 || c.Pending != 0 {
				t.Fatalf("Counters after the write = %+v, want Lag 0 and nothing pending", c)
			}
			// The timer re-arms for the next first record.
			emit(l, 2, 1)
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{2, 1}) {
				t.Fatalf("second round: batches %v, want [2 1]", got)
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("min_level_keeps_the_journal_quiet", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			s := enabled
			s.MinLevel = "warn"
			if _, err := logjournal.StartTap(l, fj, s); err != nil {
				t.Fatal(err)
			}
			lg := l.Module("core")
			lg.Debug("chatter")
			lg.Info("routine")
			lg.Warn("the signal is lit")
			lg.Error("the vault is open")
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			if len(fj.batches) != 1 || len(fj.batches[0]) != 2 || fj.batches[0][0].Message != "the signal is lit" {
				t.Fatalf("batches = %+v, want one batch of the warn and the error", fj.batches)
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("fails_open_and_recovers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			tp, _ := logjournal.StartTap(l, fj, enabled)
			fj.setErr(errors.New("the disk is full"))
			emit(l, 0, 3)
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			c := tp.Counters()
			if c.WriteErrors != 1 || !c.Degraded || c.Pending != 0 || c.Lag != 0 {
				t.Fatalf("after a failed write: %+v, want 1 error, Degraded, nothing pending", c)
			}
			// Logging itself is untouched: the record still reached the logger's own count.
			if n := l.Counters().Records["core"][slog.LevelInfo]; n != 3 {
				t.Fatalf("logger counted %d info records, want 3", n)
			}
			fj.setErr(nil)
			emit(l, 3, 1)
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			c = tp.Counters()
			if c.WriteErrors != 1 || c.Degraded {
				t.Fatalf("after a good write: %+v, want 1 error counted and not Degraded", c)
			}
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{1}) {
				t.Fatalf("batches %v, want only the record after recovery", got)
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("close_then_flush_loses_nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			tp, _ := logjournal.StartTap(l, fj, enabled)
			emit(l, 0, 5)
			if err := l.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := tp.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := fj.sizes(); !reflect.DeepEqual(got, []int{5}) {
				t.Fatalf("batches %v, want [5]", got)
			}
		})
	})

	t.Run("stop_refuses_further_records", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			tp, _ := logjournal.StartTap(l, fj, enabled)
			if err := tp.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			emit(l, 0, 3)
			synctest.Wait()
			time.Sleep(2 * time.Second)
			synctest.Wait()
			if len(fj.batches) != 0 {
				t.Fatalf("a stopped tap wrote %v", fj.sizes())
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("disabled_registers_nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, fj := newLogging(t), &fakeJournal{}
			s := enabled
			s.Enabled = false
			tp, err := logjournal.StartTap(l, fj, s)
			if err != nil || tp == nil {
				t.Fatalf("StartTap disabled = %v, %v; want an inert tap", tp, err)
			}
			emit(l, 0, 300)
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			if len(fj.batches) != 0 || tp.Counters() != (logjournal.TapCounters{}) {
				t.Fatalf("disabled journal wrote %v, counters %+v", fj.sizes(), tp.Counters())
			}
			if _, ok := l.Counters().Dropped[logjournal.TapName]; ok {
				t.Fatal("a disabled journal registered a tap")
			}
			_ = l.Close(context.Background())
		})
	})

	t.Run("bad_min_level_is_an_error", func(t *testing.T) {
		l := newLogging(t)
		s := enabled
		s.MinLevel = "loud"
		if _, err := logjournal.StartTap(l, &fakeJournal{}, s); err == nil {
			t.Fatal("StartTap accepted min_level loud")
		}
		_ = l.Close(context.Background())
	})

	// The journal has no migration step and no level stamp: it starts empty and
	// nothing older than core exists for it to migrate (the cutover of an app's
	// own rows is that app's step, over log.Importer).
	t.Run("storage_spec_has_no_migration_step_or_level", func(t *testing.T) {
		sp := logjournal.StorageSpec()
		if !sp.Keyspace || len(sp.Migrations) != 0 || sp.Level != 0 || len(sp.Claims) != 0 || len(sp.Serialized) != 0 ||
			len(sp.Legacy) != 0 || sp.UpgradeFloor != nil || len(sp.Derived) != 0 {
			t.Fatalf("StorageSpec = %+v, want a plain keyspace", sp)
		}
		if logjournal.Owner != "core/log" {
			t.Fatalf("Owner = %q, want core/log", logjournal.Owner)
		}
	})
}

// core.log.schedules declares the journal's prune key, journal_prune, as a
// tasks.ScheduleConfig, in the owner's section where the prune task reads it.
func TestJournal_PruneScheduleKeyDeclared(t *testing.T) {
	f, ok := reflect.TypeFor[logjournal.Schedules]().FieldByName("JournalPrune")
	if !ok {
		t.Fatal("Schedules has no JournalPrune field")
	}
	if got := f.Tag.Get("json"); got != "journal_prune" {
		t.Errorf("JournalPrune json tag = %q, want journal_prune", got)
	}
	if f.Type != reflect.TypeFor[tasks.ScheduleConfig]() {
		t.Errorf("JournalPrune type = %v, want tasks.ScheduleConfig", f.Type)
	}
	if n := reflect.TypeFor[logjournal.Schedules]().NumField(); n != 1 {
		t.Errorf("Schedules has %d fields, want only journal_prune", n)
	}
	// The key the prune task will name is the section's path plus the tag, and
	// it has the R2-7 form of a core key.
	const key = "core.log.schedules.journal_prune"
	if owner, err := schedspec.KeyOwner(key); err != nil || owner != "core" {
		t.Errorf("schedule key %q: owner %q, err %v; want core", key, owner, err)
	}
	if got := tasks.ScheduleFromConfig(key).ConfigKey(); got != key {
		t.Errorf("ScheduleFromConfig(%q).ConfigKey() = %q", key, got)
	}

	// The settings keep the tags the kernel's declaration had: enabled and
	// queue_size are restart keys, min_level is an enum, retention 0 keeps.
	st := reflect.TypeFor[logjournal.Settings]()
	for name, want := range map[string][2]string{
		"Enabled":       {"enabled", "restart"},
		"MinLevel":      {"min_level", ""},
		"RetentionDays": {"retention_days", ""},
		"QueueSize":     {"queue_size", "restart"},
	} {
		f, ok := st.FieldByName(name)
		if !ok || f.Tag.Get("json") != want[0] || f.Tag.Get("reload") != want[1] {
			t.Errorf("Settings.%s tags = %q, want json %q reload %q", name, f.Tag, want[0], want[1])
		}
	}
}

// holdingJournal is a journal whose Append waits to be released, so a test can
// look at the tap while a batch is in the store's hands.
type holdingJournal struct {
	fakeJournal
	release chan struct{}
}

func (h *holdingJournal) Append(ctx context.Context, recs []log.Record) error {
	<-h.release
	return h.fakeJournal.Append(ctx, recs)
}

// A batch the store is still writing has left Pending, but its records are not
// in the journal: Lag keeps counting from its oldest record until the write
// returns, so a stuck store shows as a growing gauge and not as a quiet one.
func TestTap_LagCountsTheBatchInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, hj := newLogging(t), &holdingJournal{release: make(chan struct{})}
		tp, err := logjournal.StartTap(l, hj, enabled)
		if err != nil {
			t.Fatal(err)
		}
		emit(l, 0, 256) // the size bound starts the write, which waits in Append
		synctest.Wait()
		time.Sleep(500 * time.Millisecond)

		c := tp.Counters()
		if c.Pending != 0 {
			t.Errorf("Pending = %d while the batch is in the store's hands, want 0", c.Pending)
		}
		if c.Lag != 500*time.Millisecond {
			t.Errorf("Lag = %v with a batch in flight for 500ms, want 500ms", c.Lag)
		}

		close(hj.release)
		synctest.Wait()
		if c := tp.Counters(); c.Lag != 0 || c.Pending != 0 || c.Degraded {
			t.Errorf("after the write returned: %+v, want no lag, nothing pending, not degraded", c)
		}
		if got := hj.sizes(); !reflect.DeepEqual(got, []int{256}) {
			t.Errorf("batches %v, want [256]", got)
		}
		_ = l.Close(context.Background())
	})
}
