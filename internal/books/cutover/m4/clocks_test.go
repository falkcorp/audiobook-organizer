// file: internal/books/cutover/m4/clocks_test.go
// version: 1.0.0
// guid: 7d2c9e41-a085-4b36-9f7d-2e18c40b6a93
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

func clockOf(ctx context.Context, t *testing.T, s *snap, k tasks.Kind) (tasks.Clock, bool) {
	t.Helper()
	c, ok, err := s.Stores.Clocks.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	return c, ok
}

// TestCutover_ClocksMergedPerKind: a kind's clock comes from every source row
// that names the kind (a def's v3 schedule state, the interval clock of the
// task that fires it, the full sweep's and the window's own clocks), merged to
// the latest time of each field and imported once; the clocks of a task that
// fires nothing are dropped, and one the scheduler never had is quarantined.
func TestCutover_ClocksMergedPerKind(t *testing.T) {
	t1 := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	t3 := t1.Add(48 * time.Hour)
	t4 := t1.Add(72 * time.Hour)
	gate := t1.Add(time.Hour)
	raw := map[string]string{}
	raw["opv3:sched:library.scan"] = schedRowJSON("library.scan", &t1, &gate, 2)
	// A schedule state of a def the flip deletes: history only, no clock.
	raw["opv3:sched:operations.backfill-legacy-status"] = schedRowJSON("operations.backfill-legacy-status", &t1, nil, 0)
	setting(raw, "scheduled_task_last_tick.library_scan", t2.Format(time.RFC3339))
	setting(raw, "scheduled_task_last_tick.library_scan_full", t3.Format(time.RFC3339))
	setting(raw, "library_scan_full_last_run", t4.Format(time.RFC3339))
	setting(raw, "scheduled_task_last_tick.batch_poller", t2.Format(time.RFC3339))
	setting(raw, "scheduled_task_last_tick.transcode", t2.Format(time.RFC3339))
	setting(raw, "scheduled_task_last_tick.joker_laugh", t2.Format(time.RFC3339))
	setting(raw, "scheduled_task_last_tick.dedup_refresh", "not a time")
	setting(raw, "maintenance_window_last_run", "2026-11-04")

	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Errorf("source rows left: %v", keysOf(s.Left))
		}
		scan, ok := clockOf(ctx, t, s, "books.library.scan")
		if !ok || !scan.LastDue.Equal(t2) || !scan.LastFire.Equal(t1) || !scan.LastRun.Equal(t1) || scan.Missed != 2 || !scan.GateClosedAt.Equal(gate) || scan.ScheduleHash != "" {
			t.Errorf("books.library.scan clock = %+v (%v), want due %v fire %v missed 2 gate %v", scan, ok, t2, t1, gate)
		}
		full, ok := clockOf(ctx, t, s, "books.library.scan-full")
		if !ok || !full.LastDue.Equal(t4) {
			t.Errorf("books.library.scan-full clock = %+v (%v), want due %v (the sweep's own clock; its hourly due-check tick is no clock)", full, ok, t4)
		}
		poll, ok := clockOf(ctx, t, s, "core.ai.batch-poll")
		if !ok || !poll.LastDue.Equal(t2) {
			t.Errorf("core.ai.batch-poll clock = %+v (%v)", poll, ok)
		}
		// The window ran on 2026-11-04 in Gotham (UTC-5): its clock is that
		// day's local midnight, five hours after the UTC one.
		win, ok := clockOf(ctx, t, s, "core.window.nightly")
		wantWin := time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)
		if !ok || !win.LastDue.Equal(wantWin) || !win.LastRun.Equal(wantWin) {
			t.Errorf("core.window.nightly clock = %+v (%v), want %v", win, ok, wantWin)
		}
		if _, ok := clockOf(ctx, t, s, "books.retired.operations.backfill-legacy-status"); ok {
			t.Error("a clock was imported for a retired kind")
		}
		if _, ok := clockOf(ctx, t, s, "books.transcode"); ok {
			t.Error("a clock was imported for the task that fires nothing")
		}
		quar := map[string]string{}
		for _, q := range s.Quarantine {
			quar[q.Key] = q.Reason
		}
		if len(quar) != 2 || quar["setting:scheduled_task_last_tick.joker_laugh"] == "" || quar["setting:scheduled_task_last_tick.dedup_refresh"] == "" {
			t.Errorf("quarantine = %v, want the unknown task and the unparseable time", quar)
		}
		for k, why := range quar {
			if strings.Contains(why, "not a time") || strings.Contains(why, "joker") {
				t.Errorf("%s: reason %q quotes a value", k, why)
			}
		}
	})
}

