// file: internal/plugins/maintenance/master_list_decide.go
// version: 1.1.0
// guid: 7d2e84a1-5b3c-4f90-a6e8-1c9f0b47d352
// last-edited: 2026-10-24

package maintenance

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// This file is the decision half of the master-list import: what a row that
// the matcher settled (matched_high), or that matches no book at all
// (read_elsewhere), will actually write. Plan and Replan both end here, from
// the same inputs, so an unchanged row re-plans to the same fingerprint.

// mlDecision is what Apply writes for a matched row (Row.Detail): the user's
// state and positions on one book, compare-and-set against what was read.
type mlDecision struct {
	user, book   string
	expect, next undo.UserStateSnapshot
}

// mlElsewhereDecision is what Apply writes for a read_elsewhere row
// (Row.Detail): one record, created if absent.
type mlElsewhereDecision struct {
	rec database.MasterListElsewhere
}

// mlDecider decides rows against one library snapshot and the live state of
// the target user. One decider serves every worker of a plan; it holds no
// per-row state.
type mlDecider struct {
	now   func() time.Time
	us    UserReadStateStore
	lists MasterListStore
	m     *mlMatcher
	// elsewhere finds a read-elsewhere record by the row's identifiers. Shared
	// by every decider of one plan or apply run (never nil).
	elsewhere *mlElsewhereIndex
}

// mlElsewhereIndex finds the read-elsewhere records that already name a row's
// book. A record's id is derived from the first identifier its row gave
// (ElsewhereID), so the same book listed by ASIN in one list and by its ISBN
// alone in another has two different ids; the index matches by every identifier
// instead (masterlist.IdentityKeys: ASIN, ISBN-13, source id), per user.
//
// It is read once, on the first row that needs it, and shared for the run: a
// record another apply creates after that is found by its id (decideElsewhere
// reads that live) but not by a second identifier. Rows of one list never share
// an identifier (Collapse), so a run's own creates are never looked for.
// Title-only rows have no identifier and match only by their record id.
type mlElsewhereIndex struct {
	once  sync.Once
	byKey map[string]database.MasterListElsewhere
	err   error
}

func mlElsewhereKey(user, key string) string { return user + "\x00" + key }

// find returns the record of user that shares an identifier with row, if any.
func (x *mlElsewhereIndex) find(lists MasterListStore, user string, row masterlist.Row) (*database.MasterListElsewhere, error) {
	x.once.Do(func() {
		recs, err := lists.ListElsewhere("")
		if err != nil {
			x.err = fmt.Errorf("list the read-elsewhere records: %w", err)
			return
		}
		x.byKey = make(map[string]database.MasterListElsewhere, len(recs))
		// ListElsewhere is newest first: the oldest record of a key wins, by
		// writing in reverse, so the answer does not move as records are added.
		for i := len(recs) - 1; i >= 0; i-- {
			rec := recs[i]
			as := masterlist.Row{ASIN: rec.ASIN, ISBN10: rec.ISBN10, ISBN13: rec.ISBN13}
			for _, s := range rec.Sources {
				as.Sources = append(as.Sources, masterlist.Source{Source: s.Source, SourceID: s.SourceID})
			}
			for _, k := range masterlist.IdentityKeys(as) {
				x.byKey[mlElsewhereKey(rec.UserID, k)] = rec
			}
		}
	})
	if x.err != nil {
		return nil, x.err
	}
	for _, k := range masterlist.IdentityKeys(row) {
		if rec, ok := x.byKey[mlElsewhereKey(user, k)]; ok {
			return &rec, nil
		}
	}
	return nil, nil
}

// mlFingerprint covers every input a decision was made from: the row as
// planned (class, flags, books, stored state), the target book as it reads now
// (title, group, length, the copies whose listening counts), and the user's
// state on all of them. extra is whatever else the decision read.
func mlFingerprint(r repairs.Row, t arsTarget, extra any) string {
	return arsFingerprint(r.RowID, r.Class, r.Flags, r.BookIDs, r.Members, r.State, t.key(), extra)
}

