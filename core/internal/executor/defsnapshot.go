// file: core/internal/executor/defsnapshot.go
// version: 1.0.0
// guid: 7c3d9e52-1a84-4f06-b2d7-5e9a8c4f1b30
// last-edited: 2026-11-28

package executor

import (
	"context"
	"encoding/json"

	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// defSnapshotVersion is the version of a definition snapshot's value
// (def/<kind>, 5T.3.11). Version 2 added View.
//
// The rule for a run of a kind with no registered definition (a retired
// fixer, a kind an M4 import carried over before its module registers it, a
// disabled module's kind) is: the run is as viewable as its kind's last
// registered definition said, and a kind whose snapshot cannot say is not
// viewable by anyone but a kernel-internal caller. The three outcomes of
// storedView are kept apart on purpose.
//
//   - viewDefault: the snapshot has a View record saying "no restriction", so
//     the kind took Perms.View's default (core:tasks.view). This is an explicit
//     record, never an absent field.
//   - viewRequires: the snapshot recorded the View requirement.
//   - viewUnknown: there is no snapshot, it does not decode, it predates View
//     (version < 2: its writer never recorded one, so "unrestricted" cannot be
//     told from "restricted"), it names a View kind this build does not know, or
//     the store could not be read. Fail closed.
//
// Rows written before this change are version 1 and so viewUnknown if their
// kind is not registered now. That costs nothing for a registered kind: its
// live definition decides, and the next boot's upsert rewrites its snapshot at
// version 2 (kernel.upsertDefinitions compares bytes). The kinds at prod are
// core's own and all registered.
const defSnapshotVersion = 2

// viewState is how much a stored snapshot says about who may view a kind's runs.
type viewState uint8

const (
	// viewUnknown: no usable record. Closed.
	viewUnknown viewState = iota
	// viewDefault: recorded as unrestricted (Perms.View omitted).
	viewDefault
	// viewRequires: recorded with an explicit requirement.
	viewRequires
)

// The View record's kinds.
const (
	viewRecDefault       = "default"
	viewRecAllOf         = "all_of"
	viewRecAnyOf         = "any_of"
	viewRecPublic        = "public"
	viewRecAuthenticated = "authenticated"
	// viewRecUnsatisfiable is a requirement the snapshot cannot carry (a
	// legacy atom or a delegated requirement): no checker satisfies one, so the
	// live rule is "nobody" and so is the stored one.
	viewRecUnsatisfiable = "unsatisfiable"
)

// defView is the View field of a snapshot.
type defView struct {
	Kind  string   `json:"kind"`
	Perms []string `json:"perms,omitempty"`
}

// defSnapshot is a definition's stored snapshot. The kind is the key only,
// never in the value, because Importer.RenameKind moves the snapshot as bytes
// (taskstore.DefKey). View is a pointer so that a version-1 value, which has
// none, is told from a version-2 value that records "default".
type defSnapshot struct {
	V     int      `json:"v"`
	Shape string   `json:"shape"`
	Title string   `json:"title"`
	Help  string   `json:"help,omitempty"`
	View  *defView `json:"view,omitempty"`
}

// EncodeDefSnapshot is d's snapshot value, for the kernel's upsert. View is
// always recorded, so an upsert over a version-1 row rewrites it.
func EncodeDefSnapshot(d tasks.Definition) ([]byte, error) {
	c := tasks.CommonOf(d)
	v := encodeView(c.Perms.View)
	return json.Marshal(defSnapshot{V: defSnapshotVersion, Shape: d.Shape().String(), Title: c.Title, Help: c.Help, View: &v})
}

// encodeView records r from its Kind and Permissions, never from String().
func encodeView(r rbac.Requirement) defView {
	if r.IsZero() {
		return defView{Kind: viewRecDefault}
	}
	perms := func() []string {
		ps := r.Permissions()
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = string(p)
		}
		return out
	}
	switch r.Kind() {
	case rbac.KindAllOf:
		return defView{Kind: viewRecAllOf, Perms: perms()}
	case rbac.KindAnyOf:
		return defView{Kind: viewRecAnyOf, Perms: perms()}
	case rbac.KindPublic:
		return defView{Kind: viewRecPublic}
	case rbac.KindAuthenticated:
		return defView{Kind: viewRecAuthenticated}
	default:
		return defView{Kind: viewRecUnsatisfiable}
	}
}

