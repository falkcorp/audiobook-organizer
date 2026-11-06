// file: core/rbac/checker.go
// version: 1.0.0
// guid: 2d4020d8-748a-4a7a-bccc-6bbd2760b307
// last-edited: 2026-11-06

package rbac

// Checker is a compiled, immutable permission set. It is an interface here and
// nothing more: the implementation (core/internal/authz) and the Service that
// builds one land at F4, and it names nothing beyond the leaf types of this
// package (05-rbac §3, "Landing").
type Checker interface {
	Has(s Permission) bool
	Satisfies(r Requirement) bool
	Confined() bool         // every concrete scope held is Confining (§4.10)
	Concrete() []Permission // expanded against the catalog, for /me and UI hints
}
