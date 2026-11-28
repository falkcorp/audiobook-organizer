// file: internal/books/taskmanage/taskmanage.go
// version: 0.1.0
// guid: 3e7a1c55-9b02-4d6f-8a14-c2d95e0b7f31
// last-edited: 2026-11-22

// Package taskmanage is books' one answer to "who may manage a books task
// run": the tasks.Permissions.Manage every books kind states (owner ruling
// 2026-11-22). HEAD's v1 operations control routes are gated behind
// settings.manage (v3's cancel and approve narrow to it at the cutover), so a
// books kind's Manage is that atom's native scopes
// (05-rbac §6.1): a caller cancels, discards, decides or approves a books run
// only while it holds all eight, next to the operation's own requirement.
//
// It is a layer-0 leaf (core's rbac only) because every plugin that declares a
// books kind must reach it and internal/books itself imports the plugins; the
// atom table in internal/books keeps its settings.manage row as literal scopes
// (a drift test in the maintenance plugin parses atoms.go's source), and a pin
// test in internal/books holds the two lists together.
package taskmanage

import "github.com/falkcorp/mediacore/rbac"

// Scopes returns the eight native scopes of HEAD's settings.manage atom, in the
// atom table's order. A fresh slice per call.
func Scopes() []rbac.Permission {
	return []rbac.Permission{
		"core:settings.manage", "books:settings.manage", "core:tasks.manage",
		"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
		"core:integrations.manage",
	}
}

// Requirement is Scopes as an AllOf: the Perms.Manage of every books kind.
func Requirement() rbac.Requirement { return rbac.AllOf(Scopes()...) }
