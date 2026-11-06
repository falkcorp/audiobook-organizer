// file: core/rbac/role.go
// version: 1.0.0
// guid: 5a59c65f-1c88-42ca-b076-92e4a471ed9a
// last-edited: 2026-11-06

package rbac

// RoleName identifies a role. Core seed roles keep ABO's IDs so imported
// User.Roles need no rewrite (§6.3):
const (
	RoleAdmin  RoleName = "admin"  // locked; holds core:admin
	RoleEditor RoleName = "editor" // core:self + core:tasks.view + core:system.status + module DefaultGrants
	RoleViewer RoleName = "viewer" // core:self + core:tasks.view + core:system.status + module DefaultGrants
)

// Module seed roles: "<ns>.<name>" (e.g. "tv.admin"); custom roles: "custom.<slug>".
type RoleName string
