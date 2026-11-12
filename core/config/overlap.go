// file: core/config/overlap.go
// version: 1.0.0
// guid: 3f6a2b9e-7c41-4d58-a0b3-91c5e8d2f7a4
// last-edited: 2026-11-12

package config

import (
	"fmt"
	"slices"
	"strings"
)

// pathSet indexes values by Path so that "which entries equal p, extend p, or
// are a prefix of p, at a segment boundary" is a few map lookups instead of a
// scan. exact holds each entry under its own path; below holds it under every
// proper ancestor of its path.
type pathSet struct {
	exact map[Path][]*section
	below map[Path][]*section
}

func newPathSet() pathSet {
	return pathSet{exact: map[Path][]*section{}, below: map[Path][]*section{}}
}

// add records owner under p.
func (ps pathSet) add(p Path, owner *section) {
	ps.exact[p] = append(ps.exact[p], owner)
	for a := p.Parent(); a != ""; a = a.Parent() {
		ps.below[a] = append(ps.below[a], owner)
	}
}

// atOrAbove calls f for every owner recorded under a path that equals q or is a
// proper ancestor of q (under(q, that path)).
func (ps pathSet) atOrAbove(q Path, f func(*section)) {
	for a := q; a != ""; a = a.Parent() {
		for _, o := range ps.exact[a] {
			f(o)
		}
	}
}

// atOrBelow calls f for every owner recorded under a path that equals q or
// extends it (under(that path, q)).
func (ps pathSet) atOrBelow(q Path, f func(*section)) {
	for _, o := range ps.exact[q] {
		f(o)
	}
	for _, o := range ps.below[q] {
		f(o)
	}
}

// overlapIndex is the registered sections' claimed paths, section paths and
// entry-list parents. A Register used to compare the new section with every
// registered one, key against key (O(sections^2 x keys) over a boot's
// registrations); the index names the registered sections the new one could
// collide with, in a lookup per claim, and conflict decides.
type overlapIndex struct {
	claims  pathSet // each registered section, under each path it claims
	paths   pathSet // each registered section, under its own path
	parents pathSet // each registered section, under each entry list's parent
}

func newOverlapIndex() *overlapIndex {
	return &overlapIndex{claims: newPathSet(), paths: newPathSet(), parents: newPathSet()}
}

// add records a section that Register accepted.
func (ix *overlapIndex) add(sec *section) {
	for _, c := range sec.claims() {
		ix.claims.add(c, sec)
	}
	ix.paths.add(sec.path, sec)
	for _, lk := range sec.entryLists {
		ix.parents.add(lk.entryParent(), sec)
	}
}

// candidates returns the registered sections that conflict could refuse sec
// for, in path order (the order a scan of the sorted section list visits them).
// It must return every one: each case of conflict has a lookup here.
func (ix *overlapIndex) candidates(sec *section) []*section {
	seen := map[*section]bool{}
	add := func(o *section) { seen[o] = true }
	// Same path.
	ix.paths.atOrBelow(sec.path, func(o *section) {
		if o.path == sec.path {
			add(o)
		}
	})
	for _, a := range sec.claims() {
		// A claim equal to, under or over a registered claim.
		ix.claims.atOrAbove(a, add)
		ix.claims.atOrBelow(a, add)
		// A registered section lying at or under one of sec's claims.
		ix.paths.atOrBelow(a, add)
	}
	// sec lying at or under a registered claim.
	ix.claims.atOrAbove(sec.path, add)
	// Entry lists: a registered section under sec's list parent, and sec
	// under a registered list parent.
	for _, lk := range sec.entryLists {
		ix.paths.atOrBelow(lk.entryParent(), add)
	}
	ix.parents.atOrAbove(sec.path, add)
	out := make([]*section, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b *section) int { return strings.Compare(string(a.path), string(b.path)) })
	return out
}

// check refuses sec when it collides with a registered section. The first
// candidate in path order that conflict refuses names the error, as the scan
// of every registered section did.
func (ix *overlapIndex) check(sec *section) error {
	for _, o := range ix.candidates(sec) {
		if err := conflict(sec, o); err != nil {
			return err
		}
	}
	return nil
}

// conflict is the whole of 05-config §1.3.1's overlap rule between a new
// section and one registered one: nil when they may live in one store.
func conflict(sec, o *section) error {
	if o.path == sec.path {
		return fmt.Errorf("%w: section %s is registered twice", ErrDuplicate, sec.path)
	}
	for _, a := range sec.claims() {
		for _, b := range o.claims() {
			if overlaps(a, b) {
				return fmt.Errorf("%w: section %s's key %s overlaps section %s's key %s", ErrDuplicate, sec.path, a, o.path, b)
			}
		}
	}
	for _, b := range o.claims() {
		if under(sec.path, b) {
			return fmt.Errorf("%w: section %s lies under section %s's key %s", ErrDuplicate, sec.path, o.path, b)
		}
	}
	for _, a := range sec.claims() {
		if under(o.path, a) {
			return fmt.Errorf("%w: section %s's key %s holds section %s", ErrDuplicate, sec.path, a, o.path)
		}
	}
	// An entry list's parent is an ID namespace: "<parent>.<id>.<ref>"
	// is a secret key for any ID, so no other section may lie there.
	for _, pair := range [][2]*section{{sec, o}, {o, sec}} {
		for _, lk := range pair[0].entryLists {
			if under(pair[1].path, lk.entryParent()) {
				return fmt.Errorf("%w: section %s lies under %s, where section %s's list %s names its entries' secrets", ErrDuplicate, pair[1].path, lk.entryParent(), pair[0].path, lk.path(""))
			}
		}
	}
	return nil
}
