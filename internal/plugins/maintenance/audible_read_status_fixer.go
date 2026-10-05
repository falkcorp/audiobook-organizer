// file: internal/plugins/maintenance/audible_read_status_fixer.go
// version: 1.0.0
// guid: 36d6036c-05ce-48d1-9997-a65d6a8b67ce
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// audibleReadStatusFixerID is the Repairs-lane id of the Audible read-status
// import.
const audibleReadStatusFixerID = "maintenance.audible-read-status"

// Row classes (Row.Class). The two applicable ones write; every other class
// is also the row's Skipped kind, so each count of the plan is a filter of
// the rows endpoint (?class=<class>, or ?filter=skipped:<class>). A would_*
// row the framework guard holds (Doctor Who and the like, books/itunes/**)
// keeps its class and carries the guard's kind in Skipped; the applicable
// count of a class is ?class=<class>&filter=applicable.
const (
	arsWouldFinish   = "would_finish"
	arsWouldProgress = "would_progress"

	arsSkipNewerLocal      = "skipped_newer_local"
	arsSkipAlreadyFinished = "skipped_already_finished"
	arsSkipLocalProgress   = "skipped_local_progress"
	arsSkipAbandoned       = "skipped_abandoned"
	arsSkipNotStarted      = "skipped_not_started"
	arsSkipNoTimestamp     = "skipped_no_audible_timestamp"
	arsSkipTargetGone      = "skipped_target_gone"
	arsSkipUnreadable      = "error_state_unreadable"

	arsReviewAmbiguous       = "review_ambiguous"
	arsReviewRuntimeMismatch = "review_runtime_mismatch"
	arsReviewJunkTitle       = "review_junk_title"
	arsReviewSwappedTitle    = "review_swapped_title"
	arsReviewDuplicateTarget = "review_duplicate_target"

	arsUnmatchedNoCandidate    = "unmatched_no_candidate"
	arsUnmatchedAuthorMismatch = "unmatched_author_mismatch"
	arsUnmatchedDuplicateItem  = "unmatched_duplicate_item"
	arsUnmatchedInvalidItem    = "unmatched_invalid_item"
)

// Match tiers (Row.State.Tier).
const (
	arsTierASIN        = "asin"
	arsTierASINRuntime = "asin_runtime"
	arsTierTitle       = "title_author"
	arsTierTitleRT     = "title_author_runtime"
)

// arsSegmentID is the position segment an imported in-progress position is
// written on: the ABS whole-book segment, whose position is seconds into the
// whole book (handlers/abs absProgressSegmentID).
const arsSegmentID = "abs"

// arsLocalUserID is the iTunes sync user. Its finishes are pushed into the
// iTunes library as play counts (itunes/service position_sync.go), and the
// iTunes library is hands-off, so the import refuses it as a target.
const arsLocalUserID = "_local"

// arsPlanWorkers bounds the per-item pass: point reads (files, credits, user
// state) per item, I/O-bound.
const arsPlanWorkers = 8

// audibleReadStatusFixer applies the read status of an Audible library
// export to the matched books for one explicit user.
//
// MATCHING. An item matches by ASIN to exactly one target (a version-group
// primary, or the book itself when it has no group or no primary); several
// targets are settled by the one whose duration is within ±10% of Audible's
// runtime. An item no book carries the ASIN of matches by normalized title
// (a subtitle may be dropped on one side, series numbers must agree) and an
// overlapping author surname, settled the same way. A target whose duration
// is known and off by more than 10%, whose title is junk ("read by ..."),
// or that two items claim, and a title found only in an author field, are
// review rows: listed, never applied. So is anything ambiguous.
//
// ACTIONS, for the target user only (never the caller; required):
//   - Audible finished: set Finished (StatusManual, 100%, finished_at and
//     last activity = Audible's timestamp), unless the book is already
//     finished, abandoned, or has local activity (state, position or a
//     progress reset) newer than Audible's timestamp.
//   - Audible in progress: write Audible's position (scaled to the local
//     duration) and percent with Audible's timestamp, only when the user has
//     no local progress on the book at all.
//   - Not started: nothing.
//
// Nothing is ever un-finished and newer local state always wins. Each write
// is journaled first (undo.ChangeTypeUserBookStateSet) with the whole state
// and positions before and after; the apply op's revert restores them while
// they still hold what the import wrote and refuses once the user has moved
// on.
type audibleReadStatusFixer struct{ p *Plugin }

