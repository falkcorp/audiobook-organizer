// file: internal/audiobooks/revert_settle.go
// version: 1.11.0
// guid: 3f8c2a71-5d94-4e6b-b0a3-9c1e7d2f4a58
// last-edited: 2026-10-24

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// WHY ONE SETTLE PASS AFTER THE ROWS. A row's revert writes only its own
// field. Settling a version group's primary from inside a row (as this file's
// predecessor did, row by row) ran before the same book's later rows: a
// hand-off after the soft-delete row crowned the book, and its own demote row
// then found true where it expected the false the operation wrote and
// refused as changed-since, leaving the operation partial for good; or the
// hand-off crowned a sibling and the demote row then put a nil (read as
// primary) beside it. Settling once per group, after every row has run, sees
// the book as the whole revert leaves it.

// settleOwedKeyPrefix keys the record of the groups a revert could not
// settle, per operation, so the next revert run retries them
// (settleOwedRecord).
const settleOwedKeyPrefix = "revert_settle_owed:"

// settleOwedRecord is what a revert persists for the groups whose settle
// failed: for each group, the operation's original primaries (in the order
// settleGroup tries them) and the members that were explicit true when the
// settle failed. A member explicit true at retry that was not then is a
// later pick (a user's), and the owed settle is dropped rather than
// override it.
//
// Pending is the intent RevertOperation writes BEFORE its row loop: the
// groups of every book with a demote, hand-off, soft-delete or merged-into
// row, each with Pending set and no ExplicitTrue snapshot. A run that dies
// (or whose mark fails) between its rows and its settle leaves it; the next
// run then counts those rows found already restored as evidence too, and
// settles the groups. settleGroups overwrites or deletes it.
type settleOwedRecord struct {
	Pending bool                       `json:"pending,omitempty"`
	Groups  map[string]settleOwedGroup `json:"groups"`
}

type settleOwedGroup struct {
	Originals    []string `json:"originals,omitempty"`
	ExplicitTrue []string `json:"explicit_true,omitempty"`
	// Pending: listed by a run's intent, not by a failed settle; settled
	// as touched (no later-pick check, since no snapshot was taken).
	Pending bool `json:"pending,omitempty"`
}

// settleInput is what RevertOperation hands settleGroups.
type settleInput struct {
	plan    *undo.RevertPlan
	touched []settleTouch
	// refusedDemote: books whose primary demote row this run refused
	// (changed since, say): a soft-delete restore of such a book does not
	// make it an original.
	refusedDemote map[string]bool
	// crowned: per retired book, what the operation's hand-off notes
	// recorded (handOffEvidenceOf).
	crowned map[string]handOffEvidence
	// view is the owed-settle state this run started from (loadSettleView):
	// the record it writes, what it owes, and the groups an EARLIER run's
	// intent left pending. Nil reads the operation's own, now.
	view *settleView
}

// settleView is the owed-settle state one revert run works from, read ONCE at
// its start and never changed on disk before that run's settle.
//
// A whole-operation run reads the operation's record and every
// revert_settle_owed:<op>#scope: record a scoped run left, and folds them in
// memory (owed). It rewrites only the operation's record, and deletes the
// scoped ones (folded) only after that rewrite succeeded: a run that dies, or
// returns early, anywhere before its settle has moved no intent, so every
// record is still where the run that owns it will look. A scoped run reads its
// own record and, read-only, the operation record's pending groups, so the
// intent a dead whole-operation run left for the scoped run's books is
// evidence too. A scoped run never writes the operation's record.
type settleView struct {
	kv rawKV
	// record is the key this run writes: the operation's own, or a scoped
	// run's (revertScope.recordID).
	record string
	// owed is what the settle pass reads: the run's own record, with the
	// scoped records folded in on a whole-operation run.
	owed settleOwedRecord
	// priorPending: the groups an EARLIER run's intent left pending when
	// this run started. Only those Pending groups are settled from the
	// record; this run's own intent is not evidence (a retire cut off
	// before it wrote has rows, and its group must be left alone).
	priorPending map[string]bool
	// folded are the scoped records' keys whose groups are in owed; deleted
	// only once the operation's record holds them (settleGroups).
	folded []string
	// unreadable: the run's own record could not be read or decoded. Its
	// content is unknown, so the run writes nothing to it and deletes
	// nothing folded into it: a write would drop the groups it owes.
	unreadable bool
}

