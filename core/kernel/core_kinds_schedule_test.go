// file: core/kernel/core_kinds_schedule_test.go
// version: 1.0.0
// guid: b2f06d97-3c41-4a58-8e7d-61a9c0f3e254
// last-edited: 2026-11-18

package kernel

import (
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/storagetasks"
	"github.com/falkcorp/mediacore/tasks"
)

// TestCoreKinds_ScheduleKeysDeclared (name coined by F5-03): the schedule key
// each F5-03 core kind passes to tasks.ScheduleFromConfig is a
// tasks.ScheduleConfig leaf the core-section list declares, so the day F5-18
// registers the kinds none of them reads a key that does not exist. The census
// kind is operator-run and declares no schedule.
func TestCoreKinds_ScheduleKeysDeclared(t *testing.T) {
	declared := map[string]bool{}
	for p, l := range allLeaves(t) {
		if l.typ == reflect.TypeFor[tasks.ScheduleConfig]() {
			declared[p] = true
		}
	}
	defs := []tasks.Definition{
		executor.PruneDefinition(nil, nil, nil),
		logjournal.PruneDefinition(nil, nil, nil),
	}
	defs = append(defs, storagetasks.Kinds(nil, nil, storagetasks.Options{})...)
	seen := 0
	for _, d := range defs {
		s := tasks.CommonOf(d).Schedule
		if d.Kind() == storagetasks.KindCensus {
			if s != nil {
				t.Errorf("%s declares a schedule %q; it is operator-run", d.Kind(), s.ConfigKey())
			}
			continue
		}
		if s == nil {
			t.Errorf("%s declares no schedule", d.Kind())
			continue
		}
		seen++
		if !declared[s.ConfigKey()] {
			t.Errorf("%s reads schedule key %q, which the core sections do not declare", d.Kind(), s.ConfigKey())
		}
		if !s.IsLive() {
			t.Errorf("%s: schedule %q is not live: the key is re-read every poll", d.Kind(), s.ConfigKey())
		}
	}
	if seen != 3 {
		t.Errorf("checked %d scheduled kinds, want 3 (compact, tasks prune, journal prune)", seen)
	}
}