func newAudibleReadStatusFixer(p *Plugin) *audibleReadStatusFixer {
	return &audibleReadStatusFixer{p: p}
}

var _ repairs.Fixer = (*audibleReadStatusFixer)(nil)

func (f *audibleReadStatusFixer) ID() string { return audibleReadStatusFixerID }
func (f *audibleReadStatusFixer) Title() string {
	return "Audible read status import"
}
func (f *audibleReadStatusFixer) Description() string {
	return "Applies the read status of an Audible library export to the matched books for one user. Plan params " +
		"(the POST body): target_user_id (required, never defaulted) and items (the export's items: asin, title, " +
		"authors, runtime_length_min, is_finished, percent_complete, listening_status). Matches by ASIN, then by " +
		"title and author, with runtime within 10%; anything ambiguous, runtime-mismatched or junk-titled is a " +
		"review row and never applied. Finished titles are marked finished (manual, 100%, Audible's finish time) " +
		"unless the book is already finished or has newer local activity; in-progress titles get Audible's " +
		"position only when the book has no local progress. Never un-finishes anything. Undo with the apply " +
		"operation's revert, which refuses a book the user has listened to since."
}

// arsRowState is what a planned row stores (Row.State) for its re-plan.
type arsRowState struct {
	User       string   `json:"user"`
	Item       arsItem  `json:"item"`
	Target     string   `json:"target,omitempty"`
	Tier       string   `json:"tier,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

// arsDecision is what Apply writes for one row (Row.Detail).
type arsDecision struct {
	user, book   string
	expect, next undo.UserStateSnapshot
}

// arsTarget is the part of a target book a decision reads.
type arsTarget struct {
	id, title, vgid string
	primary         bool
	durationSec     float64
	gone            bool
}

// key is the target in a stable form for the fingerprint.
func (t arsTarget) key() []string {
	return []string{t.id, t.title, t.vgid, strconv.FormatBool(t.primary),
		strconv.FormatFloat(t.durationSec, 'f', -1, 64), strconv.FormatBool(t.gone)}
}

func (f *audibleReadStatusFixer) store() (OpsStore, UserReadStateStore, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	us := f.p.deps.UserReadStateStore()
	if us == nil {
		return nil, nil, fmt.Errorf("%s: the store cannot read and write user listening state with its own timestamps", audibleReadStatusFixerID)
	}
	return store, us, nil
}

// parseARSParams reads and checks the plan params: a target user that exists
// (and is not the iTunes sync user) and at least one item.
func parseARSParams(raw json.RawMessage, users UserReadStateStore) (arsParams, error) {
	var p arsParams
	if len(raw) == 0 || string(raw) == "null" {
		return p, fmt.Errorf("%s: params are required: target_user_id and the export's items", audibleReadStatusFixerID)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("%s: parse params: %w", audibleReadStatusFixerID, err)
	}
	p.TargetUserID = strings.TrimSpace(p.TargetUserID)
	if p.TargetUserID == "" {
		return p, fmt.Errorf("%s: target_user_id is required (the user whose listening state is written; it is never defaulted)", audibleReadStatusFixerID)
	}
	if p.TargetUserID == arsLocalUserID {
		return p, fmt.Errorf("%s: target_user_id %q is the iTunes sync user; its finishes are pushed into the iTunes library, which this import must not reach", audibleReadStatusFixerID, arsLocalUserID)
	}
	u, err := users.GetUserByID(p.TargetUserID)
	if err != nil {
		return p, fmt.Errorf("%s: read user %s: %w", audibleReadStatusFixerID, p.TargetUserID, err)
	}
	if u == nil {
		return p, fmt.Errorf("%s: target_user_id %s is not a user", audibleReadStatusFixerID, p.TargetUserID)
	}
	if len(p.items()) == 0 {
		return p, fmt.Errorf("%s: the export has no items", audibleReadStatusFixerID)
	}
	return p, nil
}

// Plan matches every export item against the library and decides each
// matched one against the target user's state. Read-only.
func (f *audibleReadStatusFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, us, err := f.store()
	if err != nil {
		return nil, err
	}
	params, err := parseARSParams(raw, us)
	if err != nil {
		return nil, err
	}
	cores, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", err)
	}
	lib := buildARSLibrary(cores, authors)
	items := params.items()
	ids := arsRowIDs(items)
	rows := make([]repairs.Row, len(items))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i; lib is read-only.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(items)), func(_ context.Context, i int) error {
		defer done.Add(1)
		rows[i] = f.planItem(store, us, lib, params.TargetUserID, ids[i], items[i])
		return nil
	}, registry.RunItemsOptions{
		Concurrency: arsPlanWorkers,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Audible items %d/%d", done.Load(), total) },
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, runErr
	}
	arsMarkDuplicateTargets(rows)
	return rows, nil
}

// arsRowIDs gives every item a stable row id: "asin:<ASIN>", or for an item
// with none, its folded title and authors. A repeat of an id gets "#2", "#3"
// (and is planned as unmatched_duplicate_item).
func arsRowIDs(items []arsItem) []string {
	out := make([]string, len(items))
	seen := map[string]int{}
	for i, it := range items {
		id := ""
		if a := strings.ToUpper(strings.TrimSpace(it.ASIN)); a != "" {
			id = "asin:" + a
		} else {
			sum := sha256.Sum256([]byte(arsKeyOf(it.Title).full + "\x00" + strings.Join(it.Authors, "\x00")))
			id = "noasin:" + hex.EncodeToString(sum[:])[:16]
		}
		seen[id]++
		if n := seen[id]; n > 1 {
			id += "#" + strconv.Itoa(n)
		}
		out[i] = id
	}
	return out
}

// arsMarkDuplicateTargets turns every applicable row whose target another
// applicable row also names into a review row: two Audible titles on one
// book means one of the matches is wrong.
func arsMarkDuplicateTargets(rows []repairs.Row) {
	byTarget := map[string][]int{}
	for i := range rows {
		if rows[i].Applicable() && len(rows[i].BookIDs) == 1 {
			byTarget[rows[i].BookIDs[0]] = append(byTarget[rows[i].BookIDs[0]], i)
		}
	}
	for book, idx := range byTarget {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			r := &rows[i]
			r.Class, r.Skipped = arsReviewDuplicateTarget, arsReviewDuplicateTarget
			r.SkipReason = fmt.Sprintf("%d Audible titles match book %s; at most one of them is that book", len(idx), book)
			r.Reason, r.Risk, r.Detail, r.Proposed = r.SkipReason, repairs.RiskReview, nil, nil
			r.Fingerprint = arsFingerprint(r.RowID, r.Class, r.BookIDs, "")
		}
	}
}

// planItem matches one item and decides it.
func (f *audibleReadStatusFixer) planItem(store OpsStore, us UserReadStateStore, lib *arsLibrary, user, rowID string, it arsItem) repairs.Row {
	st := arsRowState{User: user, Item: it}
	base := repairs.Row{RowID: rowID, Title: it.Title, Author: it.authorLine(), Risk: repairs.RiskReview}
	if strings.Contains(rowID, "#") {
		return arsSkipRow(base, st, arsUnmatchedDuplicateItem, "the export lists this title more than once; only the first is planned", nil)
	}
	if strings.TrimSpace(it.ASIN) == "" && strings.TrimSpace(it.Title) == "" {
		return arsSkipRow(base, st, arsUnmatchedInvalidItem, "the item has neither an ASIN nor a title", nil)
	}
	if it.status() == arsAudibleNotStarted {
		return arsSkipRow(base, st, arsSkipNotStarted, "not started on Audible; nothing to import", nil)
	}
	durations := map[string]float64{}
	duration := func(id string) (float64, error) {
		if d, ok := durations[id]; ok {
			return d, nil
		}
		d, err := arsLocalDuration(store, id, lib.books[id].Duration)
		if err != nil {
			return 0, err
		}
		durations[id] = d
		return d, nil
	}
	settle := func(cands []string) ([]string, error) {
		var ok []string
		for _, id := range cands {
			d, err := duration(id)
			if err != nil {
				return nil, err
			}
			if fits, known := arsRuntime(d, it.runtimeSeconds()); fits && known {
				ok = append(ok, id)
			}
		}
		return ok, nil
	}
	var target, tier string
	var cands []string
	if a := strings.ToUpper(strings.TrimSpace(it.ASIN)); a != "" && len(lib.byASIN[a]) > 0 {
		cands = lib.targets(lib.byASIN[a])
		tier = arsTierASIN
		if len(cands) == 1 {
			target = cands[0]
		} else {
			ok, err := settle(cands)
			if err != nil {
				return arsSkipRow(base, st, arsSkipUnreadable, err.Error(), cands)
			}
			if len(ok) != 1 {
				st.Candidates = cands
				return arsSkipRow(base, st, arsReviewAmbiguous,
					fmt.Sprintf("the ASIN is on %d primary books and %d of them match Audible's runtime", len(cands), len(ok)), cands)
			}
			target, tier = ok[0], arsTierASINRuntime
		}
	} else {
		key := arsKeyOf(it.Title)
		hits := lib.targets(lib.titleHits(key))
		var byAuthor []string
		for _, id := range hits {
			names, err := arsBookAuthorNames(store, lib, id)
			if err != nil {
				return arsSkipRow(base, st, arsSkipUnreadable, err.Error(), hits)
			}
			if arsAuthorsOverlap(names, it.Authors) {
				byAuthor = append(byAuthor, id)
			}
		}
		switch {
		case len(byAuthor) == 1:
			target, tier = byAuthor[0], arsTierTitle
		case len(byAuthor) > 1:
			ok, err := settle(byAuthor)
			if err != nil {
				return arsSkipRow(base, st, arsSkipUnreadable, err.Error(), byAuthor)
			}
			if len(ok) != 1 {
				st.Candidates = byAuthor
				return arsSkipRow(base, st, arsReviewAmbiguous,
					fmt.Sprintf("%d primary books match the title and author and %d of them match Audible's runtime", len(byAuthor), len(ok)), byAuthor)
			}
			target, tier = ok[0], arsTierTitleRT
		case len(hits) > 0:
			st.Candidates = hits
			return arsSkipRow(base, st, arsUnmatchedAuthorMismatch,
				fmt.Sprintf("%d books carry the title but none shares an author with Audible's credit", len(hits)), hits)
		default:
			// The title in an author field: a book whose title and author
			// were swapped, or whose title is junk. Review only.
			swapped := lib.targets(lib.byAuthor[key.full])
			var ok []string
			for _, id := range swapped {
				d, err := duration(id)
				if err != nil {
					return arsSkipRow(base, st, arsSkipUnreadable, err.Error(), swapped)
				}
				if fits, _ := arsRuntime(d, it.runtimeSeconds()); fits {
					ok = append(ok, id)
				}
			}
			if len(ok) > 0 {
				st.Candidates = ok
				return arsSkipRow(base, st, arsReviewSwappedTitle,
					"no book has this title, but the title is the author of a book of the right length (title and author swapped, or a junk title)", ok)
			}
			return arsSkipRow(base, st, arsUnmatchedNoCandidate, "no book carries the ASIN, the title, or the title as an author", nil)
		}
	}
	st.Target, st.Tier, st.Candidates = target, tier, cands
	b := lib.books[target]
	d, err := duration(target)
	if err != nil {
		return arsSkipRow(base, st, arsSkipUnreadable, err.Error(), []string{target})
	}
	t := arsTarget{id: target, title: b.Title, durationSec: d,
		primary: b.IsPrimaryVersion != nil && *b.IsPrimaryVersion}
	if b.VersionGroupID != nil {
		t.vgid = *b.VersionGroupID
	}
	return f.decide(us, base, st, t)
}

// arsLocalDuration is the book's duration the way ABS reads it: the sum of
// its files' durations, or the book's own duration when its files have none.
// Seconds; 0 when unknown.
func arsLocalDuration(store OpsStore, bookID string, bookDuration *int) (float64, error) {
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return 0, fmt.Errorf("read files of %s: %w", bookID, err)
	}
	total := 0.0
	for i := range files {
		total += float64(files[i].Duration)
	}
	if total > 0 {
		return total, nil
	}
	if bookDuration != nil && *bookDuration > 0 {
		return float64(*bookDuration), nil
	}
	return 0, nil
}

// arsBookAuthorNames are the book's credited author names, or its primary
// author's when it has no credit rows.
func arsBookAuthorNames(store OpsStore, lib *arsLibrary, bookID string) ([]string, error) {
	credits, err := store.GetBookAuthors(bookID)
	if err != nil {
		return nil, fmt.Errorf("read credits of %s: %w", bookID, err)
	}
	var out []string
	for _, c := range credits {
		if n := lib.authors[c.AuthorID]; n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		if b := lib.books[bookID]; b != nil && b.AuthorID != nil {
			if n := lib.authors[*b.AuthorID]; n != "" {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// arsSkipRow is a row apply never writes, of kind (its Class and Skipped).
func arsSkipRow(r repairs.Row, st arsRowState, kind, why string, books []string) repairs.Row {
	r.Class, r.Skipped, r.SkipReason, r.Reason = kind, kind, why, why
	r.BookIDs = append([]string(nil), books...)
	for _, id := range r.BookIDs {
		r.Members = append(r.Members, repairs.RowMember{BookID: id, Role: "candidate"})
	}
	r.State = arsMarshalState(st)
	r.Fingerprint = arsFingerprint(r.RowID, kind, r.BookIDs, why)
	return r
}

func arsMarshalState(st arsRowState) json.RawMessage {
	b, err := json.Marshal(st)
	if err != nil {
		// Every field is a string, number or bool: unreachable.
		return nil
	}
	return b
}

func arsFingerprint(parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		b = []byte(fmt.Sprint(parts...))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:32]
}

// decide judges one matched item against the target book and the user's
// state. Plan and Replan both end here, from the same inputs, so an
// unchanged row re-plans to the same fingerprint.
func (f *audibleReadStatusFixer) decide(us UserReadStateStore, base repairs.Row, st arsRowState, t arsTarget) repairs.Row {
	it := st.Item
	r := base
	r.BookIDs = []string{t.id}
	r.Members = []repairs.RowMember{{BookID: t.id, Title: t.title, Role: "target"}}
	r.State = arsMarshalState(st)
	r.Evidence = []string{"matched by " + strings.ReplaceAll(st.Tier, "_", " ")}
	skip := func(kind, why string, extra any) repairs.Row {
		r.Class, r.Skipped, r.SkipReason, r.Reason, r.Risk = kind, kind, why, why, repairs.RiskReview
		r.Fingerprint = arsFingerprint(r.RowID, kind, t.key(), st.Tier, it, extra)
		return r
	}
	if t.gone {
		return skip(arsSkipTargetGone, "the matched book no longer exists", nil)
	}
	fits, known := arsRuntime(t.durationSec, it.runtimeSeconds())
	if known {
		r.Evidence = append(r.Evidence, fmt.Sprintf("local %.0f min vs Audible %.0f min", t.durationSec/60, it.runtimeSeconds()/60))
	} else {
		r.Evidence = append(r.Evidence, "runtime unknown on one side")
	}
	if !fits {
		return skip(arsReviewRuntimeMismatch,
			fmt.Sprintf("the matched book runs %.0f min and Audible's edition %.0f min (more than 10%% apart): a different edition, a part, or a wrong ASIN",
				t.durationSec/60, it.runtimeSeconds()/60), nil)
	}
	if arsJunkTitle(t.title) {
		return skip(arsReviewJunkTitle, fmt.Sprintf("the matched book's title %q is not a title; confirm it is this book", t.title), nil)
	}
	cur, err := undo.ReadUserStateSnapshot(us, st.User, t.id)
	if err != nil {
		// Fail closed: an unreadable state is not "no state".
		return skip(arsSkipUnreadable, err.Error(), nil)
	}
	sum := arsStateSummary(cur)
	r.Current = arsDisplay(cur.State)
	ts, hasTS := it.timestamp()
	local := arsLocalActivity(cur)
	switch it.status() {
	case arsAudibleFinished:
		switch {
		case cur.State != nil && cur.State.Status == database.UserBookStatusFinished:
			return skip(arsSkipAlreadyFinished, "already finished here; left alone (its finish date is kept)", sum)
		case cur.State != nil && cur.State.Status == database.UserBookStatusAbandoned:
			return skip(arsSkipAbandoned, "marked abandoned here; a user's choice is never overwritten", sum)
		case !hasTS:
			return skip(arsSkipNoTimestamp, "Audible gives no finish time for this title, and the import never dates a finish now", sum)
		case local.After(ts):
			return skip(arsSkipNewerLocal, fmt.Sprintf("local activity %s is newer than Audible's finish %s", local.Format(time.RFC3339), ts.Format(time.RFC3339)), sum)
		}
		next := arsCopyState(cur.State, st.User, t.id)
		next.Status = database.UserBookStatusFinished
		next.StatusManual = true
		next.ProgressPct = 100
		finished := ts
		next.FinishedAt = &finished
		next.LastActivityAt = ts
		r.Class, r.Risk = arsWouldFinish, repairs.RiskLow
		r.Reason = "finished on Audible at " + ts.Format(time.RFC3339)
		r.Proposed = arsDisplay(next)
		r.Detail = &arsDecision{user: st.User, book: t.id, expect: cur,
			next: undo.UserStateSnapshot{State: next, Positions: cur.Positions}}
	case arsAudibleInProgress:
		switch {
		case cur.State != nil && cur.State.Status == database.UserBookStatusFinished:
			return skip(arsSkipAlreadyFinished, "finished here; never un-finished", sum)
		case cur.State != nil && cur.State.Status == database.UserBookStatusAbandoned:
			return skip(arsSkipAbandoned, "marked abandoned here; a user's choice is never overwritten", sum)
		case arsHasLocalProgress(cur):
			return skip(arsSkipLocalProgress, "the book already has local progress; the import cannot prove Audible's is newer", sum)
		case !hasTS:
			return skip(arsSkipNoTimestamp, "Audible gives no listening time for this title, and the import never stamps a position now", sum)
		case local.After(ts):
			// No progress, but a newer reset or activity: the user cleared it.
			return skip(arsSkipNewerLocal, fmt.Sprintf("local activity %s (a reset or a mark) is newer than Audible's %s", local.Format(time.RFC3339), ts.Format(time.RFC3339)), sum)
		}
		pos, pct := arsPosition(it, t.durationSec)
		next := arsCopyState(cur.State, st.User, t.id)
		next.Status = database.UserBookStatusInProgress
		next.StatusManual = false
		next.ProgressPct = pct
		next.TotalListenedSeconds = pos
		next.LastSegmentID = arsSegmentID
		next.LastActivityAt = ts
		next.FinishedAt = nil
		r.Class, r.Risk = arsWouldProgress, repairs.RiskLow
		r.Reason = fmt.Sprintf("%d%% on Audible at %s", pct, ts.Format(time.RFC3339))
		r.Proposed = arsDisplay(next)
		r.Proposed["position_seconds"] = strconv.FormatFloat(pos, 'f', 0, 64)
		r.Detail = &arsDecision{user: st.User, book: t.id, expect: cur, next: undo.UserStateSnapshot{State: next,
			Positions: []database.UserPosition{{UserID: st.User, BookID: t.id, SegmentID: arsSegmentID, PositionSeconds: pos, UpdatedAt: ts}}}}
	default:
		return skip(arsSkipNotStarted, "not started on Audible; nothing to import", nil)
	}
	r.Fingerprint = arsFingerprint(r.RowID, r.Class, t.key(), st.Tier, it, sum)
	return r
}

// arsCreditTitleRe is a title that is a narrator credit naming someone
// ("Read by Jane Doe"). IsJunkTitle counts it junk only when the name is the
// book's narrator; for a match it is never a title to trust either way, and
// the only consequence here is a review row.
var arsCreditTitleRe = regexp.MustCompile(`(?i)^\s*(?:read|narrated|performed)\s+by\s+\S`)

func arsJunkTitle(title string) bool {
	return IsJunkTitle(title) || arsCreditTitleRe.MatchString(title)
}

// arsLocalActivity is the newest local sign of the user on the book: the
// state's last activity, its progress reset, or any position's time (ABS
// merges on the newest of these, so the import must lose to all of them).
func arsLocalActivity(s undo.UserStateSnapshot) time.Time {
	var t time.Time
	if s.State != nil {
		t = s.State.LastActivityAt
		if s.State.ProgressResetAt != nil && s.State.ProgressResetAt.After(t) {
			t = *s.State.ProgressResetAt
		}
	}
	for _, p := range s.Positions {
		if p.UpdatedAt.After(t) {
			t = p.UpdatedAt
		}
	}
	return t
}

// arsHasLocalProgress: any position, or a state that is anything but an
// untouched, auto "unstarted" row (a row may exist only to hold the
// hide-from-continue-listening flag).
func arsHasLocalProgress(s undo.UserStateSnapshot) bool {
	if len(s.Positions) > 0 {
		return true
	}
	st := s.State
	if st == nil {
		return false
	}
	untouched := (st.Status == "" || st.Status == database.UserBookStatusUnstarted) &&
		!st.StatusManual && st.ProgressPct == 0 && st.TotalListenedSeconds == 0
	return !untouched
}

// arsPosition is the in-progress position to write (seconds into the local
// copy) and its percent. Audible's percent is applied to the local duration,
// since the local edition's length may differ; with no local duration,
// Audible's own runtime minus its time remaining.
func arsPosition(it arsItem, localSec float64) (float64, int) {
	pct := min(max(it.percent(), 0), 99)
	pos := 0.0
	switch {
	case localSec > 0:
		pos = localSec * pct / 100
	case it.Listening != nil && it.Listening.TimeRemaining != nil && it.runtimeSeconds() > 0:
		pos = max(it.runtimeSeconds()-float64(*it.Listening.TimeRemaining), 0)
	default:
		pos = it.runtimeSeconds() * pct / 100
	}
	return float64(int64(pos)), int(pct)
}

// arsCopyState starts the new state from the stored row (read-modify-write:
// the hide flag and the reset tombstone are the user's), or a fresh one.
func arsCopyState(s *database.UserBookState, user, book string) *database.UserBookState {
	if s == nil {
		return &database.UserBookState{UserID: user, BookID: book}
	}
	c := *s
	c.ProgressResetPositions = slices.Clone(s.ProgressResetPositions)
	return &c
}

// arsStateSummary is the part of the user's state a decision reads, in a
// stable form for the fingerprint.
func arsStateSummary(s undo.UserStateSnapshot) []string {
	out := []string{"absent"}
	if st := s.State; st != nil {
		out = []string{st.Status, strconv.FormatBool(st.StatusManual), strconv.Itoa(st.ProgressPct),
			strconv.FormatInt(st.LastActivityAt.UnixNano(), 10), arsTimeKey(st.FinishedAt), arsTimeKey(st.ProgressResetAt),
			strconv.FormatFloat(st.TotalListenedSeconds, 'f', -1, 64), st.LastSegmentID,
			strconv.FormatBool(st.HideFromContinueListening), strconv.FormatInt(st.UpdatedAt.UnixNano(), 10)}
	}
	pos := make([]string, 0, len(s.Positions))
	for _, p := range s.Positions {
		pos = append(pos, fmt.Sprintf("%s=%v@%d", p.SegmentID, p.PositionSeconds, p.UpdatedAt.UnixNano()))
	}
	sort.Strings(pos)
	return append(out, pos...)
}

func arsTimeKey(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func arsDisplay(s *database.UserBookState) map[string]string {
	if s == nil {
		return map[string]string{"status": "(none)"}
	}
	out := map[string]string{"status": s.Status, "progress_pct": strconv.Itoa(s.ProgressPct),
		"status_manual": strconv.FormatBool(s.StatusManual)}
	if !s.LastActivityAt.IsZero() {
		out["last_activity_at"] = s.LastActivityAt.UTC().Format(time.RFC3339)
	}
	if s.FinishedAt != nil {
		out["finished_at"] = s.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// Replan re-reads the target book and the user's state and decides the row
// again from the item the plan stored. Only applicable rows are re-planned.
func (f *audibleReadStatusFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store, us, err := f.store()
	if err != nil {
		return repairs.Row{}, err
	}
	var st arsRowState
	if err := json.Unmarshal(planned.State, &st); err != nil {
		return repairs.Row{}, fmt.Errorf("row %s: decode plan state: %w", planned.RowID, err)
	}
	if st.Target == "" || st.User == "" {
		return repairs.Row{}, fmt.Errorf("row %s: the plan stored no target", planned.RowID)
	}
	base := repairs.Row{RowID: planned.RowID, Title: st.Item.Title, Author: st.Item.authorLine(), Risk: repairs.RiskReview}
	b, err := store.GetBookByID(st.Target)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", st.Target, err)
	}
	t := arsTarget{id: st.Target}
	if b == nil || b.IsSoftDeleted() {
		t.gone = true
		return f.decide(us, base, st, t), nil
	}
	t.title = b.Title
	t.primary = b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
	if b.VersionGroupID != nil {
		t.vgid = *b.VersionGroupID
	}
	if t.durationSec, err = arsLocalDuration(store, b.ID, b.Duration); err != nil {
		return repairs.Row{}, err
	}
	return f.decide(us, base, st, t), nil
}

// Apply writes the planned state through the Writer, which re-reads it first
// and refuses (changed_since_plan) when the user's state moved.
func (f *audibleReadStatusFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*arsDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", audibleReadStatusFixerID, fresh.RowID)
	}
	if err := w.SetUserState(d.user, d.book, d.expect, d.next); err != nil {
		if errors.Is(err, repairs.ErrChangedSincePlan) || errors.Is(err, repairs.ErrPartiallyApplied) {
			return err
		}
		return fmt.Errorf("%s: row %s: %w", audibleReadStatusFixerID, fresh.RowID, err)
	}
	return nil
}
