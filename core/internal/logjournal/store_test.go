// file: core/internal/logjournal/store_test.go
// version: 1.0.0
// guid: 1f6d2b84-9c37-4a05-8e1b-d5a7c3f0e962
// last-edited: 2026-11-13

package logjournal_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/log/logtest"
	"github.com/falkcorp/mediacore/storage"
)

// The journal is opened through storage.Open/Prepare/Migrate over MemFS, never
// through coretest: internal/logjournal is in the kernel's import graph, and
// coretest imports the kernel (the outboxstore tests do the same).
func openKS(t testing.TB) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   "/batcave/journal.pebble",
		Owners: []storage.OwnerSpec{{Owner: logjournal.Owner, Spec: logjournal.StorageSpec(), Enabled: true}},
		Build:  "logjournal+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(logjournal.Owner)
	if !ok {
		t.Fatal("no journal keyspace")
	}
	return ks
}

func openStore(t testing.TB, opts ...logjournal.Options) (*logjournal.Store, storage.Keyspace) {
	t.Helper()
	ks := openKS(t)
	return logjournal.New(ks, opts...), ks
}

var epoch = time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return epoch.Add(d) }

func rec(d time.Duration, msg string) log.Record {
	return log.Record{Time: at(d), Level: log.LevelInfo, Module: "core", Message: msg}
}

func all() log.JournalQuery {
	return log.JournalQuery{MinLevel: log.LevelTrace, Limit: logjournal.MaxLimit}
}

func messages(recs []log.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Message
	}
	return out
}

func wantMessages(t *testing.T, what string, got []log.Record, want ...string) {
	t.Helper()
	g := messages(got)
	if len(g) == 0 && len(want) == 0 {
		return
	}
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("%s: got %q, want %q", what, g, want)
	}
}