// TestM4_WindowClockLocalMidnight: the window's date is read in the injected
// zone, so the same stored date yields a different instant in a different zone
// (a build that read it as UTC would put west-of-UTC windows hours ahead and run
// the window twice on its own day).
func TestM4_WindowClockLocalMidnight(t *testing.T) {
	for _, tc := range []struct {
		name string
		zone *time.Location
		want time.Time
	}{
		{"west", gotham, time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)},
		{"utc", time.UTC, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)},
		{"east", time.FixedZone("Metropolis", 9*3600), time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]string{}
			setting(raw, "maintenance_window_last_run", "2026-11-04")
			r := newRig(t, withRaw(raw), func(r *rig) { r.zone = tc.zone })
			r.runObserve(t, func(ctx context.Context, s *snap) {
				win, ok := clockOf(ctx, t, s, "core.window.nightly")
				if !ok || !win.LastDue.Equal(tc.want) || !win.LastRun.Equal(tc.want) {
					t.Errorf("clock = %+v (%v), want %v", win, ok, tc.want)
				}
			})
		})
	}
}

// TestCutover_PauseAndStandDownMarkers: a set pause marker and a live
// stand-down marker become the native ones; HEAD's cleared sentinels (a blank
// value, a marker with Paused false or no scan op) import nothing and still go.
func TestCutover_PauseAndStandDownMarkers(t *testing.T) {
	since := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	lease := since.Add(time.Hour)
	pause, _ := json.Marshal(map[string]any{"paused": true, "reason": "Arkham maintenance", "by": "bruce.wayne", "since_unix_nano": since.UnixNano()})
	hold, _ := json.Marshal(map[string]any{"holder_op_id": runID(1), "scan_op_id": runID(2), "lease_expiry_unix_nano": lease.UnixNano()})
	raw := map[string]string{}
	setting(raw, "registry.operations_paused", string(pause))
	setting(raw, "registry.scan_standdown", string(hold))
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Errorf("source rows left: %v", keysOf(s.Left))
		}
		p, ok, err := s.Stores.Pause.Get(ctx)
		if err != nil || !ok || p.Reason != "Arkham maintenance" || p.By.UserID != "bruce.wayne" || !p.At.Equal(since) {
			t.Errorf("pause = %+v, %v, %v", p, ok, err)
		}
		holds := collect(t, func(y func(tasks.StandDown, error) bool) { s.Stores.StandDown.List(ctx)(y) })
		if len(holds) != 1 || holds[0].Key != scanKey || holds[0].Holder != tasks.RunID(runID(1)) || len(holds[0].Quiesced) != 1 || holds[0].Quiesced[0] != tasks.RunID(runID(2)) || !holds[0].Expires.Equal(lease) {
			t.Errorf("stand-down holds = %+v", holds)
		}
	})

	for name, c := range map[string]struct{ pause, hold string }{
		"resumed-and-blank":    {`{"paused":false}`, ""},
		"blank-and-no-scan-op": {"", `{"holder_op_id":"` + runID(1) + `","scan_op_id":"","lease_expiry_unix_nano":0}`},
	} {
		t.Run(name, func(t *testing.T) {
			cleared := map[string]string{}
			setting(cleared, "registry.operations_paused", c.pause)
			setting(cleared, "registry.scan_standdown", c.hold)
			r := newRig(t, withRaw(cleared))
			r.runObserve(t, func(ctx context.Context, s *snap) {
				if len(s.Left) != 0 || len(s.Quarantine) != 0 {
					t.Errorf("left %v, quarantined %+v", keysOf(s.Left), s.Quarantine)
				}
				if _, ok, _ := s.Stores.Pause.Get(ctx); ok {
					t.Error("a resumed gate's marker imported a pause")
				}
				if holds := collect(t, func(y func(tasks.StandDown, error) bool) { s.Stores.StandDown.List(ctx)(y) }); len(holds) != 0 {
					t.Errorf("a cleared stand-down imported %+v", holds)
				}
			})
		})
	}

	garbage := map[string]string{}
	setting(garbage, "registry.operations_paused", "{not json")
	r = newRig(t, withRaw(garbage))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Quarantine) != 1 || s.Quarantine[0].Key != "setting:registry.operations_paused" {
			t.Errorf("quarantine = %+v, want the undecodable pause marker", s.Quarantine)
		}
	})
}

