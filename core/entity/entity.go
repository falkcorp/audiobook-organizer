// file: core/entity/entity.go
// version: 1.0.0
// guid: 1ad08dc9-6f55-4839-bcf1-8c84a63c221b
// last-edited: 2026-11-07

// Package entity holds the one polymorphic reference to a module-owned entity.
package entity

// Ref is the one polymorphic reference to a module-owned entity (03 §3.3).
type Ref struct {
	Module string
	Kind   string
	ID     string
}

// String renders the reference as Module/Kind/ID.
func (r Ref) String() string {
	return r.Module + "/" + r.Kind + "/" + r.ID
}