// decodeView is a snapshot value's View, or viewUnknown. It never returns a
// requirement it did not rebuild whole: a malformed one (rbac.AllOf of no or a
// bad scope is the zero Requirement) is viewUnknown, not "anyone".
func decodeView(raw []byte) (rbac.Requirement, viewState) {
	var s defSnapshot
	if err := json.Unmarshal(raw, &s); err != nil || s.V < defSnapshotVersion || s.View == nil {
		return rbac.Requirement{}, viewUnknown
	}
	perms := make([]rbac.Permission, len(s.View.Perms))
	for i, p := range s.View.Perms {
		perms[i] = rbac.Permission(p)
	}
	var r rbac.Requirement
	switch s.View.Kind {
	case viewRecDefault:
		return rbac.Requirement{}, viewDefault
	case viewRecAllOf:
		r = rbac.AllOf(perms...)
	case viewRecAnyOf:
		r = rbac.AnyOf(perms...)
	case viewRecPublic:
		r = rbac.Public
	case viewRecAuthenticated:
		r = rbac.AnyAuthenticated
	case viewRecUnsatisfiable:
		return rbac.Requirement{}, viewRequires // the zero requirement, which Satisfies refuses (see mayView)
	default:
		return rbac.Requirement{}, viewUnknown
	}
	if r.IsZero() {
		return rbac.Requirement{}, viewUnknown
	}
	return r, viewRequires
}

// storedView is the View rule kind's snapshot records. A store that cannot be
// read, and an executor built with no Options.Keyspace (no snapshot is
// reachable), answer viewUnknown.
func (e *executor) storedView(ctx context.Context, kind tasks.Kind) (rbac.Requirement, viewState) {
	if e.idem == nil {
		return rbac.Requirement{}, viewUnknown
	}
	raw, found, err := e.idem.Get(ctx, taskstore.DefKey(string(kind)))
	if err != nil {
		e.log.Warn("task definition snapshot unreadable; the runs of its kind are hidden", "kind", string(kind), "err", err)
		e.ins.undecodable("definition_snapshot")
		return rbac.Requirement{}, viewUnknown
	}
	if !found {
		return rbac.Requirement{}, viewUnknown
	}
	r, st := decodeView(raw)
	if st == viewUnknown {
		e.log.Warn("task definition snapshot has no usable View record; the runs of its kind are hidden", "kind", string(kind))
		e.ins.undecodable("definition_snapshot")
	}
	return r, st
}

// viewer answers mayView for one caller over one read (a Get, a Runs
// iteration, a counts pass). It remembers each kind's answer, so a list of any
// length reads a kind's snapshot at most once and the permission check runs
// once per kind. It is not safe for concurrent use and is never shared
// between reads: a snapshot is not cached across calls, so a stored answer is
// never staler than the call that made it.
type viewer struct {
	e    *executor
	ctx  context.Context
	who  rbac.Checker
	memo map[tasks.Kind]bool
}

func (e *executor) viewer(ctx context.Context, who rbac.Checker) *viewer {
	return &viewer{e: e, ctx: ctx, who: who, memo: map[tasks.Kind]bool{}}
}

// may applies the View rule to kind: a nil checker (kernel-internal) and a
// module's own client read their own module's kinds first; then the live
// definition's View (omitted: core:tasks.view); a kind with no live definition
// takes the rule its snapshot recorded, and closed when the snapshot says
// nothing usable (defSnapshotVersion).
func (v *viewer) may(kind tasks.Kind) bool {
	if v.who == nil {
		return true
	}
	if mc, ok := v.who.(moduleChecker); ok && kind.Module() == mc.module {
		return true
	}
	if ok, seen := v.memo[kind]; seen {
		return ok
	}
	ok := v.decide(kind)
	v.memo[kind] = ok
	return ok
}

func (v *viewer) decide(kind tasks.Kind) bool {
	if d := v.e.defs[kind]; d != nil {
		if !d.c.Perms.View.IsZero() {
			return v.who.Satisfies(d.c.Perms.View)
		}
		return v.who.Has(scopeTasksView)
	}
	req, st := v.e.storedView(v.ctx, kind)
	switch st {
	case viewDefault:
		return v.who.Has(scopeTasksView)
	case viewRequires:
		if req.IsZero() {
			return false // a recorded requirement no checker satisfies
		}
		return v.who.Satisfies(req)
	default:
		return false
	}
}