// mlNeedsDecision reports whether a row goes through the decision pass: a
// matched_high row (a write to the matched book) or an unmatched row nothing
// in the library could be (a read-elsewhere record).
func mlNeedsDecision(r repairs.Row, st mlRowState) bool {
	return r.Class == mlMatchedHigh || mlElsewhereEligible(r, st)
}

// mlElsewhereCandidate: an unmatched row with no title candidate and no trashed
// copy carrying one of its identifiers. A row that might still be a library
// book (the title is there under another author's name, or a trashed book holds
// the identifier) is for a person to look at, not to record as read elsewhere.
func mlElsewhereCandidate(r repairs.Row, st mlRowState) bool {
	return r.Class == mlUnmatched && len(st.Candidates) == 0 && len(st.Trashed) == 0 &&
		!slices.Contains(r.Flags, mlFlagAuthorMismatch) && !slices.Contains(r.Flags, mlFlagTrashedOnly)
}

// mlElsewhereEligible: a candidate (above) whose list row also has no data
// problem of its own. A row whose identifier was dropped as bad
// (bad_identifier), whose identifiers disagree (identifier_conflict), or whose
// time is unreadable or in the future may well be a library book the matcher
// could not see (the dropped value was the one that would have matched), and a
// future time would be stored as the record's finish date; a matched_high row
// with any of these is held for review (matchRow), and so is this one. It stays
// an unmatched row, listed with the findings, never a bulk-applicable record.
func mlElsewhereEligible(r repairs.Row, st mlRowState) bool {
	return mlElsewhereCandidate(r, st) && len(st.Row.Issues) == 0
}

// decide runs the decision for a row mlNeedsDecision accepted.
func (d *mlDecider) decide(r repairs.Row, st mlRowState) repairs.Row {
	if r.Class == mlMatchedHigh {
		return d.decideMatched(r, st)
	}
	return d.decideElsewhere(r, st)
}

// mlSkip is a row the apply never writes, of kind (its Class and Skipped).
func mlSkip(r repairs.Row, t arsTarget, kind, why string, extra any) repairs.Row {
	r.Class, r.Skipped, r.SkipReason, r.Reason, r.Risk = kind, kind, why, why, repairs.RiskReview
	r.Detail = nil
	r.Fingerprint = mlFingerprint(r, t, extra)
	return r
}

// target reads the matched book as the decision sees it.
func (d *mlDecider) target(st mlRowState) (arsTarget, error) {
	lib := d.m.lib
	b := lib.books[st.Target]
	if b == nil {
		return arsTarget{}, fmt.Errorf("matched book %s is not in the library snapshot", st.Target)
	}
	t := arsTarget{id: st.Target, title: b.Title, primary: b.IsPrimaryVersion != nil && *b.IsPrimaryVersion}
	if b.VersionGroupID != nil {
		t.vgid = *b.VersionGroupID
	}
	var err error
	if t.durationSec, err = arsLocalDuration(d.m.store, b.ID, b.Duration); err != nil {
		return t, err
	}
	t.related = arsRelatedIDs(st.Target, lib.members[t.vgid], st.Hits)
	return t, nil
}

// mlLastListened is the newest last-listened time of any of the row's sources.
func mlLastListened(r masterlist.Row) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, s := range r.Sources {
		if t, ok := masterlist.ParseTime(s.LastListenedAt); ok && (!found || t.After(newest)) {
			newest, found = t, true
		}
	}
	return newest, found
}

