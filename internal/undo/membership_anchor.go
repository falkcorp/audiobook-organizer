// file: internal/undo/membership_anchor.go
// version: 3.1.0
// guid: 7c2e9a40-5b1d-4f86-a3d7-e0b8c4f19a65
// last-edited: 2026-10-22

package undo

import (
	"fmt"
	"slices"
	"strings"

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
// are mapped to it, see markerHistory.survivorNodeAt); once the book is restored
// the survivor is a book of its own (it sits right after the restored one), so
// the records that name it then keep naming it.
//
// More than one survivor in a list. The records of a retire live in two
// journals, the survivor's marker and the retired book's row, so a list that
// two survivors share has its records split between them, and the records that
// say how a book of one survivor stands to a book of the other are in neither
// survivor's journal alone. The graph therefore reads, through a loader
// (SetJournalLoader), the journal of every book it reaches while walking, of
// every book named by a record it uses (to know whether that book is a survivor
// of this list, and so which book it stood in for at that time), and of every
// listed book that lies between the bounds the walks found. It stops when
// every one of them is read.
//
// And it never guesses. A slot is used only when both walks reach a listed book
// (the head and the tail count) and nothing is listed between the two bounds
// but the survivor itself: any other book there is one the records do not order
// against the retired book, and putting the book beside the bounds would be a
// guess. In that case, and when a journal cannot be read or the reading would
// pass maxJournalLoads, slot reports not-ok and the caller falls back to the
// recorded index and says so (RestoreMembership's note).

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
	node, _ := h.survivorEpochAt(created, id)
	return node
}

// survivorEpochAt is survivorNodeAt that also returns the epoch's root marker
// when the survivor was in the list on a retire's account (nil when it was an
// original member).
func (h *markerHistory) survivorEpochAt(created int64, id string) (node string, root *carryMarker) {
	probe := &carryMarker{created: created, id: id}
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
		return "", nil
	case root.standingAt(created):
		return root.retired, root
	}
	return ghostID(root), root
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
	id       string
	retired  string
	survivor string
	prev     string
	next     string
	created  int64 // CreatedAt, ns
	took     bool  // a marker-derived record of a retire where the survivor took the retired book's slot
}

// JournalLoader reads the journal of one book (database GetBookChanges).
type JournalLoader func(bookID string) ([]*database.OperationChange, error)

// maxJournalLoads bounds the journals one slot lookup reads. The books a walk
// reaches are the neighbours of one book in one list, a handful; a lookup that
// would read more is a list whose history cannot be followed, and it is
// reported and placed by index instead.
const maxJournalLoads = 128

// MembershipGraph is the recorded adjacencies of the carries of retired books
// into survivors' slots of one list (a collection or playlist). It is built
// from the journals it is given (AddRows, AddJournal) and the ones it reads
// through its loader (SetJournalLoader), and keeps EVERY record, reverted or
// not, of every retire into any survivor: a book retired, restored and retired
// again contributes all of them, and two survivors in one list each
// contribute theirs. The survivor it was made for (NewMembershipGraph) is the
// one whose slot is being reverted; the others are known only by their journals.
type MembershipGraph struct {
	survivor string
	kind     string
	listID   string

	row    *database.OperationChange // the row being reverted (SetRow)
	rows   []*database.OperationChange
	byID   map[string]int // row id -> index in rows
	loaded map[string]bool
	load   JournalLoader
	loads  int
	err    error

	built bool
	hists map[string]*markerHistory
	recs  []anchored
}

// NewMembershipGraph is an empty graph for kind (UserContentCollection or
// UserContentPlaylist) list listID and the survivor.
func NewMembershipGraph(survivor, kind, listID string) *MembershipGraph {
	return &MembershipGraph{survivor: survivor, kind: kind, listID: listID,
		byID: map[string]int{}, loaded: map[string]bool{}}
}

// SetJournalLoader lets the graph read the journals of the books it reaches.
// Without one it places a book only when every journal its place rests on was
// handed in (AddRows / AddJournal), and otherwise reports not-ok so the caller
// falls back to the recorded index and says so.
func (g *MembershipGraph) SetJournalLoader(load JournalLoader) { g.load = load }

// Err is the first journal read error the graph met, if any. A graph that met
// one places nothing (slot reports not-ok).
func (g *MembershipGraph) Err() error { return g.err }

// AddRows takes rows into the graph (a later row with the same id replaces the
// earlier one). Only the carry rows of this list count: the survivors' markers
// and the rows of retired books. Voided rows, rows of another list and rows
// with no anchors (journaled before they were recorded) are left out of the
// adjacencies; unparsable ones too. AddRows does not mark any journal read:
// use AddJournal for a book's whole journal.
func (g *MembershipGraph) AddRows(rows []*database.OperationChange) {
	for _, r := range rows {
		if r == nil {
			continue
		}
		if i, ok := g.byID[r.ID]; ok {
			g.rows[i] = r
			continue
		}
		g.byID[r.ID] = len(g.rows)
		g.rows = append(g.rows, r)
	}
	g.built = false
}