// TestCutover_DebounceBucketsMerged: HEAD's per-subject op:batch: rows become
// one bucket per kind, first and last arrival the earliest and latest, every
// subject listed; an undecodable row is set aside by itself; a retired kind's
// pending starts are dropped.
func TestCutover_DebounceBucketsMerged(t *testing.T) {
	a := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	raw := map[string]string{
		"op:batch:dedup.book-scan:book:killing-joke":                         batchRowJSON("book", "killing-joke", a.Add(time.Minute)),
		"op:batch:dedup.book-scan:book:year-one":                             batchRowJSON("book", "year-one", a),
		"op:batch:dedup.book-scan:book_file:the-cult-1":                      batchRowJSON("book_file", "the-cult-1", a.Add(2*time.Minute)),
		"op:batch:dedup.book-scan:book:zzz-broken":                           "not json",
		"op:batch:operations.backfill-legacy-status:book:a-death-in-the-fam": batchRowJSON("book", "a-death-in-the-fam", a),
		"op:batch:metafetch.asin-backfill:book:arkham-asylum":                batchRowJSON("book", "arkham-asylum", a.Add(time.Hour)),
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		buckets := collect(t, func(y func(tasks.DebounceBucket, error) bool) { s.Stores.Runs.DebounceBuckets(ctx)(y) })
		byKind := map[tasks.Kind]tasks.DebounceBucket{}
		for _, b := range buckets {
			byKind[b.Kind] = b
		}
		scan, ok := byKind["books.dedup.book-scan"]
		if !ok || !scan.First.Equal(a) || !scan.Last.Equal(a.Add(2*time.Minute)) || len(scan.Subjects) != 3 || scan.Bucket != "" {
			t.Errorf("books.dedup.book-scan bucket = %+v (%v)", scan, ok)
		}
		if _, ok := byKind["books.metafetch.asin-backfill"]; !ok {
			t.Error("the second kind's bucket is missing")
		}
		if len(buckets) != 2 {
			t.Errorf("%d buckets, want 2 (the retired kind's starts are dropped): %+v", len(buckets), buckets)
		}
		if len(s.Left) != 0 {
			t.Errorf("source rows left: %v", keysOf(s.Left))
		}
		if len(s.Quarantine) != 1 || s.Quarantine[0].Key != "op:batch:dedup.book-scan:book:zzz-broken" {
			t.Errorf("quarantine = %+v, want the one undecodable row", s.Quarantine)
		}
	})
}

// TestCutover_SharedSettingFamilyOnlyRunKeysMove: of the shared setting: family
// only the run keys of 5T.8.3 move; every other row is byte for byte what it was.
func TestCutover_SharedSettingFamilyOnlyRunKeysMove(t *testing.T) {
	other := map[string]string{}
	setting(other, "ui_theme", "dark-knight")
	setting(other, "scheduled_task_last_tick", "kept") // no trailing dot: not the interval clocks' prefix
	setting(other, "registry.operations_paused_note", "kept")
	setting(other, "library_scan_full_last_run_note", "kept")
	raw := map[string]string{}
	maps.Copy(raw, other)
	setting(raw, "library_scan_full_last_run", time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC).Format(time.RFC3339))
	setting(raw, "registry.operations_paused", `{"paused":false}`)
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		for k, want := range other {
			got, err := s.legacyGet(ctx, k)
			if err != nil || got != want {
				t.Errorf("%s = %q, %v; want it untouched (%q)", k, got, err, want)
			}
		}
	})
}

// TestCutover_ClockRowTakenByItsGroupIsLeftAlone: a worker handed a clock row
// that another shard's group batch has taken since the runner read it (the row
// is gone from the live store) defers: it neither imports nor panics on a row it
// cannot find. The row the runner still holds is then the leftover Verify
// reports, which here is the point: nothing was written for it.
func TestCutover_ClockRowTakenByItsGroupIsLeftAlone(t *testing.T) {
	raw := map[string]string{}
	setting(raw, "maintenance_window_last_run", "2026-11-04")
	r := newRig(t, withRaw(raw), func(r *rig) {
		r.view = func(key string) ([]byte, bool, error) {
			return nil, key == "setting:maintenance_window_last_run", storage.ErrNotFound
		}
	})
	s, err := r.runWith(t, r.seed(t), r.migration())
	if err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("boot = %+v, %v; want Verify's finding for the row nobody imported", s, err)
	}
}

