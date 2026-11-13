// file: internal/plugins/maintenance/export_test.go
// version: 1.0.0
// guid: 5e1f0c47-3b8a-4d62-9a15-c47d2e8b6f03
// last-edited: 2026-11-13

package maintenance

import "github.com/falkcorp/mediacore/rbac"

// T6G3StartRequirement exposes the Perms.Start the group's settings.manage
// definitions are built with, for the external atom-table drift test.
func T6G3StartRequirement() rbac.Requirement { return rbac.AllOf(t6g3SettingsManageScopes...) }
