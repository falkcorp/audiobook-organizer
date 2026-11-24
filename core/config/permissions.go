// file: core/config/permissions.go
// version: 0.1.0
// guid: a14e6c0b-5d27-4f83-8b9e-72c1d03f6a58
// last-edited: 2026-11-21

package config

// ReadPermission is the permission that lets a caller read the registered
// section at path: the read half of config.Permissions(read, write), or
// 05-config §1.3.4's default ("<module>:settings.manage" for a module's
// section, "core:settings.manage" otherwise). It is a plain string because
// config (L3) names no rbac type; the settings routes (F4-05) take it as an
// rbac.Permission.
//
// path must be a section's own path (KeyState.Section): a key's path, a
// section's parent or a path no section registered answers false, so a caller
// that filters by it reads nothing it cannot name. It takes no lock, so it is
// safe inside the callbacks the store runs under its mutex (Schema's include).
func (s *Store) ReadPermission(path Path) (string, bool) {
	m := s.readPerms.Load()
	if m == nil {
		return "", false
	}
	perm, ok := (*m)[path]
	return perm, ok
}
