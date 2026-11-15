// file: internal/plugins/maintenance/export_t6g5_test.go
// version: 1.1.0
// guid: cb138cc9-3e25-4e93-bb13-9c0b1a1e2ddb
// last-edited: 2026-11-13

package maintenance

import (
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// T6G5StartRequirement exposes the Perms.Start the group's settings.manage
// definitions are built with, for the external atom-table drift test.
func T6G5StartRequirement() rbac.Requirement { return rbac.AllOf(t6g5SettingsManageScopes()...) }

// T6G5Kinds exposes the group's native definitions, built over empty deps, for
// the external tests that read their declared common fields.
func T6G5Kinds() []tasks.Definition { return (&Plugin{}).t6g5Kinds(t6g5Deps{}, nil) }