// loadSettleView reads, and writes nothing, the owed-settle state of a run of
// operationID (scope nil: the whole operation).
func (rs *RevertService) loadSettleView(operationID string, scope *revertScope) *settleView {
	kv, _ := rs.db.(rawKV)
	v := &settleView{kv: kv, record: operationID, priorPending: map[string]bool{}}
	if scope != nil {
		v.record = scope.recordID(operationID)
	}
	own, err := readSettleOwed(kv, v.record)
	if err != nil {
		revertLog.Warn("revert: the owed group settles of operation %s are unreadable and left as they are: %s",
			logger.SanitizeLogValue(v.record), logger.SanitizeLogValue(err.Error()))
		v.unreadable = true
	}
	v.owed = own
	if scope == nil {
		rs.foldScopedSettleRecords(v, operationID)
	}
	for gid, og := range v.owed.Groups {
		if og.Pending {
			v.priorPending[gid] = true
		}
	}
	if scope != nil {
		// Read-only: a whole-operation run that died leaves its intent in the
		// operation's record, where only a whole-operation run would look.
		for gid := range rs.settlePendingGroups(operationID) {
			v.priorPending[gid] = true
		}
	}
	return v
}

// foldScopedSettleRecords folds into v.owed, in memory, the owed-settle records
// of the operation's scoped runs (RevertOperationForBooks, the combine undo's
// retire reversal). Each keeps a record of its own so it cannot clear the whole
// operation's, but a scoped run that dies after restoring rows and before
// settling leaves its intent there, and a whole-operation run that did not look
// would not count the rows that run restored as evidence: a version group could
// stay with no primary or two, silently. A record that cannot be decoded, or a
// scan that fails, is logged and left where it is (never folded, never
// deleted). The caller holds the operation's lock, which every scoped run holds
// too, so no scoped run is writing a record while this reads it.
func (rs *RevertService) foldScopedSettleRecords(v *settleView, operationID string) {
	scanner, _ := rs.db.(interface {
		ScanPrefix(prefix string) ([]database.KVPair, error)
	})
	if v.kv == nil || scanner == nil {
		return
	}
	rows, err := scanner.ScanPrefix(settleOwedKeyPrefix + operationID + scopedRecordMarker)
	if err != nil {
		revertLog.Warn("revert: scan the scoped owed group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
		return
	}
	for _, row := range rows {
		scoped, err := decodeSettleOwed(row.Value)
		if err != nil {
			revertLog.Warn("revert: the scoped owed group settles %s are undecodable and left as they are: %s",
				logger.SanitizeLogValue(row.Key), logger.SanitizeLogValue(err.Error()))
			continue
		}
		for gid, g := range scoped.Groups {
			if cur, ok := v.owed.Groups[gid]; ok {
				g = mergeSettleOwedGroup(cur, g)
			}
			v.owed.Groups[gid] = g
		}
		v.folded = append(v.folded, row.Key)
	}
}

// hasOwed reports whether the run owes groups to a failed settle (a pending
// intent alone does not count).
func (v *settleView) hasOwed() bool {
	for _, og := range v.owed.Groups {
		if !og.Pending {
			return true
		}
	}
	return false
}

// markPending records group gid as pending in the run's own record.
func (rs *RevertService) markPending(v *settleView, gid string) {
	if v.unreadable {
		return
	}
	rs.markSettlePending(v.record, gid)
}

// deleteFolded deletes the scoped records folded into the run's owed set. Call
// it only after the run's own record holds what they owed.
func (v *settleView) deleteFolded() {
	for _, key := range v.folded {
		if err := v.kv.DeleteRaw(key); err != nil {
			revertLog.Warn("revert: clear the scoped owed group settles %s: %s",
				logger.SanitizeLogValue(key), logger.SanitizeLogValue(err.Error()))
		}
	}
	v.folded = nil
}

// clear drops a pending-only record as stale: the run's own, and the scoped
// ones folded into it.
func (v *settleView) clear() {
	if v.unreadable {
		return
	}
	deleteSettleOwed(v.kv, v.record)
	v.deleteFolded()
}

// opLocks serialises reverts of one operation in this process, so two
// concurrent reverts of it cannot interleave the owed record's
// load-modify-store (or its rows). It is keyed by the exact operation id
// (a refcounted map, not striped by hash): the combine undo's retire
// reversal takes the RETIRE op's lock while the combine op's own revert may
// already hold the combine op's, and two different ops sharing a stripe
// would make that a self-deadlock (or a TryLock that always fails).
var opLocks = struct {
	mu sync.Mutex
	m  map[string]*opLockEntry
}{m: map[string]*opLockEntry{}}

type opLockEntry struct {
	mu   sync.Mutex
	refs int
}

// opLockRef takes a reference on operationID's entry (creating it); the entry
// is dropped when its last reference is released.
func opLockRef(operationID string) *opLockEntry {
	opLocks.mu.Lock()
	defer opLocks.mu.Unlock()
	e := opLocks.m[operationID]
	if e == nil {
		e = &opLockEntry{}
		opLocks.m[operationID] = e
	}
	e.refs++
	return e
}

func opLockUnref(operationID string, e *opLockEntry) {
	opLocks.mu.Lock()
	defer opLocks.mu.Unlock()
	if e.refs--; e.refs == 0 {
		delete(opLocks.m, operationID)
	}
}

// lockOperation takes operationID's revert lock and returns its release.
func lockOperation(operationID string) func() {
	e := opLockRef(operationID)
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		opLockUnref(operationID, e)
	}
}