// SetRow names the row being reverted and takes it into the graph (AddRows).
// It matters when the retired book is itself a survivor of this list: the book
// it is put back as is then the one it stood in for when it was retired
// (markerHistory.survivorNodeAt), found from the row's time.
func (g *MembershipGraph) SetRow(row *database.OperationChange) {
	g.row = row
	g.AddRows([]*database.OperationChange{row})
}

// AddJournal is AddRows for the whole journal of book bookID, which counts as
// read.
func (g *MembershipGraph) AddJournal(bookID string, rows []*database.OperationChange) {
	g.AddRows(rows)
	g.loaded[bookID] = true
}

// ensure reads the journal of book id through the loader, once. It reports
// whether a journal was read now.
func (g *MembershipGraph) ensure(id string) bool {
	if id == "" || g.load == nil || g.loaded[id] || g.err != nil {
		return false
	}
	g.loaded[id] = true
	g.loads++
	rows, err := g.load(id)
	if err != nil {
		g.err = fmt.Errorf("read the journal of book %s: %w", id, err)
		return false
	}
	g.AddRows(rows)
	return true
}

func (g *MembershipGraph) build() {
	if g.built {
		return
	}
	g.built = true
	g.hists, g.recs = map[string]*markerHistory{}, nil
	took := UserContentField(TookKind(g.kind), g.listID)
	held := UserContentField(HeldKind(g.kind), g.listID)
	var survivors []string
	for _, r := range g.rows {
		if r.ChangeType == ChangeTypeUserContentFollow && !r.Voided && (r.FieldName == took || r.FieldName == held) &&
			!slices.Contains(survivors, r.BookID) {
			survivors = append(survivors, r.BookID)
		}
	}
	slices.Sort(survivors)
	seen := map[string]bool{}
	key := func(op, retired string) string { return op + "\x00" + retired }
	for _, sv := range survivors {
		h := newMarkerHistory(sv, g.kind, g.listID, g.rows)
		g.hists[sv] = h
		for _, m := range h.ms {
			if !m.recOK || !m.rec.Anchored || seen[key(m.op, m.retired)] {
				continue
			}
			seen[key(m.op, m.retired)] = true
			g.recs = append(g.recs, anchored{id: m.id, retired: m.retired, survivor: sv, prev: m.rec.Prev, next: m.rec.Next, created: m.created, took: m.took})
		}
	}
	main := UserContentField(g.kind, g.listID)
	for _, r := range g.rows {
		if r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.FieldName != main || seen[key(r.OperationID, r.BookID)] {
			continue
		}
		rec, err := DecodeMembership(r.OldValue)
		if err != nil || !rec.Anchored {
			continue
		}
		seen[key(r.OperationID, r.BookID)] = true
		g.recs = append(g.recs, anchored{id: r.ID, retired: r.BookID, survivor: r.NewValue, prev: rec.Prev, next: rec.Next, created: r.CreatedAt.UnixNano()})
	}
}

// node maps an anchor recorded at a's time to its graph node: "" is the head
// or the tail (empty); a survivor of this list is the book it stood in for at
// that time (or its epoch's ghost), see markerHistory.survivorNodeAt; any other
// id is itself.
func (g *MembershipGraph) node(id, empty string, a anchored) string {
	if id == "" {
		return empty
	}
	if h := g.hists[id]; h != nil {
		if x := h.survivorNodeAt(a.created, a.id); x != "" {
			return x
		}
	}
	return id
}

// retiredNode maps the retired book of a record to its graph node: a book that
// was itself a survivor of this list, standing in the list on a retire's account
// when it was retired, is the book it stood in for then (or its epoch's ghost),
// exactly as when it is named as an anchor (node); any other book is itself.
func (g *MembershipGraph) retiredNode(a anchored) string {
	return g.node(a.retired, a.retired, a)
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
		ret := g.retiredNode(a)
		add(g.node(a.prev, anchorHead, a), ret)
		add(ret, g.node(a.next, anchorTail, a))
		// A survivor standing in a retired book's slot is that book while the
		// book is out, and the book plus the survivor after it once the book is
		// back (its epoch's ghost). A book recorded right AFTER such a survivor
		// is after both; one recorded right before it is before both.
		// The survivor that took the retired book's slot is, once the book is
		// back, a book of its own (its epoch's ghost) right after it and before
		// whatever followed the slot.
		if a.took {
			add(ret, ghostPrefix+a.id)
			add(ghostPrefix+a.id, g.node(a.next, anchorTail, a))
		}
		if h := g.hists[a.prev]; h != nil && a.prev != "" {
			if node, root := h.survivorEpochAt(a.created, a.id); root != nil && node == root.retired {
				add(ghostID(root), ret)
			}
		}
	}
	return preds, succs
}

const ghostPrefix = "\x00ghost:"

