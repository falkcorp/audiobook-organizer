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

// EncodeDefSnapshot is d's snapshot value, for the kernel's upsert. View is
// always recorded, so an upsert over a version-1 row rewrites it. The codec is
// taskstore's (DefSnapshot, EncodeView, DecodeView).
func EncodeDefSnapshot(d tasks.Definition) ([]byte, error) {
	c := tasks.CommonOf(d)
	v := taskstore.EncodeView(c.Perms.View)
	return json.Marshal(taskstore.DefSnapshot{V: taskstore.DefSnapshotVersion, Shape: d.Shape().String(), Title: c.Title, Help: c.Help, View: &v})
}

// reasonNoViewSnapshot is the log reason for a kind whose runs are hidden
// because its snapshot is missing, unreadable, version 1, or has no usable View.
const reasonNoViewSnapshot = "no view snapshot"

// storedView is the View rule kind's snapshot records. A store that cannot be
// read (the error is returned beside it), and an executor built with no
// Options.Keyspace (no snapshot is reachable), answer taskstore.ViewUnknown.
func (e *executor) storedView(ctx context.Context, kind tasks.Kind) (rbac.Requirement, taskstore.ViewState, error) {
	if e.idem == nil {
		return rbac.Requirement{}, taskstore.ViewUnknown, nil
	}
	raw, found, err := e.idem.Get(ctx, taskstore.DefKey(string(kind)))
	if err != nil {
		e.ins.undecodable("definition_snapshot")
		return rbac.Requirement{}, taskstore.ViewUnknown, err
	}
	if !found {
		return rbac.Requirement{}, taskstore.ViewUnknown, nil
	}
	r, st := taskstore.DecodeView(raw)
	if st == taskstore.ViewUnknown {
		e.ins.undecodable("definition_snapshot")
	}
	return r, st, nil
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
// nothing usable (taskstore.DefSnapshotVersion).
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
	req, st, err := v.e.storedView(v.ctx, kind)
	switch st {
	case taskstore.ViewDefault:
		return v.who.Has(scopeTasksView)
	case taskstore.ViewRequires:
		if req.IsZero() {
			return false // a recorded requirement no checker satisfies
		}
		return v.who.Satisfies(req)
	default:
		// The reason an admin sees an empty list: once per kind per read (the
		// memo calls decide once per kind), never a run id or a param.
		args := []any{"kind", string(kind), "reason", reasonNoViewSnapshot}
		if err != nil {
			args = append(args, "err", err)
		}
		v.e.log.Info("task runs of a kind are hidden from every caller", args...)
		return false
	}
}
