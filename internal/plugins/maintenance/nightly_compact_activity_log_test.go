// file: internal/plugins/maintenance/nightly_compact_activity_log_test.go
// version: 1.0.0
// guid: 3a9f6c21-7d4e-4b58-a1c3-5e8d2f0b7a64
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

type nightlyDeps struct {
	compactDeps
	keep int
}

func (d *nightlyDeps) ActivityLogFullDetailDays() int { return d.keep }

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata %s unavailable: %v", name, err)
	}
	return loc
}

// TestNightlyCutoff_IsLocalMidnight uses a non-UTC zone and an evening "now",
// so a UTC-midnight cutoff (Truncate(24h)) or a now-relative cutoff both fail.
func TestNightlyCutoff_IsLocalMidnight(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	now := time.Date(2026, 9, 13, 21, 30, 0, 0, ny) // 01:30 UTC on 09-14
	got, err := nightlyCompactionCutoff(now, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 13, 0, 0, 0, 0, ny)
	if !got.Equal(want) {
		t.Fatalf("cutoff = %s, want local midnight %s", got, want)
	}

	// An entry from today (local) is never before the cutoff; one from
	// yesterday evening local — which is already TODAY in UTC — is.
	today := time.Date(2026, 9, 13, 0, 0, 1, 0, ny)
	yesterdayEvening := time.Date(2026, 9, 12, 23, 30, 0, 0, ny) // 03:30 UTC 09-13
	if today.Before(got) {
		t.Error("an entry from today would be compacted")
	}
	if !yesterdayEvening.Before(got) {
		t.Error("an entry from yesterday (local) would be kept")
	}
}

func TestNightlyCutoff_DSTDays(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	for _, tc := range []struct {
		name string
		now  time.Time
		keep int
		want time.Time
	}{
		// Spring forward 2026-03-08 (23h day): late evening, EDT in force.
		{"spring-forward evening", time.Date(2026, 3, 8, 23, 30, 0, 0, ny), 0, time.Date(2026, 3, 8, 0, 0, 0, 0, ny)},
		// Fall back 2026-11-01 (25h day).
		{"fall-back evening", time.Date(2026, 11, 1, 23, 30, 0, 0, ny), 0, time.Date(2026, 11, 1, 0, 0, 0, 0, ny)},
		// keep-days across the spring-forward boundary: N calendar days, not N*24h.
		{"keep 1 day after spring-forward", time.Date(2026, 3, 9, 0, 10, 0, 0, ny), 1, time.Date(2026, 3, 8, 0, 0, 0, 0, ny)},
		{"keep 2 days across fall-back", time.Date(2026, 11, 2, 12, 0, 0, 0, ny), 2, time.Date(2026, 10, 31, 0, 0, 0, 0, ny)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nightlyCompactionCutoff(tc.now, tc.keep)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("cutoff = %s, want %s", got, tc.want)
			}
			if h, m, _ := got.Clock(); h != 0 || m != 0 {
				t.Fatalf("cutoff %s is not local midnight", got)
			}
		})
	}
}

func TestNightlyCutoff_KeepDaysBounds(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, bad := range []int{-1, MaxCompactDays + 1} {
		if _, err := nightlyCompactionCutoff(now, bad); err == nil {
			t.Errorf("keep=%d accepted", bad)
		}
	}
	got, err := nightlyCompactionCutoff(now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("keep=3 cutoff = %s, want %s", got, want)
	}
}

func TestMaxCompactDaysMatchesConfigBound(t *testing.T) {
	if MaxCompactDays != config.MaxActivityLogFullDetailDays {
		t.Fatalf("maintenance.MaxCompactDays=%d, config.MaxActivityLogFullDetailDays=%d", MaxCompactDays, config.MaxActivityLogFullDetailDays)
	}
}

// TestNightlyCompact_RunUsesInjectedClock drives the op end to end and checks
// the cutoff handed to the single compaction entry point.
func TestNightlyCompact_RunUsesInjectedClock(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	orig := nightlyNow
	nightlyNow = func() time.Time { return time.Date(2026, 9, 14, 0, 10, 0, 0, ny) }
	t.Cleanup(func() { nightlyNow = orig })

	d := &nightlyDeps{keep: 0}
	p := New(d)
	rep := &resultReporter{}
	if err := p.runNightlyCompactActivityLog(context.Background(), nil, rep); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 14, 0, 0, 0, 0, ny); !d.gotCutoff.Equal(want) {
		t.Fatalf("cutoff = %s, want %s", d.gotCutoff, want)
	}
	if len(rep.progress) == 0 {
		t.Fatal("no progress forwarded; the watchdog would strike the op")
	}
	if _, ok := rep.result.(CompactActivityLogResult); !ok {
		t.Fatalf("result not persisted: %T", rep.result)
	}
}

func TestNightlyCompactDef_Shape(t *testing.T) {
	def := New(fakeDeps{}).nightlyCompactActivityLogDef()
	if def.ConcurrencyKey != "maintenance.cleanup-activity-log" {
		t.Errorf("ConcurrencyKey = %q; must share the cleanup key", def.ConcurrencyKey)
	}
	if def.Timeout != 6*time.Hour {
		t.Errorf("Timeout = %s, want 6h", def.Timeout)
	}
}
