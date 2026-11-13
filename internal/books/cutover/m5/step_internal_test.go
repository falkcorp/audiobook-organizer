// file: internal/books/cutover/m5/step_internal_test.go
// version: 1.0.1
// guid: 7a2c5e19-0b84-4d36-a1f3-98e4d6b02c57
// last-edited: 2026-11-13

package m5

import (
	"context"
	"iter"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// TestParseKey_Shapes pins the key cutter: HEAD's key is
// syslog:<RFC3339Nano in the server's zone>:<source>, and both the time and the
// source can hold colons.
func TestParseKey_Shapes(t *testing.T) {
	utc := time.Date(2026, 11, 10, 15, 37, 13, 179512786, time.UTC)
	est := time.FixedZone("", -5*3600)
	ist := time.FixedZone("", 5*3600+1800)
	cases := []struct {
		name, key, source string
		at                time.Time
		ok                bool
	}{
		{"utc", "syslog:2026-11-10T15:37:13.179512786Z:scanner", "scanner", utc, true},
		{"utc_no_fraction", "syslog:2026-11-10T15:37:13Z:scanner", "scanner", utc.Truncate(time.Second), true},
		{"negative_offset", "syslog:2026-11-10T10:37:13.179512786-05:00:scanner", "scanner", utc.In(est), true},
		{"positive_offset_half_hour", "syslog:2026-11-10T21:07:13.179512786+05:30:scanner", "scanner", utc.In(ist), true},
		{"source_with_colons", "syslog:2026-11-10T15:37:13.179512786Z:dedup:v2:retry", "dedup:v2:retry", utc, true},
		{"empty_source", "syslog:2026-11-10T15:37:13.179512786Z:", "", utc, true},
		{"no_prefix", "log:2026-11-10T15:37:13Z:scanner", "", time.Time{}, false},
		{"no_time", "syslog:scanner", "", time.Time{}, false},
		{"no_source_separator", "syslog:2026-11-10T15:37:13Z", "", time.Time{}, false},
		{"garbage_time", "syslog:2026-13-45T99:99:99Z:scanner", "", time.Time{}, false},
		{"truncated_offset", "syslog:2026-11-10T10:37:13-05:scanner", "", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at, source, ok := parseKey(c.key)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if !at.Equal(c.at) || source != c.source {
				t.Errorf("parseKey = %s, %q, want %s, %q", at, source, c.at, c.source)
			}
		})
	}
}