// tryLockOperation is lockOperation that gives up instead of waiting: for a
// caller that already holds the merge lock. RevertOperation takes the
// operation lock and THEN the merge lock (once per row), so waiting for an
// operation lock while holding the merge lock could deadlock; refusing when it
// is busy cannot.
func tryLockOperation(operationID string) (release func(), ok bool) {
	e := opLockRef(operationID)
	if !e.mu.TryLock() {
		opLockUnref(operationID, e)
		return nil, false
	}
	return func() {
		e.mu.Unlock()
		opLockUnref(operationID, e)
	}, true
}

// handOffEvidence is what an operation's notes say about the primary
// hand-off of one retired book's version group.
type handOffEvidence struct {
	// crowned: the members a hand-off note of the operation names as made
	// primary in a form that may mean a write: "wrote:<id>" and the
	// ambiguous "crowned:<id>" of 2026-10-02..06 (never "kept:<id>").
	// laterPick does not yield to them: the operation may have written
	// their true, so the original is crowned back over them, as before.
	crowned []string
	// wrote: the members a note names as WRITTEN, only in the unambiguous
	// "wrote:<id>" form (undo.HandOffWrote). Only these may have their
	// flag put back past the iTunes guard (settleGroup's own set): an
	// ambiguous "crowned:" note may name a member the hand-off only kept,
	// whose flag the operation never wrote.
	wrote []string
	// recorded: the operation recorded how its hand-off ended -- a member
	// made primary (any form), a member kept without a write ("kept:<id>",
	// undo.HandOffKept), or a refusal that wrote nothing
	// (undo.ChangeTypeBookPrimaryHandoffRefused). Every explicit-true
	// member not in crowned then carries a true the operation never wrote.
	// A hand-off note naming nobody (journaled before 2026-10-02), or no
	// note at all, records nothing: the crown-back rule stands.
	//
	// The crash windows, where no note says how a hand-off ended:
	//   1. the hand-off wrote and the process died before its note: the
	//      revert crowns the original back over the member it crowned,
	//      undoing the write (correct);
	//   2. the hand-off refused (nothing written) and the process died
	//      before the refusal note: the revert crowns the original back and
	//      demotes an incumbent the operation never wrote (the pre-fix
	//      behaviour, for that window only);
	//   3. a folder-books or duplicate-copies crown journaled its demote
	//      rows and died before the crown: the rows are live for writes
	//      that never happened; each demote revert finds its flag still
	//      true (already restored), so it is not counted as touched and the
	//      group is left alone.
	recorded bool
}

// merge folds o into ev (a group's evidence is the union over its
// originals, so the outcome never depends on which original is read first).
func (ev handOffEvidence) merge(o handOffEvidence) handOffEvidence {
	ev.recorded = ev.recorded || o.recorded
	for _, id := range o.crowned {
		if !slices.Contains(ev.crowned, id) {
			ev.crowned = append(ev.crowned, id)
		}
	}
	for _, id := range o.wrote {
		if !slices.Contains(ev.wrote, id) {
			ev.wrote = append(ev.wrote, id)
		}
	}
	return ev
}

