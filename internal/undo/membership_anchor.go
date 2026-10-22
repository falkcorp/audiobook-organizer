// file: internal/undo/membership_anchor.go
// version: 2.0.0
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
// another retire removed, so a revert resolves them through the retires'
// records (MembershipGraph) until it reaches a book that is in the list now,
// and puts the book back right after it.
//
// Why that is exact. Every record is a pair of adjacencies that held in the
// list at the moment of that retire: "Prev stood right before the book, the
// book right before Next". Books never change their relative order (the
// retires only remove books, or swap one for the survivor in its slot, and
// every revert inserts a book where the records say), so every recorded
// adjacency is also an order fact for all time, whatever happened to either
// book since: retired, restored, retired again.
//
// Take the books of the list now, in their order, and the book z that is the
// nearest listed one before the retired book. Everything between z and the
// retired book is absent (retired and not yet restored). Take any two books u
// and v with nothing between them in that order, and the first moment either
// was retired: the other one was listed then (it had not been retired yet) and
// right beside it, so that retire recorded the pair. A restore never undoes a
// record: the records are kept for every retire, reverted or not (a book
// retired, restored and retired again has all its records), so the pair is
// always among them. Hence there is a chain of recorded adjacencies from the
// retired book back through the absent ones to z, and every other listed
// book that walking backwards through absent books reaches is before z. The
// revert puts the book right after the last listed book it reached. When the
// walk reaches none (the book was the first), it puts the book right before
// the first listed book walking forwards reaches.
//
// A survivor that stands in a retired book's slot, while that book is not
// restored, is that book for this purpose (the records that name the survivor
// are mapped to it, see markerHistory.identityAt); once the book is restored
// the survivor is a book of its own (it sits right after the restored one), so
// the records that name it then keep naming it. A neighbour that is not listed and that no
// retire into this survivor explains (another survivor absorbed it, or the
// owner deleted it) is a dead end, never stepped through: the journal of such
// a book places it in the slot of its own neighbours, where another book may
// now stand.

// Sentinel anchors: the book was first (head) or last (tail) in the list.
const (
	anchorHead = "\x00head"
	anchorTail = "\x00tail"
)

// carryMarker is one survivor-side marker row of a retire's carry into the
// list, as the history keeps it.
type carryMarker struct {
	id        string
	op        string
	retired   string
	rec       MembershipRecord
	recOK     bool
	took      bool  // the survivor took the retired book's slot (held: it was listed)
	created   int64 // CreatedAt, ns
	reverted  int64 // RevertedAt, ns; 0 while the marker stands
	borrowing bool  // the survivor is in the list on this marker's account
}

func (m *carryMarker) standing() bool { return m.reverted == 0 }

// standingAt: the marker was written and not yet reverted at time t (ns).
func (m *carryMarker) standingAt(t int64) bool { return m.reverted == 0 || m.reverted > t }

func (m *carryMarker) before(o *carryMarker) bool {
	return m.created < o.created || (m.created == o.created && m.id < o.id)
}

// markerHistory is the survivor-side markers of one list in the order they
// were written, with which of them borrow the survivor's place in the list:
// a "took" marker always (the survivor entered the list for it), a "held"
// marker when a borrowing marker was standing at the moment it was written
// (the survivor was in the list on that marker's account, not as an original
// member).
type markerHistory struct {
	survivor string
	ms       []*carryMarker
}

func newMarkerHistory(survivor, kind, listID string, rows []*database.OperationChange) *markerHistory {
	took := UserContentField(TookKind(kind), listID)
	held := UserContentField(HeldKind(kind), listID)
	h := &markerHistory{survivor: survivor}
	for _, r := range rows {
		if r == nil || r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.BookID != survivor {
			continue
		}
		if r.FieldName != took && r.FieldName != held {
			continue
		}
		m := &carryMarker{id: r.ID, op: r.OperationID, retired: r.NewValue, took: r.FieldName == took, created: r.CreatedAt.UnixNano()}
		if rec, err := DecodeMembership(r.OldValue); err == nil {
			m.rec, m.recOK = rec, true
		}
		if r.RevertedAt != nil {
			m.reverted = r.RevertedAt.UnixNano()
		}
		h.ms = append(h.ms, m)
	}
	slices.SortFunc(h.ms, func(a, b *carryMarker) int {
		switch {
		case a.before(b):
			return -1
		case b.before(a):
			return 1
		}
		return 0
	})
	for i, m := range h.ms {
		if m.took {
			m.borrowing = true
			continue
		}
		for _, e := range h.ms[:i] {
			if e.borrowing && e.standingAt(m.created) {
				m.borrowing = true
				break
			}
		}
	}
	return h
}

// markerOf is the latest marker of the retire of book retired in operation op.
func (h *markerHistory) markerOf(op, retired string) *carryMarker {
	var best *carryMarker
	for _, m := range h.ms {
		if m.op == op && m.retired == retired {
			best = m
		}
	}
	return best
}

// ghostID names the survivor as a book of its own in the list during the
// epoch a "took" marker started: the survivor entered the list for that retire,
// stays for as long as any retire of the epoch stands, and sits in the gap
// where the retired book was. An epoch is a node of its own because the survivor
// can come and go: its position in one epoch says nothing about the next.
func ghostID(root *carryMarker) string { return "\x00ghost:" + root.id }