func TestLevel_OnlyWhatHeadWrote(t *testing.T) {
	for s, want := range map[string]slog.Level{"info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		if got, ok := level(s); !ok || got != want {
			t.Errorf("level(%q) = %v, %v, want %v", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "debug", "trace", "INFO", "warning", "fatal", " info"} {
		if _, ok := level(s); ok {
			t.Errorf("level(%q) accepted: HEAD never wrote it", s)
		}
	}
}

func TestUsable_Bounds(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"zero", time.Time{}, false},
		{"epoch", time.UnixMilli(0), true},
		{"one_ms_before_the_epoch", time.UnixMilli(-1), false},
		{"pre_1970", time.Date(1969, 7, 20, 0, 0, 0, 0, time.UTC), false},
		{"now", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), true},
		{"last_instant_of_9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"year_10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		if got := usable(c.t); got != c.want {
			t.Errorf("usable(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// emptyReader is a legacy range with no row in it.
type emptyReader struct{}

func (emptyReader) Get(context.Context, []byte) ([]byte, error) { return nil, storage.ErrNotFound }
func (emptyReader) Scan(context.Context, storage.ScanOptions) iter.Seq2[storage.KV, error] {
	return func(func(storage.KV, error) bool) {}
}
func (emptyReader) NewIterator(context.Context, storage.ScanOptions) (*storage.Iterator, error) {
	return nil, nil
}

// TestVerify_AccountingMustBalance: Verify fails a boot whose own count of rows
// read is not the rows it imported plus the rows it quarantined, even over a
// store that has no syslog: row left. A Step that returned an error after
// counting a row, or a code path that read a row and neither moved nor
// quarantined it, would show here.
func TestVerify_AccountingMustBalance(t *testing.T) {
	cases := []struct {
		name                        string
		read, imported, quarantined int64
		wantFailure                 bool
	}{
		{"nothing_read", 0, 0, 0, false},
		{"all_imported", 5, 5, 0, false},
		{"mixed", 5, 3, 2, false},
		{"a_row_neither_imported_nor_quarantined", 5, 3, 1, true},
		{"more_moved_than_read", 5, 5, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Migration{}
			m.read.Store(c.read)
			m.imported.Store(c.imported)
			m.quarantined.Store(c.quarantined)
			rep, err := m.Verify(context.Background(), emptyReader{})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(rep.Failures) > 0; got != c.wantFailure {
				t.Errorf("failures = %v, want a failure: %v", rep.Failures, c.wantFailure)
			}
			if c.wantFailure && !strings.Contains(strings.Join(rep.Failures, "\n"), "imported") {
				t.Errorf("the failure does not name the counts: %v", rep.Failures)
			}
			if rep.Read != c.read || rep.Written != c.imported || rep.Quarantined != c.quarantined {
				t.Errorf("report = %+v, want the migration's own counts", rep)
			}
		})
	}
}

// TestDecode_TimeAndSubsystem: the time check is made in UTC, the zone the
// record is stored in, so M5 refuses exactly what the journal's Import refuses
// (an instant after 9999-12-31T23:59:59Z fails Import for the whole chunk, on
// every boot); and a row with no source takes the source its key was added under.
func TestDecode_TimeAndSubsystem(t *testing.T) {
	const goodKey = "syslog:2026-11-05T00:00:04Z:scanner"
	keyTime := time.Date(2026, 11, 5, 0, 0, 4, 0, time.UTC)
	cases := []struct {
		name       string
		key, raw   string
		wantReason string
		wantTime   time.Time
		wantSub    string
	}{
		{
			name: "local_year_9999_is_utc_year_10000_falls_back_to_the_key",
			key:  goodKey, raw: `{"source":"scanner","level":"info","message":"x","created_at":"9999-12-31T23:59:59-05:00"}`,
			wantTime: keyTime, wantSub: "scanner",
		},
		{
			name: "local_year_9999_is_utc_year_10000_and_no_usable_key",
			key:  "syslog:garbage", raw: `{"source":"scanner","level":"info","message":"x","created_at":"9999-12-31T23:59:59-05:00"}`,
			wantReason: reasonTime,
		},
		{
			name: "last_utc_instant_of_9999_given_in_a_west_zone_is_kept",
			key:  goodKey, raw: `{"source":"scanner","level":"info","message":"x","created_at":"9999-12-31T18:59:59-05:00"}`,
			wantTime: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), wantSub: "scanner",
		},
		{
			name: "created_at_in_an_east_zone_is_stored_as_utc",
			key:  goodKey, raw: `{"source":"scanner","level":"info","message":"x","created_at":"2026-11-05T09:00:04+09:00"}`,
			wantTime: keyTime, wantSub: "scanner",
		},
		{
			name: "no_source_takes_the_keys",
			key:  goodKey, raw: `{"level":"warn","message":"x","created_at":"2026-11-05T00:00:04Z"}`,
			wantTime: keyTime, wantSub: "scanner",
		},
		{
			name: "a_source_on_the_row_wins_over_the_keys",
			key:  goodKey, raw: `{"source":"organizer","level":"warn","message":"x","created_at":"2026-11-05T00:00:04Z"}`,
			wantTime: keyTime, wantSub: "organizer",
		},
		{
			name: "no_source_and_no_parseable_key_stays_blank",
			key:  "syslog:garbage", raw: `{"level":"warn","message":"x","created_at":"2026-11-05T00:00:04Z"}`,
			wantTime: keyTime, wantSub: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, reason := decode(c.key, []byte(c.raw))
			if reason != c.wantReason {
				t.Fatalf("reason = %q, want %q", reason, c.wantReason)
			}
			if reason != "" {
				return
			}
			if !rec.Time.Equal(c.wantTime) || rec.Time.Location() != time.UTC {
				t.Errorf("Time = %v, want %v in UTC", rec.Time, c.wantTime)
			}
			if !usable(rec.Time) {
				t.Errorf("the record's time %v is one the journal's Import refuses", rec.Time)
			}
			if rec.Subsystem != c.wantSub {
				t.Errorf("Subsystem = %q, want %q", rec.Subsystem, c.wantSub)
			}
		})
	}
}
