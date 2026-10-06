// file: internal/audiobooks/revert_settle.go
// version: 1.5.0
// guid: 3f8c2a71-5d94-4e6b-b0a3-9c1e7d2f4a58
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
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
	// priorPending: the groups an EARLIER run's intent left pending when
	// this run started. Only those Pending groups are settled from the
	// record; this run's own intent is not evidence (a retire cut off
	// before it wrote has rows, and its group must be left alone).
	priorPending map[string]bool
}

// opLocks serialises reverts of one operation in this process, so two
// concurrent reverts of it cannot interleave the owed record's
// load-modify-store (or its rows).
var opLocks [64]sync.Mutex

// lockOperation takes operationID's revert lock and returns its release.
func lockOperation(operationID string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(operationID))
	mu := &opLocks[h.Sum32()%uint32(len(opLocks))]
	mu.Lock()
	return mu.Unlock
}

// handOffEvidence is what an operation's notes say about the primary
// hand-off of one retired book's version group.
type handOffEvidence struct {
	// crowned: the members a hand-off of the operation WROTE explicit true
	// on ("crowned:<id>" notes; a note journaled before 2026-10-06 may
	// name a member it only kept, and cannot be told apart).
	crowned []string
	// recorded: the operation recorded how its hand-off ended -- a member
	// crowned, a member kept without a write ("kept:<id>",
	// undo.HandOffKept), or a refusal that wrote nothing
	// (undo.ChangeTypeBookPrimaryHandoffRefused). Every explicit-true
	// member not in crowned then carries a true the operation never wrote.
	// A hand-off note naming nobody (journaled before 2026-10-02), or no
	// note at all (a retire cut off before its hand-off, or after it wrote
	// and before its note), records nothing: the crown-back rule stands.
	recorded bool
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
				if !slices.Contains(ev.crowned, id) {
					ev.crowned = append(ev.crowned, id)
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
	for gid, og := range rs.loadSettleOwed(kv, operationID).Groups {
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
	rec := rs.loadSettleOwed(kv, operationID)
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

// clearSettleOwed deletes the operation's record.
func (rs *RevertService) clearSettleOwed(operationID string) {
	kv, _ := rs.db.(rawKV)
	deleteSettleOwed(kv, operationID)
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

	kv, _ := rs.db.(rawKV)
	owed := rs.loadSettleOwed(kv, operationID)
	for gid, og := range owed.Groups {
		og := og
		if og.Pending {
			if !in.priorPending[gid] {
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
	if err := rs.storeSettleOwed(kv, operationID, left); err != nil {
		for i := range result.HandOffFailed {
			result.HandOffFailed[i] += "; the retry could not be recorded (" + err.Error() + "), run version-group-primary-repair"
		}
	} else {
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
	// primary flag: each asks this under the group lock about every member
	// it would write, and refuses the whole write (ErrWriteRefused) instead.
	mayWrite := itunesguard.MayWrite(rs.db, gid)
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
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
	for _, o := range originals {
		if !electable[o] {
			continue
		}
		if later := laterPick(explicit, originals, crowned[o]); later != "" {
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
			for _, y := range originals {
				if !electable[y] {
					continue
				}
				if err := rs.yieldOriginal(opID, y); err != nil {
					return explicit, fmt.Errorf("return %s non-primary in group %s: %w", y, gid, err)
				}
			}
			effective = 0
			for i := range members {
				m := &members[i]
				if !slices.Contains(originals, m.ID) && electable[m.ID] && database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
					effective++
				}
			}
			break
		}
		if _, err := versionprimary.CrownEnv(hist, gid, o, versionprimary.Env{MayWrite: mayWrite}); err != nil {
			return explicit, fmt.Errorf("crown %s and demote the rest of group %s: %w", o, gid, err)
		}
		return nil, nil
	}
	if effective == 1 {
		return nil, nil
	}
	if _, err := versionprimary.EnsureSinglePrimary(context.Background(), hist, gid,
		versionprimary.Env{RootDir: merge.TrashRestoreEnv().RootDir, MayWrite: mayWrite}); err != nil {
		return explicit, fmt.Errorf("hand off version group %s: %w", gid, err)
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

// loadSettleOwed reads the operation's owed record; none (or no raw store,
// or an unreadable one, which is logged) is an empty record.
func (rs *RevertService) loadSettleOwed(kv rawKV, operationID string) settleOwedRecord {
	rec := settleOwedRecord{Groups: map[string]settleOwedGroup{}}
	if kv == nil {
		return rec
	}
	raw, err := kv.GetRaw(settleOwedKeyPrefix + operationID)
	if err != nil {
		revertLog.Warn("revert: read the owed group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
		return rec
	}
	if len(raw) == 0 {
		return rec
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		revertLog.Warn("revert: decode the owed group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
		return settleOwedRecord{Groups: map[string]settleOwedGroup{}}
	}
	if rec.Groups == nil {
		rec.Groups = map[string]settleOwedGroup{}
	}
	return rec
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

// hasSettleOwed reports whether the operation has groups owed a settle by
// a failed one (a pending intent alone does not count: see
// settlePendingGroups).
func (rs *RevertService) hasSettleOwed(operationID string) bool {
	kv, _ := rs.db.(rawKV)
	for _, og := range rs.loadSettleOwed(kv, operationID).Groups {
		if !og.Pending {
			return true
		}
	}
	return false
}