// survivorNodeAt names the survivor as an anchor of a record written at time
// created (ns), ordered after the markers before it: "" when the survivor was
// an original member of the list (it is itself); otherwise it is in the list
// on a retire's account, and it is the book of the epoch's "took" marker while
// that marker stands (the survivor took that book's slot and the book has not
// been restored), else the epoch's ghost (the book was restored and the survivor
// still stands for other retires, just after it). The marker itself is not
// before itself.
func (h *markerHistory) survivorNodeAt(created int64, id string) string {
	probe := &carryMarker{created: created, id: id}
	var root *carryMarker
	alive := false
	for _, m := range h.ms {
		if !m.before(probe) {
			break
		}
		if m.took {
			root = m
		}
		if m.borrowing && m.standingAt(created) {
			alive = true
		}
	}
	switch {
	case !alive || root == nil:
		return ""
	case root.standingAt(created):
		return root.retired
	}
	return ghostID(root)
}

// epochNow is the epoch the list is in now: the latest "took" marker, and
// whether a borrowing marker of it still stands (the marker of the row being
// reverted counts: it is stamped only once its row is restored).
func (h *markerHistory) epochNow() (root *carryMarker, alive bool) {
	for _, m := range h.ms {
		if m.took {
			root = m
		}
		if m.borrowing && m.standing() {
			alive = true
		}
	}
	return root, alive && root != nil
}

// anchored is one carry's record, as the graph keeps it.
type anchored struct {
	id      string
	retired string
	prev    string
	next    string
	created int64 // CreatedAt, ns
}

// MembershipGraph is the recorded adjacencies of the carries of retired books
// into one survivor's slot of one list (a collection or playlist). It is
// built from the survivor's journal (every carry journals a marker there) and
// the row being reverted, and keeps EVERY record, reverted or not, of every
// retire: a book retired, restored and retired again contributes all of them.
// A neighbour that is not listed and that no carry into this survivor explains
// is deliberately a dead end (see the top of this file); the walk then uses
// the neighbours it can trust, and falls back to the index (reported) when
// there are none.
type MembershipGraph struct {
	survivor string
	kind     string
	listID   string
	rows     []*database.OperationChange
	hist     *markerHistory
	recs     []anchored
}

// NewMembershipGraph is an empty graph for kind (UserContentCollection or
// UserContentPlaylist) list listID and the survivor.
func NewMembershipGraph(survivor, kind, listID string) *MembershipGraph {
	return &MembershipGraph{survivor: survivor, kind: kind, listID: listID}
}

// AddRows takes the carry rows of this list among rows (a book's journal): the
// survivor's markers, and rows on retired books of a retire into this
// survivor (used for a carry whose marker is missing). Voided rows, rows of
// another list or survivor and rows with no anchors (journaled before they
// were recorded) are left out of the adjacencies; unparsable ones too.
func (g *MembershipGraph) AddRows(rows []*database.OperationChange) {
	g.rows = append(g.rows, rows...)
	g.hist, g.recs = nil, nil
}

func (g *MembershipGraph) build() {
	if g.hist != nil {
		return
	}
	g.hist = newMarkerHistory(g.survivor, g.kind, g.listID, g.rows)
	seen := map[string]bool{}
	key := func(op, retired string) string { return op + "\x00" + retired }
	for _, m := range g.hist.ms {
		if !m.recOK || !m.rec.Anchored || seen[key(m.op, m.retired)] {
			continue
		}
		seen[key(m.op, m.retired)] = true
		g.recs = append(g.recs, anchored{id: m.id, retired: m.retired, prev: m.rec.Prev, next: m.rec.Next, created: m.created})
	}
	main := UserContentField(g.kind, g.listID)
	for _, r := range g.rows {
		if r == nil || r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.FieldName != main ||
			r.NewValue != g.survivor || seen[key(r.OperationID, r.BookID)] {
			continue
		}
		rec, err := DecodeMembership(r.OldValue)
		if err != nil || !rec.Anchored {
			continue
		}
		seen[key(r.OperationID, r.BookID)] = true
		g.recs = append(g.recs, anchored{id: r.ID, retired: r.BookID, prev: rec.Prev, next: rec.Next, created: r.CreatedAt.UnixNano()})
	}
}

// node maps an anchor recorded at a's time to its graph node: "" is the head
// or the tail (empty), the survivor is the book it stood in for, any other id
// is itself.
func (g *MembershipGraph) node(id, empty string, a anchored) string {
	switch {
	case id == "":
		return empty
	case id == g.survivor:
		if x := g.hist.survivorNodeAt(a.created, a.id); x != "" {
			return x
		}
	}
	return id
}

// edges returns, per node, the nodes recorded right before it (preds) and
// right after it (succs), from every record.
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
		add(g.node(a.prev, anchorHead, a), a.retired)
		add(a.retired, g.node(a.next, anchorTail, a))
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
	g.build()
	preds, succs := g.edges()
	root, alive := g.hist.epochNow()
	var standIn, ghost string // the nodes the survivor is in the list as, now
	if alive {
		ghost = ghostID(root)
		if root.standing() {
			standIn = root.retired
		}
	}
	// pos is the index of node n in cur, -2 when n is not listed. The survivor
	// is listed as the book it stands in for, or as its epoch's ghost.
	pos := func(n string) int {
		switch n {
		case anchorHead:
			return -1
		case anchorTail:
			return len(cur)
		}
		if n == g.survivor {
			if alive {
				return -2 // the survivor in the list now is the epoch's, not an original member
			}
			if i := slices.Index(cur, n); i >= 0 {
				return i
			}
			return -2
		}
		if i := slices.Index(cur, n); i >= 0 {
			return i
		}
		if n == standIn || n == ghost {
			if i := slices.Index(cur, g.survivor); i >= 0 {
				return i
			}
		}
		return -2
	}
	// walk collects the listed nodes reached from retired through absent ones.
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
