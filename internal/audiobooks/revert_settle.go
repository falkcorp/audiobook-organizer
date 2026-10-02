// file: internal/audiobooks/revert_settle.go
// version: 1.1.0
// guid: 3f8c2a71-5d94-4e6b-b0a3-9c1e7d2f4a58
// last-edited: 2026-10-02

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
	// crowned: per retired book, the members the operation's hand-off notes
	// recorded crowning (undo.HandOffCrowned).
	crowned map[string][]string
	// priorPending: the record held an EARLIER run's intent when this run
	// started. Only then are its Pending groups settled; this run's own
	// intent is not evidence (a retire cut off before it wrote has rows,
	// and its group must be left alone).
	priorPending bool
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

// crownedByHandOff collects every hand-off note's recorded winner, per
// retired book.
func crownedByHandOff(changes []*database.OperationChange) map[string][]string {
	out := map[string][]string{}
	for _, c := range changes {
		if id, ok := undo.HandOffCrowned(c); ok && !slices.Contains(out[c.BookID], id) {
			out[c.BookID] = append(out[c.BookID], id)
		}
	}
	return out
}

// writeSettlePending records the intent before the row loop: the groups of
// rows (and the groups already owed, kept as they are). It returns whether
// an earlier run's intent was still pending, which makes already-restored
// rows evidence in this run. A store without raw keys records nothing.
func (rs *RevertService) writeSettlePending(operationID string, rows []*database.OperationChange) bool {
	kv, _ := rs.db.(rawKV)
	if kv == nil {
		return false
	}
	rec := rs.loadSettleOwed(kv, operationID)
	prior := rec.Pending
	seen := map[string]bool{}
	for _, c := range rows {
		if _, ok := settleNote(c); !ok || seen[c.BookID] {
			continue
		}
		seen[c.BookID] = true
		b, err := rs.db.GetBookByID(c.BookID)
		if err != nil || b == nil || b.VersionGroupID == nil || strings.TrimSpace(*b.VersionGroupID) == "" {
			continue
		}
		gid := strings.TrimSpace(*b.VersionGroupID)
		if _, owed := rec.Groups[gid]; !owed {
			rec.Groups[gid] = settleOwedGroup{Pending: true}
		}
	}
	if len(rec.Groups) == 0 {
		return prior
	}
	rec.Pending = true
	raw, err := json.Marshal(rec)
	if err == nil {
		err = kv.SetRaw(settleOwedKeyPrefix+operationID, raw)
	}
	if err != nil {
		revertLog.Warn("revert: record the pending group settles of operation %s: %s",
			logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
	}
	return prior
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

// settleNote decides, after a row's revert, whether the row is evidence
// for the settle pass: only a row that wrote in this run (not one found
// already restored: a retire cut off before it wrote changed nothing), of a
// type that can change who a group's primary is.
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

	kv, _ := rs.db.(rawKV)
	owed := rs.loadSettleOwed(kv, operationID)
	for gid, og := range owed.Groups {
		og := og
		if og.Pending {
			if !in.priorPending {
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
		explicit, err := rs.settleGroup(gid, g.originals, g.owed, in.crowned)
		if err == nil {
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
// owed is set for a group retried only from the operation's owed record:
// if any Electable member is explicit true now that was not when the settle
// failed, that is a later pick (a user's) and nothing is written; the owed
// record is dropped (nil error).
//
// On an error it returns the members explicit true now, for the owed
// record.
func (rs *RevertService) settleGroup(gid string, originals []string, owed *settleOwedGroup, crowned map[string][]string) ([]string, error) {
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
			// The operation recorded whom its hand-off crowned, and someone
			// else has been made primary since (a user's pick): the original
			// yields, explicit false so its nil is not read as a second
			// primary, and the group is judged as it stands below.
			revertLog.Info("revert: version group %s keeps %s, made primary after the operation; %s returns non-primary",
				logger.SanitizeLogValue(gid), logger.SanitizeLogValue(later), logger.SanitizeLogValue(o))
			if err := rs.yieldOriginal(o); err != nil {
				return explicit, fmt.Errorf("return %s non-primary in group %s: %w", o, gid, err)
			}
			effective = 0
			for i := range members {
				m := &members[i]
				if m.ID != o && electable[m.ID] && database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
					effective++
				}
			}
			break
		}
		if _, err := versionprimary.Crown(rs.db, gid, o); err != nil {
			return explicit, fmt.Errorf("crown %s and demote the rest of group %s: %w", o, gid, err)
		}
		return nil, nil
	}
	if effective == 1 {
		return nil, nil
	}
	if _, err := versionprimary.EnsureSinglePrimary(context.Background(), rs.db, gid,
		versionprimary.Env{RootDir: merge.TrashRestoreEnv().RootDir}); err != nil {
		return explicit, fmt.Errorf("hand off version group %s: %w", gid, err)
	}
	return nil, nil
}

// laterPick returns an explicit-true member that is neither an original nor
// one the operation's hand-off recorded crowning, or "". With no recorded
// winner (a hand-off note journaled before the field existed, or no note at
// all) it returns "": the original is crowned as before.
func laterPick(explicit, originals, crowned []string) string {
	if len(crowned) == 0 {
		return ""
	}
	for _, id := range explicit {
		if !slices.Contains(originals, id) && !slices.Contains(crowned, id) {
			return id
		}
	}
	return ""
}

// yieldOriginal writes an explicit false on an original that yields to a
// later pick (a nil would read as primary).
func (rs *RevertService) yieldOriginal(id string) error {
	_, err := rs.db.ModifyBook(id, func(b *database.Book) error {
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			return database.ErrSkipBookWrite
		}
		no := false
		b.IsPrimaryVersion = &no
		return nil
	})
	if errors.Is(err, database.ErrSkipBookWrite) {
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
	key := settleOwedKeyPrefix + operationID
	if len(rec.Groups) == 0 {
		if kv == nil {
			return nil
		}
		if err := kv.DeleteRaw(key); err != nil {
			revertLog.Warn("revert: clear the owed group settles of operation %s: %s",
				logger.SanitizeLogValue(operationID), logger.SanitizeLogValue(err.Error()))
		}
		return nil
	}
	if kv == nil {
		return errors.New("the store keeps no retry records")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return kv.SetRaw(key, raw)
}

// hasSettleOwed reports whether the operation has groups owed a settle.
func (rs *RevertService) hasSettleOwed(operationID string) bool {
	kv, _ := rs.db.(rawKV)
	return len(rs.loadSettleOwed(kv, operationID).Groups) > 0
}
