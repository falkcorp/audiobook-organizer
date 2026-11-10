// file: core/config/health_internal_test.go
// version: 1.0.0
// guid: 2c8e4191-7161-4e33-9348-aece40d53ddf
// last-edited: 2026-11-10

package config

import (
	"context"
	"errors"
	"testing"
)

// TestSyncQuarantineHealth (05-config §1.4 step 6, §1.5): the quarantine
// health tokens follow the quarantine maps both ways. A quarantined value
// raises its layer's token, and the last one cleared lowers it; a boot record
// set aside as .bad keeps config_boot_record_quarantined raised. Apply only
// clears quarantines today, so raising is shown here, on the store itself.
func TestSyncQuarantineHealth(t *testing.T) {
	s, err := NewStore(Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	bad := errors.New("the Penguin's umbrella is out of range")
	check := func(step string, wantOverrides, wantBoot bool) {
		t.Helper()
		if _, ok := s.health[healthOverridesQuarantined]; ok != wantOverrides {
			t.Errorf("%s: %s raised = %v, want %v", step, healthOverridesQuarantined, ok, wantOverrides)
		}
		if _, ok := s.health[healthBootRecordQuarantined]; ok != wantBoot {
			t.Errorf("%s: %s raised = %v, want %v", step, healthBootRecordQuarantined, ok, wantBoot)
		}
	}
	s.quar["core.batcave.level"] = bad
	s.bootQuar["core.batcave.mode"] = bad
	s.syncQuarantineHealth()
	check("both quarantined", true, true)
	delete(s.quar, "core.batcave.level")
	delete(s.bootQuar, "core.batcave.mode")
	s.syncQuarantineHealth()
	check("both cleared", false, false)
	s.bootBadFile = true
	s.syncQuarantineHealth()
	check("boot record set aside", false, true)
}
