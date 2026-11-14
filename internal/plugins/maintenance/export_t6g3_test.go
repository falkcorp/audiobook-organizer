// file: internal/plugins/maintenance/export_t6g3_test.go
// version: 1.1.0
// guid: 5e1f0c47-3b8a-4d62-9a15-c47d2e8b6f03
// last-edited: 2026-11-13

package maintenance

import (
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// T6G3KindStarts returns the Perms.Start each native kind of the group is
// actually built with, by kind, for the external atom-table drift test. It reads
// the built definitions, never a requirement rebuilt here.
func T6G3KindStarts() map[tasks.Kind]rbac.Requirement {
	out := map[tasks.Kind]rbac.Requirement{}
	for _, d := range (&Plugin{}).t6g3Kinds(t6g3Deps{}, nil) {
		out[d.Kind()] = tasks.CommonOf(d).Perms.Start
	}
	return out
}