// mlPosition is the in-progress position to write (seconds into the local
// copy) and its percent. The list's position is used as given, capped at the
// local length; a list that gives only a percent has it applied to the local
// length (the local edition's length may differ). ok is false when the row
// gives neither, or gives a percent with no length to apply it to.
func mlPosition(row masterlist.Row, localSec float64) (pos float64, pct int, ok bool) {
	length := localSec
	if length <= 0 && row.RuntimeMin > 0 {
		length = float64(row.RuntimeMin) * 60
	}
	pct = row.ProgressPct
	switch {
	case row.PositionSec > 0:
		pos = float64(row.PositionSec)
		if length > 0 {
			pos = min(pos, length)
			if pct == 0 {
				pct = int(pos / length * 100)
			}
		}
	case pct > 0 && length > 0:
		pos = float64(int64(length * float64(pct) / 100))
	default:
		return 0, 0, false
	}
	// An in-progress book is never 0% or 100%: finishing is the finished
	// status's, with its own time.
	return pos, min(pct, 99), true
}

// decideMatched judges one matched row against the target book and the user's
// state.
func (d *mlDecider) decideMatched(base repairs.Row, st mlRowState) repairs.Row {
	r := base
	row := st.Row
	t, err := d.target(st)
	if err != nil {
		// Fail closed: an unreadable book is not a book with no length.
		return mlSkip(r, t, mlStateUnreadable, err.Error(), nil)
	}
	switch row.Status {
	case "":
		return mlSkip(r, t, mlSkipNoStatus, "the list gives no read status for this row; nothing to import", nil)
	case masterlist.StatusUnstarted:
		// Matched (so it claims its target for the duplicate-target check),
		// never applied.
		return mlSkip(r, t, mlSkipNotStarted, "not started in the list; nothing to import", nil)
	case masterlist.StatusAbandoned:
		return mlSkip(r, t, mlSkipListAbandoned, "abandoned in the list; the import never marks a book abandoned (that is the user's choice to make)", nil)
	}

	var ts time.Time
	var hasTS bool
	switch row.Status {
	case masterlist.StatusFinished:
		ts, hasTS = masterlist.ParseTime(row.FinishedAt)
	case masterlist.StatusInProgress:
		ts, hasTS = mlLastListened(row)
	}
	if hasTS && ts.After(d.now().Add(mlFutureSlack)) {
		// Plan flags a future time on the row (matched_review); this guards a
		// row replanned after the clock moved.
		return mlSkip(r, t, mlSkipFutureTimestamp, fmt.Sprintf("the list's time %s is in the future; it would beat every real listen", ts.Format(time.RFC3339)), nil)
	}

	cur, err := undo.ReadUserStateSnapshot(d.us, st.User, t.id)
	if err != nil {
		return mlSkip(r, t, mlStateUnreadable, err.Error(), nil)
	}
	sum := arsStateSummary(cur)
	// The same book's other copies (every member of the target's version
	// group, and the book the identifier actually hit): listening there is
	// listening to this book.
	related := make([]arsRelatedState, 0, len(t.related))
	for _, id := range t.related {
		snap, err := undo.ReadUserStateSnapshot(d.us, st.User, id)
		if err != nil {
			return mlSkip(r, t, mlStateUnreadable, err.Error(), nil)
		}
		related = append(related, arsRelatedState{id: id, snap: snap})
		sum = append(sum, append([]string{"related:" + id}, arsStateSummary(snap)...)...)
	}
	r.Current = arsDisplay(cur.State)
	local, localBook := arsLocalActivity(cur), t.id
	for _, rs := range related {
		if a := arsLocalActivity(rs.snap); a.After(local) {
			local, localBook = a, rs.id
		}
	}
	where := func(id string) string {
		if id == t.id {
			return "here"
		}
		return "on " + id + ", another copy of this book"
	}
	anyOf := func(pred func(*database.UserBookState) bool) (string, bool) {
		if cur.State != nil && pred(cur.State) {
			return t.id, true
		}
		for _, rs := range related {
			if rs.snap.State != nil && pred(rs.snap.State) {
				return rs.id, true
			}
		}
		return "", false
	}
	abandoned := func(s *database.UserBookState) bool { return s.Status == database.UserBookStatusAbandoned }
	manualUnstarted := func(s *database.UserBookState) bool {
		return s.StatusManual && s.Status == database.UserBookStatusUnstarted
	}
	// What the user already decided stands, whichever way the list reads.
	if cur.State != nil && cur.State.Status == database.UserBookStatusFinished {
		return mlSkip(r, t, mlSkipAlreadyFinished, "already finished here; never un-finished, and its finish date is kept", sum)
	}
	if id, ok := anyOf(abandoned); ok {
		return mlSkip(r, t, mlSkipUserAbandoned, "marked abandoned "+where(id)+"; a user's choice is never overwritten", sum)
	}
	if id, ok := anyOf(manualUnstarted); ok {
		return mlSkip(r, t, mlSkipManualUnstarted, "marked unstarted by hand "+where(id)+"; treated as the user's choice, like abandoned", sum)
	}

	switch row.Status {
	case masterlist.StatusFinished:
		switch {
		case !hasTS:
			return mlSkip(r, t, mlSkipNoTimestamp, "the list gives no finish time for this row, and the import never dates a finish now", sum)
		case local.After(ts):
			return mlSkip(r, t, mlSkipNewerLocal, fmt.Sprintf("local activity %s (%s) is newer than the list's finish %s",
				local.Format(time.RFC3339), where(localBook), ts.Format(time.RFC3339)), sum)
		}
		next := arsCopyState(cur.State, st.User, t.id)
		next.Status = database.UserBookStatusFinished
		next.StatusManual = true
		next.ProgressPct = 100
		finished := ts
		next.FinishedAt = &finished
		next.LastActivityAt = ts
		positions := cur.Positions
		// The ABS progress list (GET /api/me, AudioBooth) is enumerated from
		// position rows, so a finished state with no position never reaches a
		// client. A book with no position gets one at its end, on the ABS
		// whole-book segment, at the list's time; a book with one keeps it.
		if len(positions) == 0 {
			end := t.durationSec
			if end <= 0 {
				end = float64(row.RuntimeMin) * 60
			}
			if end > 0 {
				positions = []database.UserPosition{{UserID: st.User, BookID: t.id, SegmentID: arsSegmentID, PositionSeconds: end, UpdatedAt: ts}}
				next.LastSegmentID = arsSegmentID
				next.TotalListenedSeconds = max(next.TotalListenedSeconds, end)
			}
		}
		r.Reason = "finished per the list at " + ts.Format(time.RFC3339)
		r.Detail = &mlDecision{user: st.User, book: t.id, expect: cur, next: undo.UserStateSnapshot{State: next, Positions: positions}}
	case masterlist.StatusInProgress:
		if arsHasLocalProgress(cur) {
			return mlSkip(r, t, mlSkipLocalProgress, "the book already has local progress; the import cannot prove the list's is newer", sum)
		}
		for _, rs := range related {
			if arsHasLocalProgress(rs.snap) {
				return mlSkip(r, t, mlSkipLocalProgress, "another copy of this book ("+rs.id+") has local progress; the import cannot prove the list's is newer", sum)
			}
		}
		switch {
		case !hasTS:
			return mlSkip(r, t, mlSkipNoTimestamp, "the list gives no last-listened time for this row, and the import never stamps a position now", sum)
		case local.After(ts):
			// No progress, but a newer reset or activity: the user cleared it.
			return mlSkip(r, t, mlSkipNewerLocal, fmt.Sprintf("local activity %s (%s; a reset or a mark) is newer than the list's %s",
				local.Format(time.RFC3339), where(localBook), ts.Format(time.RFC3339)), sum)
		}
		pos, pct, ok := mlPosition(row, t.durationSec)
		if !ok {
			return mlSkip(r, t, mlSkipNoProgress, "the list gives no position, and no percent that could be applied to this book's length", sum)
		}
		next := arsCopyState(cur.State, st.User, t.id)
		next.Status = database.UserBookStatusInProgress
		next.StatusManual = false
		next.ProgressPct = pct
		next.TotalListenedSeconds = pos
		next.LastSegmentID = arsSegmentID
		next.LastActivityAt = ts
		next.FinishedAt = nil
		r.Reason = fmt.Sprintf("%d%% per the list at %s", pct, ts.Format(time.RFC3339))
		r.Detail = &mlDecision{user: st.User, book: t.id, expect: cur, next: undo.UserStateSnapshot{State: next,
			Positions: []database.UserPosition{{UserID: st.User, BookID: t.id, SegmentID: arsSegmentID, PositionSeconds: pos, UpdatedAt: ts}}}}
	default:
		// Unreachable: the list's four statuses are handled above, and a
		// row with any other status carries bad_status (matched_review).
		return mlSkip(r, t, mlSkipNoStatus, fmt.Sprintf("the list's status %q is not one the import writes", row.Status), nil)
	}
	r.Fingerprint = mlFingerprint(r, t, sum)
	return r
}

