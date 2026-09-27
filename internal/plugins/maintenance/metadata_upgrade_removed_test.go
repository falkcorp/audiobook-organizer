// file: internal/plugins/maintenance/metadata_upgrade_removed_test.go
// version: 1.0.0
// guid: 7a41c9e2-5d38-4b06-a1f7-2e9c8b3d6f15
// last-edited: 2026-09-27

package maintenance

import (
	"strings"
	"testing"
)

// The metadata upgrade has ONE registration, scheduler.metadata-upgrade
// (internal/scheduler/extra_ops.go), which answers to this plugin's old ID as
// a FormerID. The maintenance plugin must not register a second def for it:
// two defs with separate ConcurrencyKeys could run over the same books at once.
func TestMaintenancePlugin_DoesNotRegisterMetadataUpgrade(t *testing.T) {
	reg := &phantomCaptureRegistry{}
	if err := New(fakeDeps{}).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, id := range reg.ids {
		if strings.Contains(id, "metadata-upgrade") {
			t.Fatalf("maintenance plugin registers %q; the upgrade is scheduler.metadata-upgrade only", id)
		}
	}
}