// slot finds where retired goes back into cur by walking the records, as
// described at the top of the file: right after the last listed book among
// those reached walking backwards (the head counts as listed), provided both
// walks reach a listed book and nothing but the survivor is listed between the
// two bounds. ok is false when that does not hold, so the caller falls back to
// the index and says so.
func (g *MembershipGraph) slot(cur []string, retired string) (at int, ok bool) {
	if g == nil {
		return 0, false
	}
	g.ensure(retired)
	for {
		g.build()
		preds, succs := g.edges()
		// The node the book is put back as: itself, or, for a survivor retired
		// while it stood in the list on a retire's account, the book it stood in
		// for (retiredNode).
		start := retired
		if g.row != nil && g.row.BookID == retired {
			for _, a := range g.recs {
				if a.id == g.row.ID || a.retired == retired && a.survivor == g.row.NewValue && a.created == g.row.CreatedAt.UnixNano() {
					start = g.retiredNode(a)
					break
				}
			}
		}
		// The survivors in the list now: a survivor whose epoch is alive is in
		// the list as the book it stands in for, or as its epoch's ghost.
		aliveSurvivor := map[string]bool{}
		onlyGhost := map[string]bool{}   // survivors listed as a ghost: their slot's book is back
		standsFor := map[string]string{} // node -> the survivor listed as it
		for sv, h := range g.hists {
			root, alive := h.epochNow()
			if !alive {
				continue
			}
			aliveSurvivor[sv] = true
			standsFor[ghostID(root)] = sv
			if root.standing() {
				standsFor[root.retired] = sv
			} else {
				onlyGhost[sv] = true
			}
		}
		// pos is the index of node n in cur, -2 when n is not listed.
		pos := func(n string) int {
			switch n {
			case anchorHead:
				return -1
			case anchorTail:
				return len(cur)
			}
			if aliveSurvivor[n] {
				return -2 // the survivor in the list now is its epoch's, not an original member
			}
			if i := slices.Index(cur, n); i >= 0 {
				return i
			}
			if sv, ok := standsFor[n]; ok {
				if i := slices.Index(cur, sv); i >= 0 {
					return i
				}
			}
			return -2
		}
		// walk collects the listed nodes reached from retired through absent
		// ones, and every node it visited.
		visited := map[string]bool{start: true}
		walk := func(next map[string][]string) (found []int) {
			seen := map[string]bool{start: true}
			queue := slices.Clone(next[start])
			for len(queue) > 0 {
				n := queue[0]
				queue = queue[1:]
				if seen[n] {
					continue
				}
				seen[n] = true
				visited[n] = true
				if p := pos(n); p > -2 {
					found = append(found, p)
					continue
				}
				queue = append(queue, next[n]...)
			}
			return found
		}
		before, after := walk(preds), walk(succs)
		resolved, lo, hi := false, 0, 0
		var gap []string
		if len(before) > 0 && len(after) > 0 {
			lo = min(slices.Max(before)+1, len(cur))
			hi = min(max(slices.Min(after), 0), len(cur))
			if lo <= hi {
				resolved = true
				for _, e := range cur[lo:hi] {
					// The survivor being reverted may stand between the
					// bounds (the book goes right before it). So may another
					// survivor's ghost: its slot's book is back, it is listed
					// only for the retires that still stand on it and leaves
					// by value, so where it sits among books it was never
					// recorded against changes no book's place.
					if e != g.survivor && !onlyGhost[e] {
						resolved = false
						gap = append(gap, e)
					}
				}
			}
		}
		// Everything the decision rests on must be read: the books reached,
		// the books named by the records between them (a book named in a
		// record may be a survivor of this list, and then it stood for another
		// book at that time), and the books left in the gap.
		var need []string
		want := func(id string) {
			if id != "" && !strings.HasPrefix(id, ghostPrefix) && id != anchorHead && id != anchorTail &&
				!g.loaded[id] && !slices.Contains(need, id) {
				need = append(need, id)
			}
		}
		for n := range visited {
			want(n)
		}
		for _, e := range gap {
			want(e)
		}
		// The records that name a book left in the gap count like the ones
		// that name a book the walks visited: they say how that book stands to
		// the books around it, and a gap book's own journal is read only to find
		// them.
		inGap := map[string]bool{}
		for _, e := range gap {
			inGap[e] = true
		}
		for _, a := range g.recs {
			p, q := g.node(a.prev, anchorHead, a), g.node(a.next, anchorTail, a)
			if r := g.retiredNode(a); visited[r] || visited[p] || visited[q] || inGap[r] || inGap[p] || inGap[q] {
				want(a.retired)
				want(a.prev)
				want(a.next)
				want(a.survivor)
			}
		}
		if g.load == nil {
			// Nothing can be read: the decision rests only on what was
			// handed in, and a book whose journal was not handed in may hold
			// the record that moves the answer. Place only when everything
			// the decision rests on is in already.
			if len(need) > 0 {
				return 0, false
			}
			return lo, resolved
		}
		if g.err != nil {
			return 0, false
		}
		if len(need) == 0 {
			// Everything the decision rests on is read.
			return lo, resolved
		}
		slices.Sort(need)
		for _, id := range need {
			if g.loads >= maxJournalLoads {
				g.err = fmt.Errorf("ordering it needs more than %d journals read, the most one placement reads", maxJournalLoads)
				return 0, false
			}
			g.ensure(id)
			if g.err != nil {
				return 0, false
			}
		}
	}
}
