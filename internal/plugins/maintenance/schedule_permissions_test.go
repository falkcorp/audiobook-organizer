// file: internal/plugins/maintenance/schedule_permissions_test.go
// version: 1.0.0
// guid: 8d1f6a47-2c3e-49b5-b0a8-6e7f4c915d23
// last-edited: 2026-10-10

package maintenance

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// The nightly scheduler now enqueues both report-only checks. They must carry
// settings.manage explicitly rather than lean on the empty-means-settings.manage
// default, and the mock def used by the handler permission test must not be able
// to drift from the real one.
func TestScheduledReportOpsRequireSettingsManage(t *testing.T) {
	p := &Plugin{}
	want := []auth.Permission{auth.PermSettingsManage}
	for name, perms := range map[string][]auth.Permission{
		"file-integrity-check":      p.integrityCheckDef().Permissions,
		"orphan-book-files-cleanup": p.orphanBookFilesCleanupDef().Permissions,
	} {
		if !reflect.DeepEqual(perms, want) {
			t.Errorf("%s: Permissions = %v, want %v", name, perms, want)
		}
	}
}
