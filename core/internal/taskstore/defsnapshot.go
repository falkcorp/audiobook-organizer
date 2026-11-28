// file: core/internal/taskstore/defsnapshot.go
// version: 1.0.0
// guid: 5f1a9c37-8e24-4d60-b3a7-2c9d6e0f4a81
// last-edited: 2026-11-28

package taskstore

import (
	"encoding/json"

	"github.com/falkcorp/mediacore/rbac"
)

// The definition snapshot's codec (def/<kind>, 5T.3.11). It lives here, beside
// DefKey, because three packages write or read the value: the kernel's upsert
// of a registered definition (through the executor's EncodeDefSnapshot), the
// executor's read of a kind with no live definition, and the native importer's
// StampView for the kinds an app's cutover carries over.

// DefSnapshotVersion is the version of a definition snapshot's value. Version 2
// added View.
//
// The rule for a run of a kind with no registered definition (a retired
// fixer, a kind an M4 import carried over before its module registers it, a
// disabled module's kind) is: the run is as viewable as its kind's last
// registered definition said, and a kind whose snapshot cannot say is not
// viewable by anyone but a kernel-internal caller. The three outcomes of
// DecodeView are kept apart on purpose.
//
//   - ViewDefault: the snapshot has a View record saying "no restriction", so
//     the kind took Perms.View's default (core:tasks.view). This is an explicit
//     record, never an absent field.
//   - ViewRequires: the snapshot recorded the View requirement.
//   - ViewUnknown: there is no snapshot, it does not decode, it predates View
//     (version < 2: its writer never recorded one, so "unrestricted" cannot be
//     told from "restricted"), it names a View kind this build does not know, or
//     the store could not be read. Fail closed.
//
// Rows written before version 2 are ViewUnknown if their kind is not
// registered now. That costs nothing for a registered kind: its live
// definition decides, and the next boot's upsert rewrites its snapshot at
// version 2 (kernel.upsertDefinitions compares bytes). The kinds at prod are
// core's own and all registered.
const DefSnapshotVersion = 2

// ViewState is how much a stored snapshot says about who may view a kind's runs.
type ViewState uint8

const (
	// ViewUnknown: no usable record. Closed.
	ViewUnknown ViewState = iota
	// ViewDefault: recorded as unrestricted (Perms.View omitted).
	ViewDefault
	// ViewRequires: recorded with an explicit requirement.
	ViewRequires
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

// DefView is the View field of a snapshot.
type DefView struct {
	Kind  string   `json:"kind"`
	Perms []string `json:"perms,omitempty"`
}

// DefSnapshot is a definition's stored snapshot. The kind is the key only,
// never in the value, because Importer.RenameKind moves the snapshot as bytes
// (DefKey). View is a pointer so that a version-1 value, which has none, is
// told from a version-2 value that records "default".
type DefSnapshot struct {
	V     int      `json:"v"`
	Shape string   `json:"shape"`
	Title string   `json:"title"`
	Help  string   `json:"help,omitempty"`
	View  *DefView `json:"view,omitempty"`
}

// EncodeView records r from its Kind and Permissions, never from String().
func EncodeView(r rbac.Requirement) DefView {
	if r.IsZero() {
		return DefView{Kind: viewRecDefault}
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
		return DefView{Kind: viewRecAllOf, Perms: perms()}
	case rbac.KindAnyOf:
		return DefView{Kind: viewRecAnyOf, Perms: perms()}
	case rbac.KindPublic:
		return DefView{Kind: viewRecPublic}
	case rbac.KindAuthenticated:
		return DefView{Kind: viewRecAuthenticated}
	default:
		return DefView{Kind: viewRecUnsatisfiable}
	}
}

// DecodeView is a snapshot value's View, or ViewUnknown. It never returns a
// requirement it did not rebuild whole: a malformed one (rbac.AllOf of no or a
// bad scope is the zero Requirement) is ViewUnknown, not "anyone".
func DecodeView(raw []byte) (rbac.Requirement, ViewState) {
	var s DefSnapshot
	if err := json.Unmarshal(raw, &s); err != nil || s.V < DefSnapshotVersion || s.View == nil {
		return rbac.Requirement{}, ViewUnknown
	}
	perms := make([]rbac.Permission, len(s.View.Perms))
	for i, p := range s.View.Perms {
		perms[i] = rbac.Permission(p)
	}
	var r rbac.Requirement
	switch s.View.Kind {
	case viewRecDefault:
		return rbac.Requirement{}, ViewDefault
	case viewRecAllOf:
		r = rbac.AllOf(perms...)
	case viewRecAnyOf:
		r = rbac.AnyOf(perms...)
	case viewRecPublic:
		r = rbac.Public
	case viewRecAuthenticated:
		r = rbac.AnyAuthenticated
	case viewRecUnsatisfiable:
		return rbac.Requirement{}, ViewRequires // the zero requirement, which Satisfies refuses
	default:
		return rbac.Requirement{}, ViewUnknown
	}
	if r.IsZero() {
		return rbac.Requirement{}, ViewUnknown
	}
	return r, ViewRequires
}
