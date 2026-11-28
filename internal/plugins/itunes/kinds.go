// file: internal/plugins/itunes/kinds.go
// version: 1.0.0
// guid: 25691fdf-6436-4a9a-a129-12a7cd7c0179
// last-edited: 2026-11-19

package itunes

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books Task kind for the iTunes plugin (F5-05, 05-tasks 5T.8.2 group
// T5): the plugin's one converted v2 definition, itunes.position-sync, as a
// tasks.Task kind named "books.itunes.position-sync". It is DARK: nothing
// registers it before the tasks flip (F5-18), which also removes HEAD's v2
// shell. No kinds*.go file imports pkg/plugin/sdk (the T5 gate).
//
// The definition has one shared core and two shells over it: HEAD's Run (the v2
// shell, in position_sync.go) and the native tasks.Task below. The core is the
// stub's refusal and reads and writes nothing, so it takes no env, and
// KindDeps declares nothing the owner must wire: there is no store, writer or
// service to hand it (rule R10 has nothing to route). itunes.import, the
// plugin's other def, is an excluded stub whose real implementation is
// internal/server's, and is not converted here (F5-13).

const (
	// kindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	kindPrefix = "books."

	// signalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false).
	signalMemDB tasks.ReadySignal = "books.memdb"
)

// positionSyncKind is rule R3's kind for itunes.position-sync.
const positionSyncKind = tasks.Kind(kindPrefix + positionSyncDefID)

// settingsManageScopes is the route requirement of HEAD's settings.manage atom
// (internal/books/atoms.go). internal/books imports this package through its
// import graph, so the table cannot be read from here; it is the same copy
// internal/plugins/maintenance and internal/plugins/dedup keep, and drifts the
// same way (the atom-table golden test in internal/books pins the original).
var settingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// kindStartRequirement is rule R6 for itunes.position-sync: HEAD declares no
// Permissions, which the registry defaults to settings.manage.
func kindStartRequirement() rbac.Requirement {
	return rbac.AllOf(settingsManageScopes...)
}

// KindDeps is what the native kinds need from their owner. The iTunes kind needs
// nothing: its core is a refusal (see the file comment). The type exists so the
// F5-18 registration has the same Kinds(deps, sched) shape in every plugin.
type KindDeps struct{}

// Kinds returns every native kind this plugin declares, with Schedule:
// sched(kind) on each (rule R9; a nil sched means no schedule). Nothing calls it
// in production before F5-18.
func (p *Plugin) Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return kindList(p, deps, sched)
}

// kindList is Kinds as a package-level function: tasklint's unregistered-kind
// rule counts package-level list functions only, and not a method.
func kindList(p *Plugin, deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.positionSyncNative(deps, sched),
	}
}

// kindListRoot names kindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shells.
var _ = kindList

// The brief's list-function line, kept so the method stays referenced.
var _ = (*Plugin).Kinds

// positionSyncNative is the native itunes.position-sync kind. HEAD's def has no
// Writes, no Preview and no capability that writes, and its body writes
// nothing, so the Effects are ReadOnly. It fails in every mode, as HEAD's does:
// a stub that returned nil would report green for work that did not happen.
func (p *Plugin) positionSyncNative(_ KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := tasks.Common{
		Title:          positionSyncTitle,
		Help:           positionSyncHelp,
		Perms:          tasks.Permissions{Start: kindStartRequirement(), Manage: taskmanage.Requirement()},
		Ready:          []tasks.ReadySignal{signalMemDB},
		Effects:        tasks.ReadOnly(),
		Resume:         tasks.ResumeFromZero, // HEAD ResumeRequeue (rule R5)
		Priority:       tasks.PriorityNormal,
		ConcurrencyKey: tasks.Key(kindPrefix + "itunes.position-sync"),
		Timeout:        30 * time.Minute,
	}
	if sched != nil {
		c.Schedule = sched(positionSyncKind)
	}
	return tasks.Task(positionSyncKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(_ *tasks.RC, _ json.RawMessage) (struct{}, error) {
			return struct{}{}, positionSyncCore()
		},
	})
}
