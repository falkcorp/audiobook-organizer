// file: core/kernel/core_sections_schedule_test.go
// version: 1.0.0
// guid: 142497a5-74e7-4e83-b386-0ab3ddd41c0b
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// scheduleKeys are F2-04's 14 schedule keys with their owner tables'
// defaults (05-ai §7, 05-rbac §10, 05-authn §8, 05-backup §5B.7, 05-health
// §5H.7, 05-metadata M1.7, 05-library §5L.7, 05-logging §8, 05-tasks §5T.10,
// 05-updater §5U.8, 05-storage §5S.4).
func scheduleKeys() map[string]tasks.ScheduleConfig {
	every := func(d time.Duration) tasks.ScheduleConfig {
		return tasks.ScheduleConfig{Enabled: true, Every: config.Duration(d)}
	}
	return map[string]tasks.ScheduleConfig{
		"core.ai.schedules.batch_poll":            every(5 * time.Minute),
		"core.audit.schedules.prune":              every(24 * time.Hour),
		"core.auth.schedules.session_prune":       every(time.Hour),
		"core.auth.schedules.onetime_prune":       every(time.Hour),
		"core.backup.schedules.create":            every(24 * time.Hour),
		"core.backup.schedules.verify":            every(168 * time.Hour),
		"core.diagnostics.schedules.prune":        every(time.Hour),
		"core.integrations.schedules.health":      every(5 * time.Minute),
		"core.library.schedules.quarantine_prune": every(24 * time.Hour),
		"core.library.schedules.transfer_recover": every(24 * time.Hour),
		"core.log.schedules.journal_prune":        every(24 * time.Hour),
		"core.tasks.schedules.prune":              every(24 * time.Hour),
		"core.updater.schedules.check":            every(time.Hour),
		"core.storage.schedules.compact":          {Enabled: true, Window: "nightly"},
	}
}

// TestCoreSections_ScheduleKeysDeclared (name coined by F2-04): the leaf
// paths of type tasks.ScheduleConfig reflected from the core-section list are
// exactly the 14 schedule keys, each with its owner's default and live
// reload; core.tasks.windows.nightly.{start,end,enabled} resolve with
// defaults 1, 4, true; and the core config store registers the list with
// each schedule key as ONE key (tasks.ScheduleConfig encodes itself).
func TestCoreSections_ScheduleKeysDeclared(t *testing.T) {
	want := scheduleKeys()
	all := allLeaves(t)
	var got []string
	for p, l := range all {
		if l.typ == reflect.TypeFor[tasks.ScheduleConfig]() {
			got = append(got, p)
			if l.reload != config.ReloadLive {
				t.Errorf("%s: reload %d, want live (re-read every poll)", p, l.reload)
			}
		}
	}
	sort.Strings(got)
	wantKeys := make([]string, 0, len(want))
	for k := range want {
		wantKeys = append(wantKeys, k)
	}
	sort.Strings(wantKeys)
	if !slices.Equal(got, wantKeys) {
		t.Fatalf("schedule keys:\n got %v\nwant %v", got, wantKeys)
	}
	if !slices.Contains(got, "core.library.schedules.transfer_recover") {
		t.Fatal("core.library.schedules.transfer_recover is not declared")
	}

	// The config store registers the list with each schedule key as one key
	// whose default is the owner's.
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	if _, err := config.RegisterSections(st.Registry(), coreSections()...); err != nil {
		t.Fatalf("register the core sections: %v", err)
	}
	if err := st.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	for k, w := range want {
		ks, ok := st.Lookup(config.Path(k))
		if !ok {
			t.Errorf("config has no key %s", k)
			continue
		}
		var c tasks.ScheduleConfig
		if err := json.Unmarshal(ks.Effective, &c); err != nil || c != w {
			t.Errorf("config %s = %s (%v), want %+v", k, ks.Effective, err, w)
		}
		if _, sub := st.Lookup(config.Path(k + ".enabled")); sub {
			t.Errorf("config split %s into sub-keys", k)
		}
	}

	// core.tasks.windows is one map key; its nightly entry resolves.
	if l, ok := all["core.tasks.windows"]; !ok || !l.hasMap {
		t.Error("core.tasks.windows is not one map key")
	}
	ks, ok := st.Lookup("core.tasks.windows")
	if !ok {
		t.Fatal("config has no key core.tasks.windows")
	}
	var windows map[string]map[string]any
	if err := json.Unmarshal(ks.Effective, &windows); err != nil {
		t.Fatalf("core.tasks.windows = %s: %v", ks.Effective, err)
	}
	for seg, w := range map[string]any{"start": float64(1), "end": float64(4), "enabled": true} {
		if v, ok := windows["nightly"][seg]; !ok || v != w {
			t.Errorf("core.tasks.windows.nightly.%s = %v (present %v), want %v", seg, v, ok, w)
		}
	}
	if len(windows) != 1 {
		t.Errorf("core.tasks.windows default declares %d windows, want only nightly: %s", len(windows), ks.Effective)
	}
}