// handOffEvidenceOf collects every hand-off note's evidence, per retired
// book.
func handOffEvidenceOf(changes []*database.OperationChange) map[string]handOffEvidence {
	out := map[string]handOffEvidence{}
	for _, c := range changes {
		if c == nil {
			continue
		}
		ev := out[c.BookID]
		switch {
		case c.ChangeType == undo.ChangeTypeBookPrimaryHandoffRefused:
			ev.recorded = true
		default:
			if _, ok := undo.HandOffKept(c); ok {
				ev.recorded = true
			} else if id, ok := undo.HandOffCrowned(c); ok {
				ev.recorded = true
				ev = ev.merge(handOffEvidence{crowned: []string{id}})
				if w, ok := undo.HandOffWrote(c); ok {
					ev = ev.merge(handOffEvidence{wrote: []string{w}})
				}
			} else {
				continue
			}
		}
		out[c.BookID] = ev
	}
	return out
}

// bookGroup is a book's current version group, or "".
func (rs *RevertService) bookGroup(id string) string {
	b, err := rs.db.GetBookByID(id)
	if err != nil || b == nil || b.VersionGroupID == nil {
		return ""
	}
	return strings.TrimSpace(*b.VersionGroupID)
}

// settlePendingGroups is the set of groups an earlier run's intent left
// pending in the operation's record (settleOwedGroup.Pending).
func (rs *RevertService) settlePendingGroups(operationID string) map[string]bool {
	kv, _ := rs.db.(rawKV)
	out := map[string]bool{}
	rec, err := readSettleOwed(kv, operationID)
	if err != nil {
		revertLog.Warn("revert: read the pending group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
		return out
	}
	for gid, og := range rec.Groups {
		if og.Pending {
			out[gid] = true
		}
	}
	return out
}

// markSettlePending adds group gid to the operation's record as pending,
// keeping whatever else it holds. Called the first time a settle row of a
// book in gid writes. A store without raw keys, or a failed write (logged),
// records nothing: a run that then dies before its settle leaves the group
// to version-group-primary-repair.
func (rs *RevertService) markSettlePending(operationID, gid string) {
	kv, _ := rs.db.(rawKV)
	if kv == nil {
		return
	}
	rec, err := readSettleOwed(kv, operationID)
	if err != nil {
		// Unknown content: a write would drop the groups the record owes.
		revertLog.Warn("revert: the owed group settles of operation %s are unreadable, the pending settle of group %s is not recorded: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(gid), logger.SanitizeLogValue(err.Error()))
		return
	}
	if _, ok := rec.Groups[gid]; ok {
		return
	}
	rec.Groups[gid] = settleOwedGroup{Pending: true}
	rec.Pending = true
	raw, err := json.Marshal(rec)
	if err == nil {
		err = kv.SetRaw(settleOwedKeyPrefix+operationID, raw)
	}
	if err != nil {
		revertLog.Warn("revert: record the pending settle of group %s of operation %s: %s",
			logger.SanitizeLogValue(gid), logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
	}
}

// rawKV is the raw key surface the owed record is kept in, asserted on the
// store rather than widening revertServiceStore.
type rawKV interface {
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
}

// settleTouch is one reverted row's evidence about a version group.
type settleTouch struct {
	bookID, changeType, oldValue string
}

// settleNote reports whether a row is of a type that can change who a
// group's primary is, and so may be evidence for the settle pass. Whether
// it IS evidence is the caller's call: a row that wrote in this run, or one
// found already restored whose group an earlier run left pending.
func settleNote(c *database.OperationChange) (settleTouch, bool) {
	switch c.ChangeType {
	case undo.ChangeTypeBookPrimaryDemote, undo.ChangeTypeBookPrimaryHandoff,
		undo.ChangeTypeBookSoftDelete, undo.ChangeTypeBookMergedInto:
		return settleTouch{bookID: c.BookID, changeType: c.ChangeType, oldValue: c.OldValue}, true
	}
	return settleTouch{}, false
}

// settleGroups is the settle pass of RevertOperation: every version group
// one of touched names (by the book's current group), plus every group the
// operation's owed record lists, gets settleGroup. Groups the operation did
// not touch are never read or written. Failures are reported in result
// (HandOffFailed) and recorded for retry; the record is cleared once every
// group settles.
//
// The originals of a group are, in order: the books whose primary demote
// (OldValue "true", or "" for a never-set flag, which read as primary) or
// hand-off note this run reverted; then the books whose soft-delete this run
// restored when the operation also demoted them or handed their group off
// (plan.ChangedPrimary). A soft-delete restore alone (a duplicate copy that
// was never primary) names no original.
func (rs *RevertService) settleGroups(operationID string, in settleInput, result *RevertResult) []string {
	plan, touched := in.plan, in.touched
	type groupWork struct {
		originals []string
		touched   bool
		owed      *settleOwedGroup
	}
	groups := map[string]*groupWork{}
	var order []string
	add := func(gid string) *groupWork {
		g, ok := groups[gid]
		if !ok {
			g = &groupWork{}
			groups[gid] = g
			order = append(order, gid)
		}
		return g
	}
	var errMsgs []string
	groupOfBook := func(id string) string {
		b, err := rs.db.GetBookByID(id)
		if err != nil || b == nil || b.VersionGroupID == nil {
			return ""
		}
		return strings.TrimSpace(*b.VersionGroupID)
	}
	var second []settleTouch
	for _, t := range touched {
		gid := groupOfBook(t.bookID)
		if gid == "" {
			continue
		}
		g := add(gid)
		g.touched = true
		switch {
		case t.changeType == undo.ChangeTypeBookPrimaryDemote && (t.oldValue == "true" || t.oldValue == ""),
			t.changeType == undo.ChangeTypeBookPrimaryHandoff:
			if !slices.Contains(g.originals, t.bookID) {
				g.originals = append(g.originals, t.bookID)
			}
		case t.changeType == undo.ChangeTypeBookSoftDelete && plan != nil && plan.ChangedPrimary(t.bookID) && !in.refusedDemote[t.bookID]:
			second = append(second, t)
		}
	}
	for _, t := range second {
		g := add(groupOfBook(t.bookID))
		if !slices.Contains(g.originals, t.bookID) {
			g.originals = append(g.originals, t.bookID)
		}
	}

	view := in.view
	if view == nil {
		view = rs.loadSettleView(operationID, nil)
	}
	for gid, og := range view.owed.Groups {
		if og.Pending {
			if !view.priorPending[gid] {
				continue // this run's own intent: not evidence
			}
			// Listed by an earlier run's intent: that run's rows are
			// this run's evidence (already restored), so it is touched.
			g := add(gid)
			g.touched = true
			continue
		}
		g := add(gid)
		if !g.touched {
			// Only owed, not touched again by this run: retried under the
			// later-pick check.
			g.owed = &og
		}
		for _, o := range og.Originals {
			if !slices.Contains(g.originals, o) {
				g.originals = append(g.originals, o)
			}
		}
	}
	slices.Sort(order)

	left := settleOwedRecord{Groups: map[string]settleOwedGroup{}}
	for _, gid := range order {
		g := groups[gid]
		if gid == "" {
			continue
		}
		explicit, err := rs.settleGroup(operationID, gid, g.originals, g.owed, in.crowned)
		if err == nil {
			continue
		}
		if errors.Is(err, versionprimary.ErrWriteRefused) {
			// Settling would write an iTunes book's primary flag, which is
			// never done: the group is left as it stands, reported, and
			// not recorded for retry (a retry would refuse the same way
			// until the member changes; version-group-primary-repair or
			// the owner settles it).
			result.SettleSkipped = append(result.SettleSkipped, fmt.Sprintf(
				"version group %s (originals %s): left unsettled, settling it would write an iTunes book's primary flag: %v",
				gid, strings.Join(g.originals, ","), err))
			continue
		}
		left.Groups[gid] = settleOwedGroup{Originals: g.originals, ExplicitTrue: explicit}
		msg := fmt.Sprintf("version group %s (originals %s): %v", gid, strings.Join(g.originals, ","), err)
		result.HandOffFailed = append(result.HandOffFailed, msg)
		errMsgs = append(errMsgs, msg)
	}
	// The run's own record is rewritten from what it just read, and only
	// then are the scoped records folded into it deleted: nothing moved
	// before this point, so a crash anywhere above leaves every record where
	// its own retry finds it. A record the run could not read is left alone.
	var storeErr error
	if view.unreadable {
		storeErr = errors.New("the owed record is unreadable")
	} else {
		storeErr = rs.storeSettleOwed(view.kv, view.record, left)
	}
	if storeErr != nil {
		for i := range result.HandOffFailed {
			result.HandOffFailed[i] += "; the retry could not be recorded (" + storeErr.Error() + "), run version-group-primary-repair"
		}
	} else {
		view.deleteFolded()
		for i := range result.HandOffFailed {
			result.HandOffFailed[i] += "; left to retry on the next revert of this operation"
		}
	}
	return errMsgs
}

// settleGroup leaves version group gid with one primary when the operation
// changed it:
//
//	(a) the first original that is live and Electable is crowned
//	    (versionprimary.Crown: explicit true on it, explicit false on every
//	    other live member, so a nil beside a true cannot survive);
//	(b) otherwise, when 0 or 2+ Electable members read as primary
//	    (database.EffectiveIsPrimaryVersion), the group is handed off
//	    (versionprimary.EnsureSinglePrimary);
//	(c) otherwise it is left alone.
//
// Before (a), an original yields instead when another Electable member is
// explicit true that the operation did not write (laterPick): a user's pick
// made since, or an incumbent the operation's hand-off kept (or never
// reached, its hand-off refused) whose true predates the operation. The
// revert writes only what the operation changed, so that member keeps its
// flag and the original comes back explicit false -- the rule
// versionprimary.YieldToIncumbent applies to a restored row.
//
// When (a)'s crown is refused (it would write an iTunes member the
// operation never wrote: versionprimary.ErrWriteRefused), nothing of it is
// written; every original then yields the same way, so its restored true
// never stays beside the true the operation's hand-off wrote, the group is
// judged as in (b)/(c), and the refusal is returned (the caller reports the
// group as left unsettled, SettleSkipped).
//
// owed is set for a group retried only from the operation's owed record:
// if any Electable member is explicit true now that was not when the settle
// failed, that is a later pick (a user's) and nothing is written; the owed
// record is dropped (nil error).
//
// On an error it returns the members explicit true now, for the owed
// record.
func (rs *RevertService) settleGroup(opID, gid string, originals []string, owed *settleOwedGroup, crowned map[string]handOffEvidence) ([]string, error) {
	// Every is_primary_version write below records history, Source
	// operation_revert, BatchID the operation id (revertHistoryStore).
	hist := revertHistoryStore{revertServiceStore: rs.db, opID: opID}
	// Neither the crown nor the hand-off below writes an iTunes book's
	// primary flag that the operation did not write: each asks this under
	// the group lock about every member it would write, and refuses the
	// whole write (ErrWriteRefused) instead.
	//
	// The one exception is a member whose flag THIS operation wrote, as its
	// own journal records it, never inferred from the group's current
	// state: an original (a book_primary_demote or hand-off row of this
	// operation, or the owed record a failed settle of it left), or a member
	// a book_primary_handoff note of this operation names in the
	// unambiguous "wrote:<id>" form (handOffEvidence.wrote). Never a
	// "kept:<id>" member, and never one named only by the ambiguous
	// "crowned:<id>" form, which was journaled for kept members too.
	// Putting such a flag back restores the state before the operation,
	// which the approval of the operation's write already covered. The folder-books fixer depends on it: the owner cleared it
	// (2026-10-01, repairs.ITunesDatabaseOnly) to write the rows of books
	// under books/itunes/**, and refusing the undo of its own write would
	// leave the original's restored true beside the crowned member's.
	// The group's evidence is the union over its originals, so neither the
	// own set nor laterPick depends on which original is read first.
	var ev handOffEvidence
	for _, o := range originals {
		ev = ev.merge(crowned[o])
	}
	own := map[string]bool{}
	for _, o := range originals {
		own[o] = true
	}
	for _, w := range ev.wrote {
		own[w] = true
	}
	guard := itunesguard.MayWrite(rs.db, gid)
	mayWrite := func(m *database.Book) error {
		if own[m.ID] {
			return nil
		}
		return guard(m)
	}
	defer rs.lockMerge()()
	members, err := rs.db.GetBooksByVersionGroup(gid)
	if err != nil {
		return nil, fmt.Errorf("read version group %s: %w", gid, err)
	}
	alive := versionprimary.StoreAlive(rs.db)
	var explicit []string
	effective := 0
	electable := map[string]bool{}
	for i := range members {
		m := &members[i]
		if !versionprimary.Electable(m, alive) {
			continue
		}
		electable[m.ID] = true
		if database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
			effective++
		}
		if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			explicit = append(explicit, m.ID)
		}
	}
	if owed != nil {
		for _, id := range explicit {
			if !slices.Contains(owed.ExplicitTrue, id) {
				revertLog.Info("revert: owed settle of version group %s dropped: %s was made primary since",
					logger.SanitizeLogValue(gid), logger.SanitizeLogValue(id))
				return nil, nil
			}
		}
	}
	// yieldAll returns every electable original explicit non-primary and
	// recounts the group's primaries without them.
	yieldAll := func() error {
		for _, y := range originals {
			if !electable[y] {
				continue
			}
			if err := rs.yieldOriginal(opID, y); err != nil {
				return fmt.Errorf("return %s non-primary in group %s: %w", y, gid, err)
			}
		}
		effective = 0
		for i := range members {
			m := &members[i]
			if !slices.Contains(originals, m.ID) && electable[m.ID] && database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
				effective++
			}
		}
		return nil
	}
	// refused: the crown of an original was refused (ErrWriteRefused); the
	// originals yielded and the group is reported as left unsettled.
	var refused error
	for _, o := range originals {
		if !electable[o] {
			continue
		}
		if later := laterPick(explicit, originals, ev); later != "" {
			// The operation recorded how its hand-off ended, and another
			// member is explicit primary that the operation did not write (a
			// user's pick since, or an incumbent the hand-off kept or never
			// reached): the original yields, explicit false so its nil is
			// not read as a second primary, and the group is judged as it
			// stands below.
			revertLog.Info("revert: version group %s keeps %s, which the operation did not make primary; %s returns non-primary",
				logger.SanitizeLogValue(gid), logger.SanitizeLogValue(later), logger.SanitizeLogValue(o))
			// EVERY electable original yields (a folder-books row demotes
			// each primary folder-book, so there can be several): one left
			// true would be elected over the later pick below.
			if err := yieldAll(); err != nil {
				return explicit, err
			}
			break
		}
		_, err := versionprimary.CrownEnv(hist, gid, o, versionprimary.Env{MayWrite: mayWrite})
		if err == nil {
			return nil, nil
		}
		err = fmt.Errorf("crown %s and demote the rest of group %s: %w", o, gid, err)
		if !errors.Is(err, versionprimary.ErrWriteRefused) {
			return explicit, err
		}
		// The crown would write an iTunes member the operation never
		// wrote, so it wrote nothing. The original's restored true must not
		// stay beside the true the operation's hand-off wrote (two primaries
		// in Audiobookshelf): every original yields -- it is the
		// operation's own write, so the guard does not apply -- and the
		// group is judged as it stands below. The group is still reported
		// as left unsettled (the refusal is returned): the original did not
		// get its flag back.
		revertLog.Info("revert: version group %s: crowning %s back would write an iTunes book's primary flag; the originals return non-primary",
			logger.SanitizeLogValue(gid), logger.SanitizeLogValue(o))
		if yerr := yieldAll(); yerr != nil {
			return explicit, errors.Join(err, yerr)
		}
		refused = err
		break
	}
	if effective != 1 {
		if _, err := versionprimary.EnsureSinglePrimary(context.Background(), hist, gid,
			versionprimary.Env{RootDir: merge.TrashRestoreEnv().RootDir, MayWrite: mayWrite}); err != nil {
			return explicit, fmt.Errorf("hand off version group %s: %w", gid, err)
		}
	}
	if refused != nil {
		return nil, refused
	}
	return nil, nil
}