// TestCutover_ClockGroupDriverCommitsBetweenReads: a clock group is read in
// several steps (a scan per family, a Get per exact key), and a worker that
// started before the group's driver committed can finish its reads after it:
// the driver has taken every row of the group, so the late reads find the group
// without the driver's rows and the worker, now the lowest row it can see,
// would import a clock built from its own row alone, over the driver's merged
// one. The worker re-reads its own row last and leaves a group already taken:
// it writes no clock.
//
// No group of HEAD's rows reaches that guard today: the race needs a group whose
// lowest key (the driver) is read after another of its rows, so an exact key
// (the sweep's or the window's clock) grouped with an interval clock. The only
// such pair was library_scan_full's due-check tick with the sweep's clock, and
// the tick is no clock (TestCutover_FullSweepTickIsNotItsClock). The guard
// stays for a later mapping that adds one, and this test drives it with a
// synthetic view: the window's group with a v3 schedule state of its def, the
// driver's row hidden from the scan and the worker's own row from its re-read.
func TestCutover_ClockGroupDriverCommitsBetweenReads(t *testing.T) {
	early := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	const sched, window = "opv3:sched:maintenance.window", "setting:maintenance_window_last_run"
	raw := map[string]string{}
	raw[sched] = schedRowJSON("maintenance.window", &early, nil, 0)
	setting(raw, "maintenance_window_last_run", "2026-11-04")
	r := newRig(t, withRaw(raw), func(r *rig) {
		r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
	})
	dm := r.migration()
	inner := dm.Step
	handed := 0
	dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		handed++
		if handed != 1 {
			return inner(ctx, kv, w)
		}
		// The runner hands the higher row (the window's) first, as a parallel
		// shard can, and the driver's commit lands after the worker's read of
		// the window key and before its re-read: the driver's row is gone from
		// the scan, and the worker's own row from the re-read.
		gets := 0
		r.scanHide = func(key string) bool { return key == sched }
		r.view = func(key string) ([]byte, bool, error) {
			if key != window {
				return nil, false, nil
			}
			gets++
			return nil, gets > 1, storage.ErrNotFound
		}
		defer func() { r.view, r.scanHide = nil, nil }()
		err := inner(ctx, storage.KV{Key: []byte(window), Value: []byte(raw[window])}, w)
		if gets < 2 {
			t.Errorf("the worker read its own row %d times, want the group read and the re-read", gets)
		}
		if n := r.imp.clockCalls("core.window.nightly"); n != 0 {
			t.Errorf("%d clock imports from a worker that read a group the driver had already taken", n)
		}
		return err
	}
	// Only the first call is the race; the boot then ends with the rows still
	// there, which Verify reports.
	if _, err := r.runWith(t, r.seed(t), dm); err == nil || !strings.Contains(err.Error(), "still present") {
		t.Errorf("boot = %v, want Verify's finding for the rows the swapped call never handled", err)
	}
}

// TestCutover_FullSweepTickIsNotItsClock: library_scan_full's interval clock is
// HEAD's hourly due check (fullSweepDueCheck stamps it every hour whether or not
// a sweep runs); the weekly sweep's clock is library_scan_full_last_run alone.
// A tick later than the last sweep (the live shape: the check ran after the
// sweep) leaves the sweep due at last run plus a period, not at cutover plus a
// period; a tick alone imports no clock. The tick row is dropped, not
// quarantined.
func TestCutover_FullSweepTickIsNotItsClock(t *testing.T) {
	lastRun := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	tick := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		sweep   bool
		wantDue time.Time
	}{
		{"tick after the last sweep", true, lastRun},
		{"tick alone", false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]string{}
			setting(raw, "scheduled_task_last_tick.library_scan_full", tick.Format(time.RFC3339))
			if tc.sweep {
				setting(raw, "library_scan_full_last_run", lastRun.Format(time.RFC3339))
			}
			r := newRig(t, withRaw(raw))
			r.runObserve(t, func(ctx context.Context, s *snap) {
				if len(s.Left) != 0 {
					t.Errorf("source rows left: %v", keysOf(s.Left))
				}
				if len(s.Quarantine) != 0 {
					t.Errorf("quarantine = %+v, want none", s.Quarantine)
				}
				full, ok := clockOf(ctx, t, s, "books.library.scan-full")
				if ok != tc.sweep || !full.LastDue.Equal(tc.wantDue) {
					t.Errorf("books.library.scan-full clock = %+v (%v), want due %v (present %v)", full, ok, tc.wantDue, tc.sweep)
				}
			})
		})
	}
}

