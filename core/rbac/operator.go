// file: core/rbac/operator.go
// version: 0.1.0
// guid: c27d5e91-3b48-4a06-9f7e-1d60b8a4f253
// last-edited: 2026-11-28

package rbac

// OperatorRequirement is what an operator-only surface that HEAD guarded with
// the settings.manage atom requires in core: every scope the atom grants (the
// scopes below). It is deliberately more than core:system.manage, which a
// custom role can hold alone, and it is one requirement for reading and writing
// alike: an operator surface that lists what an operator may then change does
// not open its reads to core:system.view. The live log tail (core.log.recorded.v1)
// and the outbox operator routes use it, because HEAD had no such surface and its
// nearest, the operations and logs admin routes, sat behind settings.manage.
//
// The scopes are those a holder of HEAD's settings.manage atom holds
// (internal/books/atoms.go, the atom's row): the atom grants them together, so a
// role of HEAD gets all of them or none. The app's own <app>:settings.manage,
// which the atom also grants, is not named: core does not know an app's scopes,
// and the seven core scopes are what no role of HEAD holds without the atom.
func OperatorRequirement() Requirement {
	return AllOf("core:settings.manage", "core:tasks.manage", "core:system.view", "core:system.manage",
		"core:library.manage", "core:library.browse", "core:integrations.manage")
}