// laterPick returns an explicit-true member that is neither an original nor
// one the operation's hand-off recorded crowning (writing), or "". A member
// a hand-off note records KEEPING, or any member when the hand-off recorded
// a refusal, is returned: the operation never wrote its true. With nothing
// recorded (a hand-off note journaled before the field existed, or no note
// at all) it returns "": the original is crowned as before.
func laterPick(explicit, originals []string, ev handOffEvidence) string {
	if !ev.recorded {
		return ""
	}
	for _, id := range explicit {
		if !slices.Contains(originals, id) && !slices.Contains(ev.crowned, id) {
			return id
		}
	}
	return ""
}

// yieldOriginal writes an explicit false on an original that yields to a
// later pick (a nil would read as primary).
// The write records history like every other revert write (modifyBook); a
// book gone by now is nothing to yield, as before.
func (rs *RevertService) yieldOriginal(opID, id string) error {
	err := rs.modifyBook(opID, id, func(b *database.Book) error {
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			return database.ErrSkipBookWrite
		}
		no := false
		b.IsPrimaryVersion = &no
		return nil
	})
	var gone *undo.ReferentError
	if errors.Is(err, database.ErrSkipBookWrite) || (errors.As(err, &gone) && gone.Reason == undo.ReasonBookMissing) {
		return nil
	}
	return err
}

