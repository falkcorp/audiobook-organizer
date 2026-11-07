// file: core/rbac/namespaces.go
// version: 1.0.0
// guid: 5026cf27-cf9b-48ec-a4ad-d631dc6f62b0
// last-edited: 2026-11-07

package rbac

// ReservedNamespaces returns the permission namespaces core owns ("core",
// "custom"); one input of module.ReservedIDs() (03 §3.6). A function, not an
// exported slice variable (R5): every call returns a fresh slice.
func ReservedNamespaces() []string {
	return []string{"core", "custom"}
}