// decideElsewhere turns an unmatched row into a read-elsewhere record, unless
// one exists already.
func (d *mlDecider) decideElsewhere(base repairs.Row, st mlRowState) repairs.Row {
	r := base
	id := database.ElsewhereID(st.User, r.RowID)
	existing, err := d.lists.GetElsewhere(id)
	switch {
	case errors.Is(err, database.ErrElsewhereNotFound):
	case err != nil:
		return mlSkip(r, arsTarget{}, mlStateUnreadable, fmt.Sprintf("read the read-elsewhere record %s: %v", id, err), nil)
	default:
		return mlSkip(r, arsTarget{}, mlSkipAlreadyRecorded,
			fmt.Sprintf("already kept as a read-elsewhere record (from list %s)", existing.ListID), "recorded")
	}
	row := st.Row
	// The same book under another identifier (an earlier list gave its ASIN, this
	// one gives its ISBN): one record, not two.
	if same, err := d.elsewhere.find(d.lists, st.User, row); err != nil {
		return mlSkip(r, arsTarget{}, mlStateUnreadable, err.Error(), nil)
	} else if same != nil {
		return mlSkip(r, arsTarget{}, mlSkipAlreadyRecorded,
			fmt.Sprintf("the same book is already kept as a read-elsewhere record %s (from list %s), under another identifier", same.ID, same.ListID), "recorded:"+same.ID)
	}
	// The record keeps the list's finish time verbatim, so a future one is never
	// copied. Plan holds such a row for review (future_timestamp, see
	// mlElsewhereEligible); this guards a row replanned after the clock moved.
	if ts, ok := masterlist.ParseTime(row.FinishedAt); ok && ts.After(d.now().Add(mlFutureSlack)) {
		return mlSkip(r, arsTarget{}, mlSkipFutureTimestamp, fmt.Sprintf("the list's finish time %s is in the future; it is not kept as a read-elsewhere record", ts.Format(time.RFC3339)), nil)
	}
	rec := database.MasterListElsewhere{
		ID: id, UserID: st.User, ListID: st.ListID, UploadedBy: st.UploadedBy, Lines: slices.Clone(st.Lines),
		Title: row.Title, Authors: slices.Clone(row.Authors), ISBN10: row.ISBN10, ISBN13: row.ISBN13, ASIN: row.ASIN,
		Status: row.Status, FinishedAt: row.FinishedAt,
	}
	for _, s := range row.Sources {
		rec.Sources = append(rec.Sources, database.MasterListElsewhereSource{Source: s.Source, SourceID: s.SourceID})
	}
	for _, is := range row.Issues {
		if is.Code != "" && !slices.Contains(rec.Issues, is.Code) {
			rec.Issues = append(rec.Issues, is.Code)
		}
	}
	r.Class, r.Skipped, r.SkipReason, r.Risk = mlReadElsewhere, "", "", repairs.RiskLow
	r.Reason = "no library book carries this row's identifiers or title; it is kept as a read-elsewhere record (no book is created)"
	r.Proposed = mlProposed(row, st.User)
	r.Detail = &mlElsewhereDecision{rec: rec}
	r.Fingerprint = mlFingerprint(r, arsTarget{}, "absent:"+id)
	return r
}
