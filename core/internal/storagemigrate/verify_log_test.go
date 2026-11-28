// file: core/internal/storagemigrate/verify_log_test.go
// version: 0.1.0
// guid: 7e9bfb98-dd8b-4d53-a8c3-6a69585aeb65
// last-edited: 2026-11-28

package storagemigrate_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// verifiedMsg is the line the prod-copy rehearsal parses M3's, M4's and M5's
// counts from. Its text and its four keys are a contract with rehearse.py.
const verifiedMsg = "storagemigrate: data migration verified"

// recordSink keeps every record a logger writes, for the tests that read them.
type recordSink struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (s *recordSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *recordSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r.Clone())
	return nil
}
func (s *recordSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *recordSink) WithGroup(string) slog.Handler      { return s }

// verified returns the attrs of every record whose message is verifiedMsg.
func (s *recordSink) verified() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, r := range s.recs {
		if r.Message != verifiedMsg {
			continue
		}
		m := map[string]any{}
		r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
		out = append(out, m)
	}
	return out
}

// wantVerified fails unless sink holds exactly one verified record with exactly
// these four attrs.
func wantVerified(t *testing.T, sink *recordSink, id string, read, written, quarantined int64) {
	t.Helper()
	got := sink.verified()
	if len(got) != 1 {
		t.Fatalf("%d %q records, want 1: %v", len(got), verifiedMsg, got)
	}
	want := map[string]any{"migration": id, "read": read, "written": written, "quarantined": quarantined}
	if len(got[0]) != len(want) {
		t.Fatalf("attrs = %v, want exactly %v", got[0], want)
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Fatalf("attr %s = %v (%T), want %v: attrs %v", k, got[0][k], got[0][k], v, got[0])
		}
	}
}

// TestDataMigrationVerifiedLine_Done: a Verify that passes logs its counts once,
// as the four attrs the rehearsal parses.
func TestDataMigrationVerifiedLine_Done(t *testing.T) {
	e := newEnv(t, 20)
	m := recount(sequential())
	m.Verify = func(context.Context, storage.Reader) (datamigrate.VerifyReport, error) {
		return datamigrate.VerifyReport{Read: 20, Written: 17, Held: 1, Quarantined: 3}, nil
	}
	sink := &recordSink{}
	e.mustBoot(e.runner(storagemigrate.Options{Logger: slog.New(sink)}, m))
	wantVerified(t, sink, m.ID, 20, 17, 3)
}

// TestDataMigrationVerifiedLine_Failed: a Verify that reports failures still
// logs its counts, once, before the step fails the boot.
func TestDataMigrationVerifiedLine_Failed(t *testing.T) {
	e := newEnv(t, 20)
	m := recount(sequential())
	m.Verify = func(context.Context, storage.Reader) (datamigrate.VerifyReport, error) {
		return datamigrate.VerifyReport{Read: 20, Written: 19, Quarantined: 1, Failures: []string{"the Penguin's ledger is short one umbrella"}}, nil
	}
	sink := &recordSink{}
	db, err := e.boot(context.Background(), e.runner(storagemigrate.Options{Logger: slog.New(sink)}, m))
	if db != nil {
		defer db.Close()
	}
	if !errors.Is(err, storagemigrate.ErrVerifyFailed) {
		t.Fatalf("boot = %v, want ErrVerifyFailed", err)
	}
	wantVerified(t, sink, m.ID, 20, 19, 1)
}

// TestDataMigrationVerifiedLine_NoneWhenStopped: a Verify cut short by a
// stopping boot records nothing and logs no counts.
func TestDataMigrationVerifiedLine_NoneWhenStopped(t *testing.T) {
	e := newEnv(t, 20)
	stop := errors.New("Alfred pulled the Batcave's breaker")
	ctx, cancel := context.WithCancelCause(context.Background())
	m := recount(sequential())
	m.Verify = func(c context.Context, _ storage.Reader) (datamigrate.VerifyReport, error) {
		cancel(stop)
		return datamigrate.VerifyReport{Read: 20}, c.Err()
	}
	sink := &recordSink{}
	db, err := e.boot(ctx, e.runner(storagemigrate.Options{Logger: slog.New(sink)}, m))
	if db != nil {
		defer db.Close()
	}
	if !errors.Is(err, stop) {
		t.Fatalf("boot = %v, want the stop's cause", err)
	}
	if got := sink.verified(); len(got) != 0 {
		t.Fatalf("a stopped Verify logged %v", got)
	}
}

// TestDataMigrationVerifiedLine_NoneWithoutVerify: a migration with no Verify has
// no counts, so it logs no verified line (a zero there would read as a count).
func TestDataMigrationVerifiedLine_NoneWithoutVerify(t *testing.T) {
	e := newEnv(t, 20)
	sink := &recordSink{}
	e.mustBoot(e.runner(storagemigrate.Options{Logger: slog.New(sink)}, recount(sequential())))
	if got := sink.verified(); len(got) != 0 {
		t.Fatalf("a migration without Verify logged %v", got)
	}
}
