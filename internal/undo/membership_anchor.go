// file: internal/undo/membership_anchor.go
// version: 1.0.0
// guid: 7c2e9a40-5b1d-4f86-a3d7-e0b8c4f19a65
// last-edited: 2026-10-21

package undo

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A retire that removes a book from a collection or playlist records where it
// stood (MembershipRecord.Index), and also the ids of the books right before
// and after it (Prev and Next). An index alone is only right when the retires
// are undone newest first: each other retire's removal shifts it. The
// neighbours are not shifted by anything, but they may themselves be books
// another retire removed, so a revert resolves them through the other
// retires' records (MembershipGraph) until it reaches a book that is in the
// list now, and puts the book back right after it.
//
// Why that is exact. Every record is a pair of adjacencies that held in some
// subsequence of the list as it was before any retire (L0): "Prev stood right
// before the book, the book right before Next". Take the books of the list
// now, in L0 order, and the book z that is the nearest one before the retired
// book's L0 slot. Everything between z and that slot is absent (retired and
// not yet restored). For any two books adjacent in L0, whichever of them was
// retired first recorded the other as its neighbour (the other was still
// listed), so there is a chain of recorded adjacencies from the retired book
// back through the absent ones to z. Walking the records backwards from the
// retired book, through absent books only, reaches z, and every other listed
// book reached is before z. The revert puts the book right after the last
// listed book it reached.

// Sentinel anchors: the book was first (head) or last (tail) in the list.
const (
	anchorHead = "\x00head"
	anchorTail = "\x00tail"
)

// anchored is one carry's record, as the graph keeps it.
type anchored struct {
	retired string
	prev    string
	next    string
	created int64 // CreatedAt, ns; orders the records of one book and the took markers
}

// MembershipGraph is the recorded adjacencies of the carries of retired books
// into one survivor's slot of one list (a collection or a playlist). It is
// built from the survivor's journal (every carry journals a marker there) and
// the row being reverted. A neighbour that is not listed and that no carry
// into this survivor explains (a book another survivor absorbed, or one the
// owner deleted) is deliberately a dead end: its own journal would put it at
// the slot of its predecessor, while another book may be standing in that
// slot now, so walking through it can pick a wrong place. The walk then
// uses the neighbours it can trust, and falls back to the index (reported)
// when there are none.
type MembershipGraph struct {
	survivor string
	kind     string
	listID   string
	recs     map[string]anchored // by retired book: the latest record
	tooks    []anchored          // took markers: the survivor entered in retired's slot
}

// NewMembershipGraph is an empty graph for kind (UserContentCollection or
// UserContentPlaylist) list listID and the survivor.
func NewMembershipGraph(survivor, kind, listID string) *MembershipGraph {
	return &MembershipGraph{survivor: survivor, kind: kind, listID: listID, recs: map[string]anchored{}}
}

// AddRows reads the carry rows of this list among rows (a book's journal): the
// survivor-side markers and the retired books' own rows. Voided rows, rows of
// another list, rows with no anchors (journaled before they were recorded) and
// unparsable ones are left out.
func (g *MembershipGraph) AddRows(rows []*database.OperationChange) {
	main := UserContentField(g.kind, g.listID)
	took := UserContentField(TookKind(g.kind), g.listID)
	held := UserContentField(HeldKind(g.kind), g.listID)
	for _, r := range rows {
		if r == nil || r.ChangeType != ChangeTypeUserContentFollow || r.Voided {
			continue
		}
		var retired string
		switch r.FieldName {
		case took, held:
			if r.BookID != g.survivor {
				continue // another survivor's marker: its stand is not ours
			}
			retired = r.NewValue
		case main:
			retired = r.BookID
		default:
			continue
		}
		rec, err := DecodeMembership(r.OldValue)
		if err != nil || !rec.Anchored || retired == "" {
			continue
		}
		a := anchored{retired: retired, prev: rec.Prev, next: rec.Next, created: r.CreatedAt.UnixNano()}
		if old, ok := g.recs[retired]; !ok || a.created >= old.created {
			g.recs[retired] = a
		}
		if r.FieldName == took {
			g.tooks = append(g.tooks, a)
		}
	}
}

// canon maps the survivor, as an anchor recorded at time at, to the book whose
// slot it was standing in (the latest took marker before at): the survivor and
// that book are one node of the list's order. Any other id is itself.
func (g *MembershipGraph) canon(id string, at int64) string {
	if id != g.survivor {
		return id
	}
	best := -1
	for i, t := range g.tooks {
		if t.created <= at && (best < 0 || t.created > g.tooks[best].created) {
			best = i
		}
	}
	if best < 0 {
		return id
	}
	return g.tooks[best].retired
}

// standIn is the book the survivor currently stands in for: the retired book
// of the latest took marker, "" when there is none.
func (g *MembershipGraph) standIn() string {
	best := -1
	for i, t := range g.tooks {
		if best < 0 || t.created > g.tooks[best].created {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return g.tooks[best].retired
}

// node maps a recorded anchor to its graph node.
func (g *MembershipGraph) node(id string, empty string, at int64) string {
	if id == "" {
		return empty
	}
	return g.canon(id, at)
}

// edges returns, per node, the nodes recorded right before it (preds) and
// right after it (succs).
func (g *MembershipGraph) edges() (preds, succs map[string][]string) {
	preds, succs = map[string][]string{}, map[string][]string{}
	add := func(from, to string) {
		if !slices.Contains(succs[from], to) {
			succs[from] = append(succs[from], to)
		}
		if !slices.Contains(preds[to], from) {
			preds[to] = append(preds[to], from)
		}
	}
	for _, a := range g.recs {
		r := g.canon(a.retired, a.created)
		add(g.node(a.prev, anchorHead, a.created), r)
		add(r, g.node(a.next, anchorTail, a.created))
	}
	return preds, succs
}

// slot finds where retired goes back into cur by walking the records, as
// described at the top of the file: right after the last listed book among
// those reached walking backwards (the head counts as listed), else right
// before the first listed book reached walking forwards. ok is false when
// neither reaches a listed book, so the caller falls back to the index.
func (g *MembershipGraph) slot(cur []string, retired string) (at int, ok bool) {
	if g == nil {
		return 0, false
	}
	preds, succs := g.edges()
	stand := g.standIn()
	// pos is the index of node n in cur, -2 when n is not listed. The survivor
	// stands in the slot of the book it replaced, so that book's node is
	// listed wherever the survivor is.
	pos := func(n string) int {
		switch n {
		case anchorHead:
			return -1
		case anchorTail:
			return len(cur)
		}
		i := slices.Index(cur, n)
		if n == stand {
			i = max(i, slices.Index(cur, g.survivor))
		}
		if i < 0 {
			return -2
		}
		return i
	}
	// walk collects the listed nodes reached from start through absent ones.
	walk := func(next map[string][]string) []int {
		var found []int
		seen := map[string]bool{retired: true}
		queue := slices.Clone(next[retired])
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if seen[n] {
				continue
			}
			seen[n] = true
			if p := pos(n); p > -2 {
				found = append(found, p)
				continue
			}
			queue = append(queue, next[n]...)
		}
		return found
	}
	if found := walk(preds); len(found) > 0 {
		return min(slices.Max(found)+1, len(cur)), true
	}
	if found := walk(succs); len(found) > 0 {
		return max(slices.Min(found), 0), true
	}
	return 0, false
}
