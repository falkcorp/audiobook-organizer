// file: internal/plugins/maintenance/export_t6g5_test.go
// version: 1.0.0
// guid: cb138cc9-3e25-4e93-bb13-9c0b1a1e2ddb
// last-edited: 2026-11-13

package maintenance

import "github.com/falkcorp/mediacore/rbac"

// T6G5StartRequirement exposes the Perms.Start the group's settings.manage
// definitions are built with, for the external atom-table drift test.
func T6G5StartRequirement() rbac.Requirement { return rbac.AllOf(t6g5SettingsManageScopes()...) }