// TestCutover_DBOptimizeClockFeedsBothHalves: HEAD's db-optimize compacted the
// main store and the two aux stores in one run; core splits it into
// core.storage.compact and books.maintenance.aux-store-compact. Every
// db-optimize clock (the db_optimize interval clock, and the v3 schedule state
// of the def or of its former ID) was the last run of both, so both kinds get
// the same merged clock and each fires at that run plus its period, not one
// period after cutover.
func TestCutover_DBOptimizeClockFeedsBothHalves(t *testing.T) {
	tick := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	fire := tick.Add(5 * 24 * time.Hour)
	for _, tc := range []struct {
		name string
		rows func(raw map[string]string)
		want tasks.Clock
	}{
		{"interval clock", func(raw map[string]string) {
			setting(raw, "scheduled_task_last_tick.db_optimize", tick.Format(time.RFC3339))
		}, tasks.Clock{LastDue: tick}},
		{"interval clock and a later v3 schedule state", func(raw map[string]string) {
			setting(raw, "scheduled_task_last_tick.db_optimize", tick.Format(time.RFC3339))
			raw["opv3:sched:maintenance.db-optimize"] = schedRowJSON("maintenance.db-optimize", &fire, nil, 1)
		}, tasks.Clock{LastDue: fire, LastFire: fire, LastRun: fire, Missed: 1}},
		{"the former ID's schedule state", func(raw map[string]string) {
			raw["opv3:sched:scheduler.db-optimize"] = schedRowJSON("scheduler.db-optimize", &fire, nil, 0)
		}, tasks.Clock{LastDue: fire, LastFire: fire, LastRun: fire}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]string{}
			tc.rows(raw)
			r := newRig(t, withRaw(raw))
			r.runObserve(t, func(ctx context.Context, s *snap) {
				if len(s.Left) != 0 || len(s.Quarantine) != 0 {
					t.Errorf("source rows left %v, quarantined %+v", keysOf(s.Left), s.Quarantine)
				}
				for _, k := range []tasks.Kind{"core.storage.compact", "books.maintenance.aux-store-compact"} {
					c, ok := clockOf(ctx, t, s, k)
					if !ok || !c.LastDue.Equal(tc.want.LastDue) || !c.LastFire.Equal(tc.want.LastFire) || !c.LastRun.Equal(tc.want.LastRun) || c.Missed != tc.want.Missed {
						t.Errorf("%s clock = %+v (%v), want %+v", k, c, ok, tc.want)
					}
					if n := r.imp.clockCalls(k); n != 1 {
						t.Errorf("%s imported %d times, want once", k, n)
					}
				}
			})
		})
	}
}

// TestCutover_DebounceStrayKeySetsTheBucketAside: a row under an op type's
// prefix whose key does not decode as a debounce row (no subject separator) is
// the bucket's: the bucket's lowest row owns it whatever its shape, and the
// stray key sets the whole bucket aside, under a retired kind too (rows that
// cannot be read are not deleted unread), wherever the stray sorts and however
// the runner cuts chunks or shards. Another op type's bucket imports as usual.
func TestCutover_DebounceStrayKeySetsTheBucketAside(t *testing.T) {
	a := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, opType, stray string }{
		{"stray_lowest", "dedup.book-scan", "op:batch:dedup.book-scan:a"},
		{"stray_highest", "dedup.book-scan", "op:batch:dedup.book-scan:zzz"},
		{"stray_retired_kind", "operations.backfill-legacy-status", "op:batch:operations.backfill-legacy-status:a"},
	} {
		for _, mode := range strayModes {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				good1 := "op:batch:" + tc.opType + ":book:killing-joke"
				good2 := "op:batch:" + tc.opType + ":book:year-one"
				raw := map[string]string{
					tc.stray: batchRowJSON("book", "hush", a),
					good1:    batchRowJSON("book", "killing-joke", a),
					good2:    batchRowJSON("book", "year-one", a.Add(time.Minute)),
					"op:batch:metafetch.asin-backfill:book:arkham-asylum": batchRowJSON("book", "arkham-asylum", a),
				}
				r := mode.rig(t, raw)
				r.runObserve(t, func(ctx context.Context, s *snap) {
					whole := "set aside whole"
					quarantineReasons(t, s, map[string]string{
						tc.stray: "key or entry does not decode",
						good1:    whole,
						good2:    whole,
					})
					if len(s.Left) != 0 {
						t.Errorf("left %v", keysOf(s.Left))
					}
					buckets := collect(t, func(y func(tasks.DebounceBucket, error) bool) { s.Stores.Runs.DebounceBuckets(ctx)(y) })
					if len(buckets) != 1 || buckets[0].Kind != "books.metafetch.asin-backfill" {
						t.Errorf("buckets = %+v, want only the other op type's", buckets)
					}
				})
			})
		}
	}
}
