// file: internal/books/cutover/config_schedule_placement_test.go
// version: 0.1.0
// guid: 6f2c8d41-9a3e-4b57-8e10-c4d7a92b5f36
// last-edited: 2026-11-21

package cutover

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// everyIs reports whether a decoded schedule's every is the duration want
// ("" for none): config spells a duration its own way ("1h30m").
func everyIs(v any, want string) bool {
	if want == "" {
		return v == nil
	}
	s, _ := v.(string)
	got, err1 := time.ParseDuration(s)
	w, err2 := time.ParseDuration(want)
	return err1 == nil && err2 == nil && got == w
}

// scheduledBlob sets HEAD's scheduled.<name> entries.
func scheduledBlob(entries map[string]map[string]any) func(*rig) {
	return mutateBlob(func(tree map[string]any) {
		sched, _ := tree["scheduled"].(map[string]any)
		if sched == nil {
			sched = map[string]any{}
		}
		for name, e := range entries {
			sched[name] = e
		}
		tree["scheduled"] = sched
	})
}

// scheduleAt is the canonical schedule M2 left at path (staged, else the
// default), decoded.
func scheduleAt(t *testing.T, snap *snapshot, path string) map[string]any {
	t.Helper()
	raw, _ := snap.Effective(path)
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s = %s: %v", path, raw, err)
	}
	return v
}

func requireNoScheduleQuarantine(t *testing.T, snap *snapshot, names ...string) {
	t.Helper()
	for _, n := range names {
		if q, ok := quarantineFor(snap, "setting:config_blob#scheduled."+n); ok {
			t.Errorf("scheduled.%s was quarantined (%s), want it carried", n, q.Reason)
		}
	}
}

// Every scheduled entry HEAD's window op ran keeps its window and position in
// the canonical value, timer or not: config stores a schedule key whole, so a
// value carrying only enabled and every would take the task out of the
// nightly window (F5-14b's finding against #4840).
func TestConfigCutover_ScheduleKeepsWindowPlacement(t *testing.T) {
	// The HEAD scheduled.* entries HEAD's window op ran.
	placed := []string{"reconcile", "dedup_refresh", "author_split", "series_prune", "db_optimize", "library_scan"}
	entries := map[string]map[string]any{}
	for _, name := range placed {
		entries[name] = map[string]any{"enabled": true, "interval": 90}
	}
	snap, err := newRig(t, "stage1", scheduledBlob(entries)).run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	for _, name := range placed {
		order := headMaintenanceOrder[name]
		v := scheduleAt(t, snap, "modules.books.scheduler.schedules."+name)
		gotOrder, _ := v["order"].(float64)
		if v["window"] != HeadMaintenanceWindow || int(gotOrder) != order || !everyIs(v["every"], "90m") || v["enabled"] != true {
			t.Errorf("scheduled.%s -> %v, want enabled, every 1h30m, window %s, order %d", name, v, HeadMaintenanceWindow, order)
		}
	}
	if v := scheduleAt(t, snap, "core.storage.schedules.compact"); v["window"] != HeadMaintenanceWindow {
		t.Errorf("core.storage.schedules.compact -> %v, want it kept in the %s window", v, HeadMaintenanceWindow)
	}
	requireNoScheduleQuarantine(t, snap, "reconcile", "dedup_refresh", "author_split", "series_prune", "db_optimize", "library_scan")
}

// An entry HEAD held enabled with interval 0 ran window-only when HEAD's
// window op ran it: it converts to an enabled window-only schedule, which
// config accepts, instead of an enabled schedule with no timing, which it
// refuses (and M2 would quarantine, turning the task off).
func TestConfigCutover_ScheduleEnabledNoIntervalRunsWindowOnly(t *testing.T) {
	snap, err := newRig(t, "stage1", scheduledBlob(map[string]map[string]any{
		"series_prune": {"enabled": true, "interval": 0},
		"author_split": {"enabled": true, "interval": 0},
		"reconcile":    {"enabled": true, "interval": 0},
	})).run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	for _, name := range []string{"series_prune", "author_split", "reconcile"} {
		v := scheduleAt(t, snap, "modules.books.scheduler.schedules."+name)
		if v["enabled"] != true || v["window"] != HeadMaintenanceWindow || v["every"] != nil {
			t.Errorf("scheduled.%s -> %v, want enabled window-only in %s", name, v, HeadMaintenanceWindow)
		}
	}
	requireNoScheduleQuarantine(t, snap, "series_prune", "author_split", "reconcile")
}

// ai_dedup_batch's interval 0 is 24h in HEAD (GetInterval's fallback), so the
// enabled entry keeps a daily timer.
func TestConfigCutover_ScheduleAIDedupBatchZeroIsDaily(t *testing.T) {
	snap, err := newRig(t, "stage1", scheduledBlob(map[string]map[string]any{
		"ai_dedup_batch": {"enabled": true, "interval": 0},
	})).run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	v := scheduleAt(t, snap, "modules.books.scheduler.schedules.ai_dedup_batch")
	if v["enabled"] != true || !everyIs(v["every"], "24h") {
		t.Errorf("scheduled.ai_dedup_batch -> %v, want enabled every 24h", v)
	}
	requireNoScheduleQuarantine(t, snap, "ai_dedup_batch")
}

