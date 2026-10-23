// file: internal/undo/membership_anchor.go
// version: 3.4.1
// guid: 7c2e9a40-5b1d-4f86-a3d7-e0b8c4f19a65
// last-edited: 2026-10-23

package undo

import (
	"fmt"
	"math"
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
// are mapped to it, see markerHistory.survivorEpochAt); once the book is restored
// the survivor is a book of its own (it sits right after the restored one), so
// the records that name it then keep naming it.
//
// More than one survivor in a list. The records of a retire live in two
// journals, the survivor's marker and the retired book's row, so a list that
// two survivors share has its records split between them, and the records that
// say how a book of one survivor stands to a book of the other are in neither
// survivor's journal alone. The graph is therefore built from the list's whole
// journal (database GetListChanges: the opchange_by_list index), every row any
// retire into the list wrote, so no record can sit in a journal it did not read.
//
// Epochs. A survivor is in the list on a retire's account from a "took" marker
// (it entered the list for that retire) until the last retire of the epoch is
// reverted. A "took" marker is written only when the survivor was not listed, so
// it also ends every epoch before it: a standing marker of an earlier epoch no
// longer accounts for the survivor's place (the survivor left the list outside
// the journal, by hand, or a revert removed it and died before stamping the
// marker). The rows of a superseded epoch have no say over the survivor.
//
// And it never guesses. A slot is used only when both walks reach a listed book
// (the head and the tail count) and nothing is listed between the two bounds
// but the survivor itself (and a survivor listed only as a ghost, which leaves by
// value): any other book there is one no record of the list orders against the
// retired book (the owner put it there), and putting the book beside the bounds
// would be a guess. It also checks the records against the list: every record
// is an order fact for all time, so two books the records order (directly, or
// through absent books) must be listed in that order; when they are not, the
// owner re-arranged the list since, no slot found from the records can be
// trusted, and nothing is placed from them. In both cases slot reports not-ok
// and the caller falls back to the recorded index and says so
// (RestoreMember's note). A survivor the owner moved, or listed again, away
// from the slot the retire gave it is left where it was put and reported
// (placement.kept): the revert never moves a book the owner placed, and never
// does so silently.

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
	created   int64 // CreatedKey: the journal's order (journal_order.go)
	reverted  int64 // RevertedKey; 0 while the marker stands
	borrowing bool  // the survivor is in the list on this marker's account
	// root is the epoch the marker belongs to: the latest "took" marker at or
	// before it (nil before the first). A "took" marker is written only when
	// the survivor was not listed, so it ends every epoch before it: a marker
	// of an earlier epoch no longer accounts for the survivor's place, even
	// while it stands (the survivor left the list outside the journal, by
	// hand, or a revert removed it and died before stamping the marker).
	root *carryMarker
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
//
// Only a marker that accounts for a live retire counts. The carry journals a
// retire's marker and its row in one commit (repairs JournalSteps), so they
// normally exist together; but a journal written by an older build, a resumed
// run, or a failed write can leave a marker with no row, and a standing marker
// says "this retire's book stands in the list": believed, it would keep the
// survivor listed for every other retire for good. So a marker is ignored when
//
//   - its retire has no non-voided row of the list (the retired book's row, in
//     the marker's operation, naming the survivor: the rule PairedRow
//     applies), or
//   - a later marker of the same survivor names the same retire (a resumed run
//     that journaled the retire again with another record): only the latest
//     counts.
//
// Ignored is never silent: ignored holds each one, MembershipGraph.Ignored lists them,
// and the revert reports every one it meets (RevertResult.OrphanMarkers).
type markerHistory struct {
	survivor string
	ms       []*carryMarker
	ignored  []IgnoredMarker
}

// The reasons a survivor-side marker is ignored (IgnoredMarker.Reason).
const (
	IgnoredNoRow    = "no live row of the retire it names"
	IgnoredReplaced = "a later marker names the same retire"
)

// IgnoredMarker is a survivor-side marker the membership decisions left out
// (markerHistory): it accounts for no live retire.
type IgnoredMarker struct {
	ID          string
	OperationID string
	Survivor    string
	Retired     string
	Field       string
	// Standing: the marker is not stamped reverted. Only a standing marker could
	// have changed a decision, so the revert reports those.
	Standing bool
	Reason   string
}

// Detail is the one-line account of the marker for a report.
func (m IgnoredMarker) Detail() string {
	return fmt.Sprintf("marker %s (%s on book %s, operation %s, retired book %s) was ignored: %s",
		m.ID, m.Field, m.Survivor, m.OperationID, m.Retired, m.Reason)
}

// retireKey names one retire of a list: its operation, the retired book and
// the survivor it was carried into.
func retireKey(op, retired, survivor string) string {
	return op + "\x00" + retired + "\x00" + survivor
}

// liveRetires is the set of retires of list listID of kind that have a
// non-voided row among rows: the main rows PairedRow pairs a marker with.
func liveRetires(rows []*database.OperationChange, kind, listID string) map[string]bool {
	main := UserContentField(kind, listID)
	live := map[string]bool{}
	for _, r := range rows {
		if r != nil && r.ChangeType == ChangeTypeUserContentFollow && !r.Voided && r.FieldName == main {
			live[retireKey(r.OperationID, r.BookID, r.NewValue)] = true
		}
	}
	return live
}

func newMarkerHistory(survivor, kind, listID string, rows []*database.OperationChange) *markerHistory {
	return newMarkerHistoryWith(survivor, kind, listID, rows, liveRetires(rows, kind, listID))
}

// newMarkerHistoryWith is newMarkerHistory with the set of live retires of the
// list given (liveRetires), for a caller building several histories of one list.
func newMarkerHistoryWith(survivor, kind, listID string, rows []*database.OperationChange, live map[string]bool) *markerHistory {
	took := UserContentField(TookKind(kind), listID)
	held := UserContentField(HeldKind(kind), listID)
	h := &markerHistory{survivor: survivor}
	type cand struct {
		m   *carryMarker
		row *database.OperationChange
	}
	var cands []cand
	for _, r := range rows {
		if r == nil || r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.BookID != survivor {
			continue
		}
		if r.FieldName != took && r.FieldName != held {
			continue
		}
		m := &carryMarker{id: r.ID, op: r.OperationID, retired: r.NewValue, took: r.FieldName == took, created: CreatedKey(r), reverted: RevertedKey(r)}
		if rec, err := DecodeMembership(r.OldValue); err == nil {
			m.rec, m.recOK = rec, true
		}
		cands = append(cands, cand{m, r})
	}
	slices.SortFunc(cands, func(a, b cand) int {
		switch {
		case a.m.before(b.m):
			return -1
		case b.m.before(a.m):
			return 1
		}
		return 0
	})
	ignore := func(c cand, reason string) {
		h.ignored = append(h.ignored, IgnoredMarker{ID: c.row.ID, OperationID: c.row.OperationID, Survivor: survivor,
			Retired: c.row.NewValue, Field: c.row.FieldName, Standing: c.row.RevertedAt == nil, Reason: reason})
	}
	latest := map[string]int{} // retire -> index of its latest marker
	for i, c := range cands {
		if live[retireKey(c.m.op, c.m.retired, survivor)] {
			latest[retireKey(c.m.op, c.m.retired, survivor)] = i
		}
	}
	for i, c := range cands {
		k := retireKey(c.m.op, c.m.retired, survivor)
		switch {
		case !live[k]:
			ignore(c, IgnoredNoRow)
		case latest[k] != i:
			ignore(c, IgnoredReplaced)
		default:
			h.ms = append(h.ms, c.m)
		}
	}
	var root *carryMarker
	for i, m := range h.ms {
		if m.took {
			root = m
		}
		m.root = root
		if m.took {
			m.borrowing = true
			continue
		}
		for _, e := range h.ms[:i] {
			if e.borrowing && e.root == m.root && e.standingAt(m.created) {
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

// survivorEpochAt names the survivor as an anchor of a record written at time
// created (ns), ordered after the markers before it: "" when the survivor was
// an original member of the list (it is itself); otherwise it is in the list
// on a retire's account, and it is the book of the epoch's "took" marker while
// that marker stands (the survivor took that book's slot and the book has not
// been restored), else the epoch's ghost (the book was restored and the survivor
// still stands for other retires, just after it). The marker itself is not
// before itself. It also returns the epoch's root marker when the survivor was
// in the list on a retire's account (nil when it was an original member).
func (h *markerHistory) survivorEpochAt(created int64, id string) (node string, root *carryMarker) {
	probe := &carryMarker{created: created, id: id}
	alive := false
	for _, m := range h.ms {
		if !m.before(probe) {
			break
		}
		if m.took {
			root, alive = m, false
		}
		if m.borrowing && m.root == root && m.standingAt(created) {
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
			root, alive = m, false
		}
		if m.borrowing && m.root == root && m.standing() {
			alive = true
		}
	}
	return root, alive && root != nil
}

// superseded: m belongs to an epoch a later "took" marker ended. Its retire no
// longer accounts for the survivor's place in the list, whether or not it
// stands.
func (h *markerHistory) superseded(m *carryMarker) bool {
	root, _ := h.epochNow()
	return root != nil && m.root != root
}

// anchored is one carry's record, as the graph keeps it.
type anchored struct {
	op       string
	id       string
	retired  string
	survivor string
	prev     string
	next     string
	created  int64 // CreatedKey: the journal's order
	took     bool  // a marker-derived record of a retire where the survivor took the retired book's slot
}

// MembershipGraph is the recorded adjacencies of the carries of retired books
// into survivors' slots of one list (a collection or playlist). It is built
// from the list's whole journal (AddRows with database GetListChanges: every
// row any retire into the list wrote, on the survivors' books and the retired
// books alike) and keeps EVERY record, reverted or not, of every retire into any
// survivor: a book retired, restored and retired again contributes all of them,
// and two survivors in one list each contribute theirs. The survivor it was
// made for (NewMembershipGraph) is the one whose slot is being reverted.
type MembershipGraph struct {
	survivor string
	kind     string
	listID   string

	row  *database.OperationChange // the row being reverted (SetRow)
	rows []*database.OperationChange
	byID map[string]int // row id -> index in rows

	built   bool
	hists   map[string]*markerHistory
	recs    []anchored
	ignored []IgnoredMarker
}

// Ignored lists the survivor-side markers of the list's journal the graph left
// out, oldest first.
func (g *MembershipGraph) Ignored() []IgnoredMarker {
	g.build()
	out := slices.Clone(g.ignored)
	slices.SortFunc(out, func(a, b IgnoredMarker) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// NewMembershipGraph is an empty graph for kind (UserContentCollection or
// UserContentPlaylist) list listID and the survivor.
func NewMembershipGraph(survivor, kind, listID string) *MembershipGraph {
	return &MembershipGraph{survivor: survivor, kind: kind, listID: listID, byID: map[string]int{}}
}

// AddRows takes rows into the graph (a later row with the same id replaces the
// earlier one). Only the carry rows of this list count: the survivors' markers
// and the rows of retired books. Voided rows, rows of another list and rows
// whose record does not parse are left out of the adjacencies. The graph decides
// from what it is given and nothing else, so it must be given the whole of the
// list's journal.
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
// (markerHistory.survivorEpochAt), found from the row's time.
func (g *MembershipGraph) SetRow(row *database.OperationChange) {
	g.row = row
	g.AddRows([]*database.OperationChange{row})
}

func (g *MembershipGraph) build() {
	if g.built {
		return
	}
	g.built = true
	g.hists, g.recs, g.ignored = map[string]*markerHistory{}, nil, nil
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
	live := liveRetires(g.rows, g.kind, g.listID)
	seen := map[string]bool{}
	key := func(op, retired string) string { return op + "\x00" + retired }
	for _, sv := range survivors {
		h := newMarkerHistoryWith(sv, g.kind, g.listID, g.rows, live)
		g.ignored = append(g.ignored, h.ignored...)
		if len(h.ms) == 0 {
			continue // every marker of this survivor was ignored: it has no history
		}
		g.hists[sv] = h
		for _, m := range h.ms {
			if !m.recOK || seen[key(m.op, m.retired)] {
				continue
			}
			seen[key(m.op, m.retired)] = true
			g.recs = append(g.recs, anchored{op: m.op, id: m.id, retired: m.retired, survivor: sv, prev: m.rec.Prev, next: m.rec.Next, created: m.created, took: m.took})
		}
	}
	main := UserContentField(g.kind, g.listID)
	for _, r := range g.rows {
		if r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.FieldName != main || seen[key(r.OperationID, r.BookID)] {
			continue
		}
		rec, err := DecodeMembership(r.OldValue)
		if err != nil {
			continue
		}
		seen[key(r.OperationID, r.BookID)] = true
		g.recs = append(g.recs, anchored{op: r.OperationID, id: r.ID, retired: r.BookID, survivor: r.NewValue, prev: rec.Prev, next: rec.Next, created: CreatedKey(r)})
	}
}

// node maps an anchor recorded at a's time to its graph node: "" is the head
// or the tail (empty); a survivor of this list is the book it stood in for at
// that time (or its epoch's ghost), see markerHistory.survivorEpochAt; any other
// id is itself.
func (g *MembershipGraph) node(id, empty string, a anchored) string {
	if id == "" {
		return empty
	}
	return g.resolve(id, a.created, a.id, 0)
}

// resolve is node for an anchor written at time created (ns) after marker aid.
func (g *MembershipGraph) resolve(id string, created int64, aid string, depth int) string {
	return g.slotNode(id, created, aid, created, depth)
}

// slotNode is the node survivor id was at time t0 (after marker aid0), viewed
// at time t (ns, t >= t0): a survivor standing in a retired book's slot is that
// book while the book is out, and the book's epoch ghost once it is back. The
// book it stood in for may itself have been a survivor then (a survivor retired
// into a survivor that took its slot): the slot it held at the time it was
// taken is followed down the chain, looked at as of t, so it is the ghost of
// the first level whose book is back by t, outermost first.
func (g *MembershipGraph) slotNode(id string, t0 int64, aid0 string, t int64, depth int) string {
	return g.slotPath(id, t0, aid0, t, depth, nil)
}

// slotPath is slotNode that also appends to path (when not nil) every node the
// chain passes through: the book each level stood in for, and the ghost it ends
// in, if it does.
func (g *MembershipGraph) slotPath(id string, t0 int64, aid0 string, t int64, depth int, path *[]string) string {
	h := g.hists[id]
	if h == nil || depth >= 16 {
		return id
	}
	x, root := h.survivorEpochAt(t0, aid0)
	if x == "" {
		return id
	}
	if root != nil && x == root.retired {
		if root.standingAt(t) {
			if path != nil {
				*path = append(*path, x)
			}
			return g.slotPath(x, root.created, root.id, t, depth+1, path)
		}
		x = ghostID(root)
	}
	// x is a ghost: the epoch's own (its book is back), or the one above.
	if path != nil {
		*path = append(*path, x)
	}
	return x
}

// retiredNode maps the retired book of a record to its graph node: a book that
// was itself a survivor of this list, standing in the list on a retire's account
// when it was retired, is the book it stood in for then (or its epoch's ghost),
// exactly as when it is named as an anchor (node); any other book is itself.
func (g *MembershipGraph) retiredNode(a anchored) string {
	return g.node(a.retired, a.retired, a)
}

// startNode is the node a survivor goes back into the list as, when the row
// being reverted is its retire a: the slot it held when it was retired, as of
// now (slotNode): the book it stood in for, or the ghost right after that book
// if the book has been restored since.
func (g *MembershipGraph) startNode(a anchored) string {
	return g.slotNode(a.retired, a.created, a.id, math.MaxInt64, 0)
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
// walks reach a listed book and nothing but the survivor (or a survivor listed
// only as a ghost, which no record orders and which leaves by value) is listed
// between the two bounds. The graph holds the whole journal of the list, so a
// book in the gap is one no record of the list orders against the retired book
// (the owner put it there by hand). ok is false when that does not hold, so the
// caller falls back to the index and says so.
func (g *MembershipGraph) slot(cur []string, retired string) (at int, ok bool) {
	at, _, ok = g.slotRange(cur, retired)
	return at, ok
}

// slotRange is slot that also returns the last index the book may go to: any
// index in [lo, hi] is a slot the records accept, because the books between the
// bounds are survivors that leave by value (see slot). A survivor that stands
// in the retired book's slot is in [lo, hi] of the list without it.
func (g *MembershipGraph) slotRange(cur []string, retired string) (lo, hi int, ok bool) {
	if g == nil {
		return 0, 0, false
	}
	g.build()
	preds, succs := g.edges()
	// The node the book is put back as: itself, or, for a survivor retired
	// while it stood in the list on a retire's account, the book it stood in
	// for (retiredNode).
	start := retired
	if g.row != nil && g.row.BookID == retired {
		for _, a := range g.recs {
			// The record is the marker's when there is one (its id and time,
			// not the row's: the journal writes the two at two instants), so
			// it is matched by its retire: operation, book and survivor.
			if a.id == g.row.ID || a.retired == retired && a.survivor == g.row.NewValue && a.op == g.row.OperationID {
				start = g.startNode(a)
				break
			}
		}
	}
	// The survivors in the list now. A survivor whose epoch is alive is in the
	// list as the slot it stands in, followed down the chain as of now
	// (slotPath): the book whose slot it took while that book is out, the slot
	// that book stood in then if it was itself a survivor, and so on; or a
	// ghost, where the chain reaches a book that is back. Every node on the way
	// is where that survivor is listed (standsFor). A survivor whose chain ends
	// in a ghost is listed only for the retires standing on it and leaves by
	// value with the last of them (onlyGhost). Survivors not in the list go
	// first, so a listed survivor wins a node two of them reach (the unlisted
	// one is followed to it through standInOf anyway).
	aliveSurvivor := map[string]bool{}
	onlyGhost := map[string]bool{}   // survivors listed as a ghost: the book at the end of their chain is back
	standsFor := map[string]string{} // node -> the survivor listed as it
	svs := make([]string, 0, len(g.hists))
	for sv := range g.hists {
		svs = append(svs, sv)
	}
	slices.Sort(svs)
	slices.SortStableFunc(svs, func(a, b string) int {
		switch la, lb := slices.Contains(cur, a), slices.Contains(cur, b); {
		case la == lb:
			return 0
		case lb:
			return -1
		}
		return 1
	})
	for _, sv := range svs {
		root, alive := g.hists[sv].epochNow()
		if !alive {
			continue
		}
		aliveSurvivor[sv] = true
		standsFor[ghostID(root)] = sv
		var path []string
		if end := g.slotPath(sv, math.MaxInt64, "", math.MaxInt64, 0, &path); strings.HasPrefix(end, ghostPrefix) {
			onlyGhost[sv] = true
		}
		for _, n := range path {
			standsFor[n] = sv
		}
	}
	// A survivor retired out of the list since, into a survivor that took its
	// slot (and so on), is where that stand-in is.
	standInOf := map[string]string{}
	mainField := UserContentField(g.kind, g.listID)
	for _, r := range g.rows {
		if r.ChangeType != ChangeTypeUserContentFollow || r.Voided || r.RevertedAt != nil || r.FieldName != mainField {
			continue
		}
		if rec, err := DecodeMembership(r.OldValue); err == nil && !rec.SurvivorWasMember {
			standInOf[r.BookID] = r.NewValue
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
			for hop := 0; hop <= len(standInOf); hop++ {
				if i := slices.Index(cur, sv); i >= 0 {
					return i
				}
				next, ok := standInOf[sv]
				if !ok {
					break
				}
				sv = next
			}
		}
		return -2
	}
	// walk collects the listed nodes reached from retired through absent ones.
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
			if p := pos(n); p > -2 {
				found = append(found, p)
				continue
			}
			queue = append(queue, next[n]...)
		}
		return found
	}
	// The records must agree with the list. Every record is an order fact that
	// holds for all time, so two listed books the records order (directly, or
	// through books that are absent) must be listed in that order. When they are
	// not, the owner re-arranged the list since (a book re-added elsewhere), the
	// records no longer describe it, and no slot found from them can be trusted:
	// the caller reports it instead of placing the book.
	for n := range succs {
		pn := pos(n)
		if pn < 0 || n == anchorTail {
			continue
		}
		seen := map[string]bool{n: true}
		queue := slices.Clone(succs[n])
		for len(queue) > 0 {
			m := queue[0]
			queue = queue[1:]
			if seen[m] {
				continue
			}
			seen[m] = true
			pm := pos(m)
			if pm == -2 {
				queue = append(queue, succs[m]...)
				continue
			}
			if m != anchorTail && pm < pn {
				return 0, 0, false
			}
		}
	}
	before, after := walk(preds), walk(succs)
	if len(before) == 0 || len(after) == 0 {
		return 0, 0, false
	}
	lo = min(slices.Max(before)+1, len(cur))
	hi = min(max(slices.Min(after), 0), len(cur))
	if lo > hi {
		return 0, 0, false
	}
	// The book being put back may itself be a survivor listed only for the
	// retires that stand on it: it leaves by value with the last of them, so
	// where it sits among books the records never ordered against it changes no
	// book's place.
	if onlyGhost[retired] {
		return lo, hi, true
	}
	for _, e := range cur[lo:hi] {
		// The survivor being reverted may stand between the bounds (the book
		// goes right before it). So may another survivor listed only as a
		// ghost: it leaves by value, and no record of the list orders it
		// against the book, so where the book goes beside it changes no book's
		// place. Any other book there is one the records do not order against
		// the retired book: putting the book beside the bounds would be a guess.
		if e != g.survivor && !onlyGhost[e] {
			return 0, 0, false
		}
	}
	return lo, hi, true
}