// The native store passes the contract every journal passes (the logtest
// suite), and keeps what the contract leaves to the implementation: a
// monotonic key, a whole-or-nothing batch, a zero time stamped by the clock, a
// skipped corrupt row, a bounded scan.
func TestNativeJournalStore_RoundTrip(t *testing.T) {
	logtest.JournalStoreSuite(t, func(t testing.TB) logtest.Subject {
		s, _ := openStore(t)
		return logtest.Subject{Store: s, Importer: logjournal.NewImporter(s)}
	})

	ctx := context.Background()

	t.Run("equal_time_follows_append_order_across_calls", func(t *testing.T) {
		s, _ := openStore(t)
		for i := range 30 {
			if err := s.Append(ctx, []log.Record{rec(0, string(rune('a'+i%26))+string(rune('A'+i/26)))}); err != nil {
				t.Fatal(err)
			}
		}
		p, err := s.Query(ctx, all())
		if err != nil || len(p.Records) != 30 {
			t.Fatalf("query: %d records, %v", len(p.Records), err)
		}
		for i, r := range p.Records {
			j := 29 - i
			if want := string(rune('a'+j%26)) + string(rune('A'+j/26)); r.Message != want {
				t.Fatalf("position %d: %q, want %q: not reverse append order", i, r.Message, want)
			}
		}
	})

	t.Run("out_of_order_times_keep_reverse_append_per_millisecond", func(t *testing.T) {
		s, _ := openStore(t)
		in := []log.Record{rec(time.Second, "b1"), rec(0, "a1"), rec(time.Second, "b2"), rec(0, "a2"), rec(time.Second, "b3")}
		if err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Query(ctx, all())
		wantMessages(t, "order", p.Records, "b3", "b2", "b1", "a2", "a1")
	})

	t.Run("invalid_time_rejects_the_whole_batch", func(t *testing.T) {
		s, _ := openStore(t)
		err := s.Append(ctx, []log.Record{rec(0, "fine"), {Time: time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), Message: "pre-epoch"}})
		if !errors.Is(err, logjournal.ErrInvalidTime) {
			t.Fatalf("Append err = %v, want ErrInvalidTime", err)
		}
		p, _ := s.Query(ctx, all())
		if len(p.Records) != 0 {
			t.Fatalf("a rejected batch left %d record(s)", len(p.Records))
		}
		if err := s.Append(ctx, []log.Record{{Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Message: "far future"}}); !errors.Is(err, logjournal.ErrInvalidTime) {
			t.Fatalf("year 10000: err = %v, want ErrInvalidTime", err)
		}
	})

	t.Run("zero_time_is_stamped_by_the_clock", func(t *testing.T) {
		s, _ := openStore(t, logjournal.Options{Now: func() time.Time { return at(5 * time.Second) }})
		if err := s.Append(ctx, []log.Record{{Level: log.LevelInfo, Module: "core", Message: "unstamped"}}); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Query(ctx, all())
		if len(p.Records) != 1 || !p.Records[0].Time.Equal(at(5*time.Second)) {
			t.Fatalf("records = %+v, want one at the clock's time", p.Records)
		}
	})

	t.Run("attribute_kinds", func(t *testing.T) {
		s, _ := openStore(t)
		r := rec(0, "kinds")
		r.TraceID, r.SpanID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
		r.Attrs = []slog.Attr{
			slog.Int64("neg", -7), slog.Uint64("big", math.MaxUint64), slog.Float64("pi", 3.25), slog.Bool("ok", true),
			slog.Duration("took", 1500*time.Millisecond), slog.Float64("nan", math.NaN()),
			slog.Group("bat", slog.String("cave", "deep"), slog.Group("belt", slog.Int("gadgets", 9))),
			slog.Group("", slog.String("inlined", "yes")), {}, slog.String("dup", "one"), slog.String("dup", "two"),
		}
		if err := s.Append(ctx, []log.Record{r}); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Query(ctx, all())
		if len(p.Records) != 1 {
			t.Fatalf("got %d records", len(p.Records))
		}
		got := p.Records[0]
		if got.TraceID != r.TraceID || got.SpanID != r.SpanID {
			t.Errorf("trace ids: %q %q", got.TraceID, got.SpanID)
		}
		var lines []string
		for _, a := range got.Attrs {
			lines = append(lines, a.Key+"="+a.Value.Kind().String()+":"+a.Value.String())
		}
		want := []string{
			"neg=Int64:-7", "big=Uint64:18446744073709551615", "pi=Float64:3.25", "ok=Bool:true",
			"took=String:1.5s", "nan=String:NaN", "bat.cave=String:deep", "bat.belt.gadgets=Int64:9",
			"inlined=String:yes", "dup=String:one", "dup=String:two",
		}
		if strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Errorf("attrs:\n got %q\nwant %q", lines, want)
		}
	})

	t.Run("a_corrupt_row_is_skipped_and_counted", func(t *testing.T) {
		s, ks := openStore(t)
		if err := s.Append(ctx, []log.Record{rec(0, "garbled"), rec(time.Second, "newer build"), rec(2*time.Second, "intact")}); err != nil {
			t.Fatal(err)
		}
		// The oldest row's value becomes garbage; the next one's becomes a row
		// of a version this build does not know.
		var rows []storage.KV
		for kv, err := range ks.Scan(ctx, storage.ScanOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, kv.Clone())
		}
		if len(rows) != 3 {
			t.Fatalf("%d rows, want 3", len(rows))
		}
		if err := ks.Set(ctx, rows[0].Key, []byte("not a row")); err != nil {
			t.Fatal(err)
		}
		future := append([]byte{rows[1].Value[0]}, `{"v":99,"msg":"newer build"}`...)
		if err := ks.Set(ctx, rows[1].Key, future); err != nil {
			t.Fatal(err)
		}
		p, err := s.Query(ctx, all())
		if err != nil {
			t.Fatal(err)
		}
		wantMessages(t, "records around two unreadable rows", p.Records, "intact")
		if got := s.Corrupt(); got != 2 {
			t.Errorf("Corrupt() = %d, want 2", got)
		}
	})

	t.Run("cursor_must_be_ours", func(t *testing.T) {
		s, _ := openStore(t)
		for _, c := range []string{"x", "j1.", "j1.not-a-ulid", "j2.01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
			if _, err := s.Query(ctx, log.JournalQuery{Cursor: c}); !errors.Is(err, logjournal.ErrBadCursor) {
				t.Errorf("cursor %q: err = %v, want ErrBadCursor", c, err)
			}
		}
	})

	t.Run("prune_bounds_one_batch", func(t *testing.T) {
		s, ks := openStore(t)
		const extra = 50
		n := logjournal.MaxPruneBatch + extra
		batch := make([]log.Record, 0, 1000)
		for i := range n {
			batch = append(batch, rec(time.Duration(i)*time.Millisecond, "noise"))
			if len(batch) == cap(batch) || i == n-1 {
				if err := s.Append(ctx, batch); err != nil {
					t.Fatal(err)
				}
				batch = batch[:0]
			}
		}
		// A limit above the cap is clamped to it: one call deletes at most
		// MaxPruneBatch, and says older rows remain.
		deleted, more, err := s.Prune(ctx, at(time.Hour), 10*logjournal.MaxPruneBatch)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != logjournal.MaxPruneBatch || !more {
			t.Errorf("Prune(limit above the cap) = %d, %v, want %d, true", deleted, more, logjournal.MaxPruneBatch)
		}
		if left := rowCount(t, ks); left != extra {
			t.Errorf("%d rows left, want %d", left, extra)
		}
		deleted, more, err = s.Prune(ctx, at(time.Hour), 10*logjournal.MaxPruneBatch)
		if err != nil || deleted != extra || more {
			t.Errorf("second Prune = %d, %v, %v, want %d, false, nil", deleted, more, err, extra)
		}
	})

	t.Run("a_filter_that_matches_nothing_cannot_hold_the_store", func(t *testing.T) {
		s, _ := openStore(t)
		const n = 20_000 + 700
		batch := make([]log.Record, 0, 1000)
		for i := range n {
			batch = append(batch, rec(time.Duration(i)*time.Millisecond, "noise"))
			if len(batch) == cap(batch) || i == n-1 {
				if err := s.Append(ctx, batch); err != nil {
					t.Fatal(err)
				}
				batch = batch[:0]
			}
		}
		if err := s.Append(ctx, []log.Record{rec(-time.Hour, "needle")}); err != nil {
			t.Fatal(err)
		}
		q := log.JournalQuery{MinLevel: log.LevelTrace, Text: "needle", Limit: 10}
		p, err := s.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Records) != 0 || p.Next == "" {
			t.Fatalf("first call: %d records, next %q; want a short page with a cursor (the scan budget ended it)", len(p.Records), p.Next)
		}
		calls := 1
		var found []log.Record
		for p.Next != "" {
			q.Cursor = p.Next
			if p, err = s.Query(ctx, q); err != nil {
				t.Fatal(err)
			}
			found = append(found, p.Records...)
			if calls++; calls > 10 {
				t.Fatal("the cursor does not converge")
			}
		}
		wantMessages(t, "needle", found, "needle")
	})
}
