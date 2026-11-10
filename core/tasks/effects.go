// file: core/tasks/effects.go
// version: 1.0.1
// guid: 850e3379-fae5-47cf-87e7-5de835a26db5
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/entity"
)

// Resources are entity.Ref values (03 §3.3, L0; COR-17). A Ref with ID == ""
// means the whole kind. tasks adds helpers and a readability alias only; it
// defines no reference type of its own.
type Resource = entity.Ref // alias, not a new type (SIMP-16 option)

// Res is the whole-kind reference {module, kind, ""}.
func Res(module, kind string) entity.Ref { return entity.Ref{Module: module, Kind: kind} }

// ResID is the reference to one entity {module, kind, id}.
func ResID(module, kind, id string) entity.Ref {
	return entity.Ref{Module: module, Kind: kind, ID: id}
}

// Overlaps is the dispatch overlap rule: same module and kind, and either ID
// empty (the whole kind) or both IDs equal.
func Overlaps(a, b entity.Ref) bool {
	return a.Module == b.Module && a.Kind == b.Kind && (a.ID == "" || b.ID == "" || a.ID == b.ID)
}

// FormatRef renders "books/book_files" or "books/book_files#<id>"; ref locks
// are taken in this order.
func FormatRef(r entity.Ref) string {
	if r.ID == "" {
		return r.Module + "/" + r.Kind
	}
	return r.Module + "/" + r.Kind + "#" + r.ID
}

// LockRefs is rc.LockRefs for code that holds only a ctx (COR2-16): when ctx
// descends from an RC it takes that executor's ref-lock table (same sorted
// all-or-wait rule); otherwise it returns ErrNoRunContext. Inside an item it
// skips the refs the item already holds (see ForEach).
func LockRefs(ctx context.Context, refs ...entity.Ref) (release func(), err error) {
	h := hostFrom(ctx)
	if h == nil {
		return nil, ErrNoRunContext
	}
	return lockUnheld(ctx, h, refs)
}

// Effects is what a definition reads and writes. Build it with ReadOnly or
// Writes; the zero value is undeclared and refused at construction.
type Effects struct {
	declared bool
	readOnly bool
	reads    []entity.Ref
	writes   []entity.Ref
	deletes  []entity.Ref
}

// ReadOnly declares a definition that writes nothing; reads are declarative
// (shown in the UI, used by preview and audit), not gated.
func ReadOnly(reads ...entity.Ref) Effects {
	return Effects{declared: true, readOnly: true, reads: appendRefs(nil, reads)}
}

// Writes declares the resources a definition may write; at least one is
// required. A Writer refuses a write outside them (ErrOutsideEffects).
func Writes(ws ...entity.Ref) Effects {
	return Effects{declared: true, writes: appendRefs(nil, ws)}
}

// AlsoReads adds declared reads.
func (e Effects) AlsoReads(rs ...entity.Ref) Effects {
	out := e.clone()
	out.reads = appendRefs(out.reads, rs)
	return out
}

// AlsoDeletes adds resources the definition deletes from (flagged for owner
// review by archtest). A delete is a write for the write-set gate, so each is
// also added to the write set; on a ReadOnly value it is a construction
// problem.
func (e Effects) AlsoDeletes(rs ...entity.Ref) Effects {
	out := e.clone()
	out.deletes = appendRefs(out.deletes, rs)
	out.writes = appendRefs(out.writes, rs)
	return out
}

// Declared reports a value built by ReadOnly or Writes. Accessor coined by F2-01.
func (e Effects) Declared() bool { return e.declared }

// IsReadOnly reports a ReadOnly declaration with no deletes. Accessor coined by F2-01.
func (e Effects) IsReadOnly() bool { return e.declared && e.readOnly && len(e.writes) == 0 }

// ReadSet returns a copy of the declared reads. Accessor coined by F2-01.
func (e Effects) ReadSet() []entity.Ref { return slices.Clone(e.reads) }

// WriteSet returns a copy of the declared writes (deletes included). Accessor coined by F2-01.
func (e Effects) WriteSet() []entity.Ref { return slices.Clone(e.writes) }

// DeleteSet returns a copy of the declared deletes. Accessor coined by F2-01.
func (e Effects) DeleteSet() []entity.Ref { return slices.Clone(e.deletes) }

func (e Effects) clone() Effects {
	e.reads = slices.Clone(e.reads)
	e.writes = slices.Clone(e.writes)
	e.deletes = slices.Clone(e.deletes)
	return e
}

func appendRefs(dst, src []entity.Ref) []entity.Ref {
	for _, r := range src {
		if !slices.Contains(dst, r) {
			dst = append(dst, r)
		}
	}
	return dst
}

// problems reports what a definition's constructor records about e.
func (e Effects) problems() []string {
	var out []string
	switch {
	case !e.declared:
		out = append(out, "Effects not declared (use ReadOnly or Writes)")
	case e.readOnly && len(e.writes) > 0:
		out = append(out, "Effects: AlsoDeletes on a ReadOnly declaration")
	case !e.readOnly && len(e.writes) == 0:
		out = append(out, "Effects: Writes names no resource")
	}
	for _, set := range [][]entity.Ref{e.reads, e.writes} {
		for _, r := range set {
			if r.Module == "" || r.Kind == "" {
				out = append(out, fmt.Sprintf("Effects: resource %s needs a module and a kind", FormatRef(r)))
			}
		}
	}
	return out
}

// SubjectsFunc narrows a run's write set to concrete IDs at enqueue time
// (e.g. one book merge writes books/books#a and #b; a season-pack import writes
// every episode it covers). Returned refs must be within Effects.Writes kinds.
type SubjectsFunc func(params json.RawMessage) ([]entity.Ref, error)

// Key is a namespaced mutual-exclusion key "<module>.<name>". A module may use
// keys in its own namespace and keys in "core." (e.g. "core.storage.exclusive",
// held by compaction and backup).
type Key string

// Module is the text before the first '.'. Accessor coined by F2-01.
func (k Key) Module() string {
	m, _, _ := strings.Cut(string(k), ".")
	return m
}

// valid reports the key form: two or more dot-separated segments of the Kind
// grammar. Which namespaces a module may use is checked at registration.
func (k Key) valid() bool {
	_, err := ParseKind(string(k))
	return err == nil
}