// An entry enabled with no interval and no window never ran in HEAD (its
// scheduler logged "can NEVER run"): it is carried disabled, not quarantined.
func TestConfigCutover_ScheduleEnabledNeverRunIsCarriedDisabled(t *testing.T) {
	snap, err := newRig(t, "stage1", scheduledBlob(map[string]map[string]any{
		"label_refinement": {"enabled": true, "interval": 0},
	})).run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	if v := scheduleAt(t, snap, "modules.books.scheduler.schedules.label_refinement"); v["enabled"] != false {
		t.Errorf("scheduled.label_refinement -> %v, want disabled", v)
	}
	requireNoScheduleQuarantine(t, snap, "label_refinement")
}

// Convert and Unconvert agree on a placed target, and Unconvert refuses a
// window or position HEAD's maintenance order does not give the task.
func TestSchedulePlacement_RoundTripAndRefusal(t *testing.T) {
	tg := Target{Kind: TargetKey, Path: "modules.books.scheduler.schedules.series_prune", Convert: ConvertScheduleStandard, Window: HeadMaintenanceWindow, Order: 4}
	canon, dropped, err := Convert(tg, json.RawMessage(`{"enabled":true,"interval":0}`))
	if err != nil || len(dropped) != 0 {
		t.Fatalf("Convert = %s, %v, %v", canon, dropped, err)
	}
	back, err := Unconvert(tg, canon)
	if err != nil || string(back) != `{"enabled":true,"interval":0}` {
		t.Fatalf("Unconvert(%s) = %s, %v", canon, back, err)
	}
	for _, in := range []string{
		`{"enabled":true,"window":"gotham_night","order":4}`,
		`{"enabled":true,"window":"nightly","order":9}`,
		`{"enabled":true,"every":"1h","order":4}`,
	} {
		if _, err := Unconvert(tg, json.RawMessage(in)); !errors.Is(err, ErrNotRepresentable) {
			t.Errorf("Unconvert(%s) = %v, want ErrNotRepresentable", in, err)
		}
	}
	// A value in no window is HEAD's timer-only reading of the entry.
	if back, err := Unconvert(tg, json.RawMessage(`{"enabled":true,"every":"1h"}`)); err != nil || !strings.Contains(string(back), `"interval":60`) {
		t.Errorf("Unconvert(no window) = %s, %v", back, err)
	}
	// The never-run case reports its enabled dropped.
	never := Target{Kind: TargetKey, Path: "modules.books.scheduler.schedules.label_refinement", Convert: ConvertScheduleStandard}
	if out, dropped, err := Convert(never, json.RawMessage(`{"enabled":true,"interval":0}`)); err != nil || len(dropped) != 1 || dropped[0] != "enabled" || !strings.Contains(string(out), `"enabled":false`) {
		t.Errorf("Convert(never-run) = %s, %v, %v", out, dropped, err)
	}
}

// library_scan's entry enabled moves to modules.books.scan.periodic_enabled
// and its schedule key stays enabled (form A): the runtime gates read the new
// key, so a disabled entry keeps its interval and placement for when it is
// turned back on, and interval 0 is window-only.
func TestConfigCutover_LibraryScanEnabledMovesToPeriodicEnabled(t *testing.T) {
	for _, c := range []struct {
		name     string
		entry    map[string]any
		every    string
		periodic bool
	}{
		{"disabled with an interval", map[string]any{"enabled": false, "interval": 360}, "6h", false},
		{"enabled with interval 0", map[string]any{"enabled": true, "interval": 0}, "", true},
		{"enabled with an interval", map[string]any{"enabled": true, "interval": 1440}, "24h", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			snap, err := newRig(t, "stage1", scheduledBlob(map[string]map[string]any{"library_scan": c.entry})).run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			v := scheduleAt(t, snap, "modules.books.scheduler.schedules.library_scan")
			order, _ := v["order"].(float64)
			if v["enabled"] != true || !everyIs(v["every"], c.every) || v["window"] != HeadMaintenanceWindow || int(order) != 19 {
				t.Errorf("schedules.library_scan = %v, want enabled, every %v, window %s, order 19", v, c.every, HeadMaintenanceWindow)
			}
			raw, _ := snap.Effective("modules.books.scan.periodic_enabled")
			if string(raw) != map[bool]string{true: "true", false: "false"}[c.periodic] {
				t.Errorf("scan.periodic_enabled = %s, want %t", raw, c.periodic)
			}
			requireNoScheduleQuarantine(t, snap, "library_scan", "library_scan.enabled")
		})
	}
}