// readSettleOwed reads one owed record: none (or no raw store) is an empty
// record. A failed read, or bytes that do not decode, is an error and never an
// empty record, so a caller cannot write over groups it failed to read.
func readSettleOwed(kv rawKV, record string) (settleOwedRecord, error) {
	empty := settleOwedRecord{Groups: map[string]settleOwedGroup{}}
	if kv == nil {
		return empty, nil
	}
	raw, err := kv.GetRaw(settleOwedKeyPrefix + record)
	if err != nil {
		return empty, fmt.Errorf("read: %w", err)
	}
	return decodeSettleOwed(raw)
}

// decodeSettleOwed decodes one owed record's stored bytes; none is an empty
// record, and bytes that do not decode are an error.
func decodeSettleOwed(raw []byte) (settleOwedRecord, error) {
	rec := settleOwedRecord{Groups: map[string]settleOwedGroup{}}
	if len(raw) == 0 {
		return rec, nil
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return settleOwedRecord{Groups: map[string]settleOwedGroup{}}, fmt.Errorf("decode: %w", err)
	}
	if rec.Groups == nil {
		rec.Groups = map[string]settleOwedGroup{}
	}
	return rec, nil
}

// storeSettleOwed writes the record, or deletes it when nothing is owed.
func (rs *RevertService) storeSettleOwed(kv rawKV, operationID string, rec settleOwedRecord) error {
	if len(rec.Groups) == 0 {
		deleteSettleOwed(kv, operationID)
		return nil
	}
	if kv == nil {
		return errors.New("the store keeps no retry records")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return kv.SetRaw(settleOwedKeyPrefix+operationID, raw)
}

// deleteSettleOwed removes the operation's record. A failed delete is
// logged, not returned: both callers reach it only after the settle the
// record was for has finished, so there is nothing left for them to retry,
// and a leftover record only makes a later revert of the operation run the
// settle pass again for the groups it names.
func deleteSettleOwed(kv rawKV, operationID string) {
	if kv == nil {
		return
	}
	if err := kv.DeleteRaw(settleOwedKeyPrefix + operationID); err != nil {
		revertLog.Warn("revert: clear the owed group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
	}
}

// scopedRecordMarker joins an operation id and a scope's shell ids in the id of
// a scoped run's owed record (revertScope.recordID).
const scopedRecordMarker = "#scope:"

// mergeSettleOwedGroup combines the two records of one group (the operation's
// and a scoped run's; see foldScopedSettleRecords), the conservative way. A group owed by a failed settle
// beats a pending intent: it carries the originals and the snapshot of the
// members explicit true, so the later-pick check still protects a user's pick
// made since. Two owed entries union their originals and keep only the members
// explicit true in both snapshots: a member in just one of them reads as a
// later pick, and the owed settle is dropped rather than override it.
func mergeSettleOwedGroup(a, b settleOwedGroup) settleOwedGroup {
	switch {
	case a.Pending && b.Pending:
		return a
	case a.Pending:
		return b
	case b.Pending:
		return a
	}
	out := settleOwedGroup{Originals: slices.Clone(a.Originals)}
	for _, o := range b.Originals {
		if !slices.Contains(out.Originals, o) {
			out.Originals = append(out.Originals, o)
		}
	}
	for _, id := range a.ExplicitTrue {
		if slices.Contains(b.ExplicitTrue, id) {
			out.ExplicitTrue = append(out.ExplicitTrue, id)
		}
	}
	return out
}
