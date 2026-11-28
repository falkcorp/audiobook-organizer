// file: core/backup/system_start_test.go
// version: 1.0.0
// guid: 7e3b9d04-5a16-4c82-b0f7-1d29c8a64e53
// last-edited: 2026-11-28

package backup_test

import (
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TestScheduledKindsCarrySystemStart: the schedule and the SystemStart flag agree
// on every backup definition. The scheduler starts create and verify as core's
// system actor, which cannot hold their Start (core:system.manage is
// AuthRequired), so those two carry the reviewed reason; prune and stage-restore
// have no Schedule and carry none. Start itself is untouched: a person's or an
// API key's start still needs core:system.manage.
func TestScheduledKindsCarrySystemStart(t *testing.T) {
	cases := []struct {
		def       tasks.Definition
		scheduled bool
	}{
		{backup.CreateTask(nil), true},
		{backup.VerifyTask(nil), true},
		{backup.PruneTask(nil), false},
		{backup.StageRestoreTask(nil), false},
	}
	for _, tc := range cases {
		c := tasks.CommonOf(tc.def)
		if got := c.Schedule != nil; got != tc.scheduled {
			t.Errorf("%s: Schedule declared = %v, want %v", tc.def.Kind(), got, tc.scheduled)
		}
		if got := c.Perms.SystemStart != ""; got != tc.scheduled {
			t.Errorf("%s: SystemStart set = %v, want %v (the flag and the schedule agree)", tc.def.Kind(), got, tc.scheduled)
		}
		if tc.scheduled {
			if want := rbac.Need("core:system.manage").Permissions(); len(c.Perms.Start.Permissions()) != 1 || c.Perms.Start.Permissions()[0] != want[0] {
				t.Errorf("%s: Start = %v, want core:system.manage unchanged", tc.def.Kind(), c.Perms.Start.Permissions())
			}
		}
	}
}
