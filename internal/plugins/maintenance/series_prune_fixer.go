// file: internal/plugins/maintenance/series_prune_fixer.go
// version: 1.1.0
// guid: 3aa1a5a7-404c-42f9-9a8d-b6213e7b09f2
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// seriesPruneFixerID is the Repairs-lane id of the series prune fixer. It is
// the id of the whole-library op it applies one row of (dedup.series-prune,
// still scheduled nightly as the series_prune task); the fixer replaces the
// old Series tab's "Prune Series" preview and confirm.
const seriesPruneFixerID = "dedup.series-prune"

// Row classes and skip kinds of a series prune row.
const (
	spClassDuplicate = "duplicate"
	spClassOrphan    = "orphan"

	// spSkipHeld: an empty series row held for an undo (database.SeriesHeld).
	spSkipHeld = "held_for_undo"
	// spSkipReferenced: some book, in any state (trashed, non-primary),
	// still names a series the row would delete.
	spSkipReferenced = "still_referenced"
	// spSkipCountFailed: a member's book count could not be read, so no
	// canonical can be chosen (dedup.VoteSeriesCanonical).
	spSkipCountFailed = "count_failed"
	// spSkipResolved: a re-plan no longer finds the candidate (the group is
	// no longer a duplicate group, or the series is gone or has books).
	spSkipResolved = "resolved"
)

// seriesPruneFixer merges duplicate series and deletes empty ones, one row at
// a time, through the cores the nightly op runs (internal/dedup
// series_prune_core.go).
//
// Two row kinds, chosen by dedup.PlanSeriesPrune with the op's EXECUTE rules
// (not the old HTTP preview's):
//   - duplicate: two or more series with the same normalised name and the
//     same author; held rows are in no group. Applying moves every book
//     (non-primary versions included) of each merged-away series onto the
//     canonical one (most listed books, then lowest id) and deletes the
//     emptied rows. Books ARE changed (their series link only).
//   - orphan: a series in no duplicate group that lists no books. Applying
//     deletes the series row; no book is changed.
//
// A row is skipped (never applicable) when a series it would delete is still
// referenced by any book in any state (the unfiltered count,
// database.SeriesRefCounts: a trashed book cannot be repointed and would be
// left naming a series that no longer exists), when an orphan is held for an
// undo, when a member's book count cannot be read, or when a member series is
// named for Doctor Who / Big Finish / Torchwood (repairs.SkipOwnerManual, by
// series name: an empty-series row has no books for the framework's book
// guard to read, so the row carries the guard itself).
//
// CONCURRENCY. Every series write the fixer makes runs under repairs.apply,
// which declares the series write-set, so the dispatcher never runs it beside
// dedup.series-prune, -normalize, -merge or entities.series-rename. A book
// write from any other op can still land between the re-plan and a delete;
// each delete therefore re-counts the references to its one series
// immediately before the write (spWriteStore.DeleteSeries) and refuses on
// any. What is left is the moment between that count and the delete itself:
// book writes take a per-book lock, not the series lock DeleteSeries takes,
// so no store-level guarded delete could close it without making every book
// write contend on the series lock.
//
// JOURNAL FIRST. Each series_merge and series_delete row is journaled before
// the write it describes (Writer.JournalStep), and voided when the write is
// refused, so a run stopped between the two never leaves a write with no
// record. Because a book's series_merge row is journaled before its Modify,
// that book's history batch is marked apply_op_journaled, and "undo last
// apply" refuses it in favour of the operation (whose series rows are
// record-only; see UNDO below).
//
// RESUME. A merge cut off part-way (a lost lease, a cancel) is NOT resumed:
// the re-plan sees the books it already moved on the kept series, the
// fingerprint differs, and the row settles changed_since_plan. A fresh plan
// offers what is left (the emptied copy as a merge or an empty series).
//
// Written Evaluate-style for the ops v3 port: evaluate decides ONE candidate
// from inputs read before it, with no writer; Plan and Replan differ only in
// how many candidates they read. Apply writes what the fresh row's Detail
// says and nothing it decides itself.
//
// UNDO. Every write is journaled under the apply op with the change types
// the op writes (series_merge per book, series_delete per row), and every
// book's series link also gets a metadata-history row. None of it brings a
// deleted series back: undo/restorable.go lists series_merge and
// series_delete as record-only (CreateSeries mints a new id), and "undo last
// apply" refuses to link a book to a series that no longer exists
// (metafetch seriesGone). So an apply is recorded, not undoable, and the
// description says nothing about undo.
type seriesPruneFixer struct{ p *Plugin }

func newSeriesPruneFixer(p *Plugin) *seriesPruneFixer { return &seriesPruneFixer{p: p} }

var _ repairs.Fixer = (*seriesPruneFixer)(nil)

func (f *seriesPruneFixer) ID() string    { return seriesPruneFixerID }
func (f *seriesPruneFixer) Title() string { return "Prune series" }
func (f *seriesPruneFixer) Description() string {
	return "Series records that are duplicates of each other, and series that no book belongs to. " +
		"Applying a duplicate row moves its books to the kept series and removes the empty copies; " +
		"applying an empty-series row removes the empty series."
}

// spState is what Replan needs from plan time: which candidate the row is.
type spState struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	AuthorID  int    `json:"author_id,omitempty"`
	SeriesID  int    `json:"series_id,omitempty"`
	MemberIDs []int  `json:"member_ids,omitempty"`
}

// spDecision is what Apply writes for one fresh row, carried in Row.Detail.
type spDecision struct {
	kind string
	// duplicate
	keep    database.Series
	away    []database.Series
	refs    map[int]int
	members database.SeriesBooksMap
	// orphan
	orphan   database.Series
	orphanRf int
}

// spReader is what evaluate reads beyond its inputs: the per-member listed
// book counts of the canonical vote, and author names for display.
type spReader interface {
	dedup.SeriesPruneReader
	GetAuthorByID(id int) (*database.Author, error)
}

// spInputs are every library-wide read a decision is made from, read once
// before evaluate runs.
type spInputs struct {
	reader spReader
	// refs are the UNFILTERED reference counts (any book state).
	refs map[int]int
	// members is the AllVersions membership of every duplicate-group member.
	members database.SeriesBooksMap
}

// spLoad reads the series list, the listed and unfiltered counts, and the
// candidates. Both counts fail CLOSED: a store that cannot answer the
// unfiltered question plans nothing rather than offering deletes decided on a
// filtered count.
func (f *seriesPruneFixer) spLoad() (OpsStore, []dedup.SeriesPruneCandidate, map[int]int, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, nil, nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllSeries()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list series: %w", err)
	}
	listed, err := store.GetAllSeriesBookCounts()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("count listed books per series: %w", err)
	}
	refs, err := database.SeriesRefCounts(store)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("refusing to plan series deletes without unfiltered reference counts: %w", err)
	}
	return store, dedup.PlanSeriesPrune(all, listed), refs, nil
}

// spMembers reads the AllVersions membership of every duplicate member of
// cands, in one pass; fail closed like the counts.
func spMembers(store OpsStore, cands []dedup.SeriesPruneCandidate) (database.SeriesBooksMap, error) {
	var ids []int
	for _, c := range cands {
		if c.Kind != dedup.SeriesPruneDuplicate {
			continue
		}
		for _, s := range c.Members {
			ids = append(ids, s.ID)
		}
	}
	m, err := database.SeriesMembershipAllVersions(store, ids)
	if err != nil {
		return nil, fmt.Errorf("refusing to plan series merges without series membership: %w", err)
	}
	return m, nil
}

// Plan lists every candidate and decides each with evaluate.
func (f *seriesPruneFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, cands, refs, err := f.spLoad()
	if err != nil {
		return nil, err
	}
	members, err := spMembers(store, cands)
	if err != nil {
		return nil, err
	}
	in := &spInputs{reader: store, refs: refs, members: members}
	rows := make([]repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i, and reads in.members
	// without writing it (evaluate copies what it keeps). Every series is in
	// at most one candidate (dedup.PlanSeriesPrune), so no two workers decide
	// the same series.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(ictx context.Context, i int) error {
		defer done.Add(1)
		row, err := f.evaluate(ictx, in, cands[i])
		if err != nil {
			row = spErrorRow(cands[i], err)
		}
		rows[i] = row
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Series prune candidates %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if rep != nil {
		var dup, orphan, applicable int
		for _, r := range rows {
			switch r.Class {
			case spClassDuplicate:
				dup++
			case spClassOrphan:
				orphan++
			}
			if r.Applicable() {
				applicable++
			}
		}
		_ = rep.Log(slog.LevelInfo, fmt.Sprintf("%s: %d duplicate groups and %d empty series; %d rows applicable",
			seriesPruneFixerID, dup, orphan, applicable))
	}
	return rows, nil
}

// Replan re-reads the library and decides the planned candidate with the
// same evaluate. A candidate the fresh plan no longer has comes back as a
// resolved row (a different fingerprint: changed_since_plan).
func (f *seriesPruneFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	var st spState
	if err := json.Unmarshal(planned.State, &st); err != nil {
		return repairs.Row{}, fmt.Errorf("%s: row %s: unreadable plan state: %w", seriesPruneFixerID, planned.RowID, err)
	}
	snap, err := f.snapshotFor(ctx)
	if err != nil {
		return repairs.Row{}, err
	}
	var found *dedup.SeriesPruneCandidate
	switch st.Kind {
	case dedup.SeriesPruneDuplicate:
		found = snap.dups[dedup.SeriesGroupKey{Name: st.Name, AuthorID: st.AuthorID}]
	case dedup.SeriesPruneOrphan:
		found = snap.orphans[st.SeriesID]
	}
	if found == nil {
		return spResolvedRow(planned), nil
	}
	// Membership is read per row, fresh: it is one index read per member
	// series, and it is what the merge moves.
	members, err := spMembers(snap.store, []dedup.SeriesPruneCandidate{*found})
	if err != nil {
		return repairs.Row{}, err
	}
	row, err := f.evaluate(ctx, &spInputs{reader: snap.store, refs: snap.refs, members: members}, *found)
	if err != nil {
		return repairs.Row{}, err
	}
	if row.RowID != planned.RowID {
		return repairs.Row{}, fmt.Errorf("%s: candidate re-planned as %s, not %s", seriesPruneFixerID, row.RowID, planned.RowID)
	}
	return row, nil
}

// spSnapshot is the library-wide read a re-plan decides from: the series
// list, the listed and unfiltered counts (spLoad) and the candidates indexed
// by row. Read-only once built.
type spSnapshot struct {
	store   OpsStore
	refs    map[int]int
	dups    map[dedup.SeriesGroupKey]*dedup.SeriesPruneCandidate
	orphans map[int]*dedup.SeriesPruneCandidate
}

func (f *seriesPruneFixer) loadSnapshot() (*spSnapshot, error) {
	store, cands, refs, err := f.spLoad()
	if err != nil {
		return nil, err
	}
	snap := &spSnapshot{store: store, refs: refs,
		dups: map[dedup.SeriesGroupKey]*dedup.SeriesPruneCandidate{}, orphans: map[int]*dedup.SeriesPruneCandidate{}}
	for i := range cands {
		c := &cands[i]
		if c.Kind == dedup.SeriesPruneOrphan {
			snap.orphans[c.Members[0].ID] = c
		} else {
			snap.dups[c.Key] = c
		}
	}
	return snap, nil
}

// spApplySession is one apply run's state (repairs.ApplyScoped).
//
// The snapshot is read ONCE per run, on the first re-plan, instead of three
// library-wide reads (series list, listed counts, reference counts) per row.
// That is safe against the run's own writes because its rows are
// series-disjoint (dedup.PlanSeriesPrune puts every series in at most one
// candidate), so no row's write changes another row's inputs. Writers outside
// the run are caught at apply: the fresh per-row membership, the existence
// and hold re-check of every series (spSeriesExist), the book checks before
// and under each write lock, and the per-series reference re-count before
// each delete. Holding the snapshot is also what makes a run whose first
// re-plan cannot read the counts fail every row (fail closed) rather than
// plan from a filtered count.
//
// wrote is set by any row that changed something; end drops the cached
// series list and the duplicates cache once, also for a run that was
// cancelled or lost its lease after a row wrote.
type spApplySession struct {
	once  sync.Once
	snap  *spSnapshot
	err   error
	wrote atomic.Bool
}

type spSessionKey struct{}

var _ repairs.ApplyScoped = (*seriesPruneFixer)(nil)

// BeginApply implements repairs.ApplyScoped.
func (f *seriesPruneFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	s := &spApplySession{}
	return context.WithValue(ctx, spSessionKey{}, s), func() {
		if s.wrote.Load() {
			f.invalidateCaches()
		}
	}
}

func spSessionOf(ctx context.Context) *spApplySession {
	s, _ := ctx.Value(spSessionKey{}).(*spApplySession)
	return s
}

// snapshotFor returns the run's snapshot, or a fresh one outside a run.
func (f *seriesPruneFixer) snapshotFor(ctx context.Context) (*spSnapshot, error) {
	s := spSessionOf(ctx)
	if s == nil {
		return f.loadSnapshot()
	}
	s.once.Do(func() { s.snap, s.err = f.loadSnapshot() })
	return s.snap, s.err
}

// spRowID is a candidate's stable row id: a duplicate group by its key (not
// its member list, which a re-plan compares through the fingerprint), an
// orphan by its series id.
func spRowID(c dedup.SeriesPruneCandidate) string {
	if c.Kind == dedup.SeriesPruneOrphan {
		return "series-empty-" + strconv.Itoa(c.Members[0].ID)
	}
	sum := sha256.Sum256([]byte(c.Key.Name + "\x00" + strconv.Itoa(c.Key.AuthorID)))
	return "series-dup-" + hex.EncodeToString(sum[:])[:16]
}

// spSeriesText names a series for display.
func spSeriesText(s database.Series, books int) string {
	return fmt.Sprintf("%q #%d (%d book%s)", s.Name, s.ID, books, plural(books))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// evaluate decides one candidate from in, reading only the listed book
// counts the canonical vote needs (and an author name). It never writes. An
// error is returned only when ctx is done or the row's state cannot be
// encoded.
func (f *seriesPruneFixer) evaluate(ctx context.Context, in *spInputs, c dedup.SeriesPruneCandidate) (repairs.Row, error) {
	if err := ctx.Err(); err != nil {
		return repairs.Row{}, err
	}
	if c.Kind == dedup.SeriesPruneOrphan {
		return f.evaluateOrphan(in, c)
	}
	return f.evaluateDuplicate(in, c)
}

// evaluateOrphan decides one series that lists no books.
func (f *seriesPruneFixer) evaluateOrphan(in *spInputs, c dedup.SeriesPruneCandidate) (repairs.Row, error) {
	s := c.Members[0]
	refs := in.refs[s.ID]
	held := database.SeriesHeld(s)
	st, err := json.Marshal(spState{Kind: c.Kind, SeriesID: s.ID})
	if err != nil {
		return repairs.Row{}, fmt.Errorf("encode row state of series %d: %w", s.ID, err)
	}
	r := repairs.Row{
		RowID:    spRowID(c),
		BookIDs:  []string{},
		Title:    fmt.Sprintf("Delete empty series %q", s.Name),
		Class:    spClassOrphan,
		Current:  map[string]string{"series": spSeriesText(s, 0)},
		Proposed: map[string]string{"series": "(removed)"},
		Reason:   "No book is listed in this series, and nothing else refers to it.",
		Risk:     repairs.RiskReview,
		Evidence: []string{
			fmt.Sprintf("series #%d lists 0 books", s.ID),
			fmt.Sprintf("%d book row(s) in any state (trashed, other versions) name it", refs),
		},
		State: st,
	}
	r.Author = f.authorName(in, s.AuthorID)
	franchise := spFranchiseName(c.Members)
	switch {
	case franchise != "":
		r.Skipped, r.SkipReason = repairs.SkipOwnerManual, "Doctor Who / Big Finish / Torchwood"
		r.Reason = fmt.Sprintf("Series %q is Doctor Who / Big Finish / Torchwood; the owner prunes these by hand.", franchise)
	case refs > 0:
		r.Skipped, r.SkipReason = spSkipReferenced, "still referenced"
		r.Reason = fmt.Sprintf("Series #%d lists no books, but %d book row(s) (trashed, or other versions) still name it; "+
			"removing it would leave them naming a series that no longer exists.", s.ID, refs)
	case held:
		r.Skipped, r.SkipReason = spSkipHeld, "held for undo"
		r.Reason = fmt.Sprintf("Series #%d is kept by %s so an undo can link its books back.", s.ID, s.HeldBy)
	}
	if r.Applicable() {
		r.Detail = &spDecision{kind: c.Kind, orphan: s, orphanRf: refs}
	}
	r.Fingerprint = spFingerprint(r, fmt.Sprintf("orphan|%d|%s|%t|%d", s.ID, s.Name, held, refs))
	return r, nil
}

// evaluateDuplicate decides one duplicate group.
func (f *seriesPruneFixer) evaluateDuplicate(in *spInputs, c dedup.SeriesPruneCandidate) (repairs.Row, error) {
	ids := make([]int, len(c.Members))
	for i, s := range c.Members {
		ids[i] = s.ID
	}
	st, err := json.Marshal(spState{Kind: c.Kind, Name: c.Key.Name, AuthorID: c.Key.AuthorID, MemberIDs: ids})
	if err != nil {
		return repairs.Row{}, fmt.Errorf("encode row state of series group %v: %w", ids, err)
	}
	r := repairs.Row{
		RowID:   spRowID(c),
		BookIDs: []string{},
		Class:   spClassDuplicate,
		Risk:    repairs.RiskReview,
		State:   st,
	}
	if c.Key.AuthorID != 0 {
		aid := c.Key.AuthorID
		r.Author = f.authorName(in, &aid)
	}
	vote, err := dedup.VoteSeriesCanonical(in.reader, c.Members)
	if err != nil {
		r.Title = fmt.Sprintf("Merge %d duplicate series named %q", len(c.Members), c.Members[0].Name)
		r.Skipped, r.SkipReason, r.Reason = spSkipCountFailed, err.Error(), err.Error()
		r.Fingerprint = spFingerprint(r, "count_failed|"+intsText(ids))
		return r, nil
	}
	keep := c.Members[vote.CanonicalIdx]
	var (
		away     []database.Series
		awayText []string
		fp       strings.Builder
		stranded []string
	)
	refs := map[int]int{}
	members := database.SeriesBooksMap{}
	total := 0
	fmt.Fprintf(&fp, "dup|keep=%d", keep.ID)
	for i, s := range c.Members {
		books := in.members[s.ID]
		// Copied: Apply's merge moves books in its own map (Move), and the
		// planning map is shared by every worker.
		members[s.ID] = append([]database.BookCore(nil), books...)
		refs[s.ID] = in.refs[s.ID]
		total += vote.CoreCounts[i]
		bookIDs := make([]string, len(books))
		for j, b := range books {
			bookIDs[j] = b.ID
		}
		sort.Strings(bookIDs)
		fmt.Fprintf(&fp, "|%d:%s:%t:%d:%d:%s", s.ID, s.Name, database.SeriesHeld(s), vote.CoreCounts[i], in.refs[s.ID],
			strings.Join(bookIDs, ","))
		role := "canonical"
		if i != vote.CanonicalIdx {
			role = "merge"
			away = append(away, s)
			awayText = append(awayText, spSeriesText(s, vote.CoreCounts[i]))
			if gap := in.refs[s.ID] - len(books); gap > 0 {
				stranded = append(stranded, fmt.Sprintf("series #%d: %d book row(s) that cannot be moved (trashed, or not readable) still name it", s.ID, gap))
			}
		}
		for _, b := range books {
			r.BookIDs = append(r.BookIDs, b.ID)
			r.Members = append(r.Members, repairs.RowMember{BookID: b.ID, Title: b.Title, Role: role})
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("series #%d %q (%s): %d listed book(s), %d book row(s) including other versions, %d reference(s) in any state",
			s.ID, s.Name, role, vote.CoreCounts[i], len(books), in.refs[s.ID]))
	}
	sort.Strings(r.BookIDs)
	r.Title = fmt.Sprintf("Merge %d duplicate series into %q", len(away), keep.Name)
	r.Current = map[string]string{
		"kept":        spSeriesText(keep, vote.CoreCounts[vote.CanonicalIdx]),
		"merged away": strings.Join(awayText, "; "),
	}
	r.Proposed = map[string]string{"kept": spSeriesText(keep, total), "merged away": "(removed)"}
	r.Reason = fmt.Sprintf("%d series share the name %q and the same author. The one with the most books (then the lowest id) "+
		"is kept; the books of the others move to it and the emptied copies are removed.", len(c.Members), keep.Name)
	switch franchise := spFranchiseName(c.Members); {
	case franchise != "":
		r.Skipped, r.SkipReason = repairs.SkipOwnerManual, "Doctor Who / Big Finish / Torchwood"
		r.Reason = fmt.Sprintf("Series %q is Doctor Who / Big Finish / Torchwood; the owner merges these by hand.", franchise)
	case len(stranded) > 0:
		r.Skipped, r.SkipReason = spSkipReferenced, "still referenced"
		r.Reason = strings.Join(stranded, "; ") + "; removing it would leave them naming a series that no longer exists."
	}
	if r.Applicable() {
		r.Detail = &spDecision{kind: c.Kind, keep: keep, away: away, refs: refs, members: members}
	}
	r.Fingerprint = spFingerprint(r, fp.String())
	return r, nil
}

// spFranchiseName returns the name of the first member series named for
// Doctor Who / Big Finish / Torchwood (the framework guard's series-name rule,
// applygate.IsOwnerManualOnly), or "". The framework guard reads a row's
// books; an empty-series row (and an all-empty duplicate pair) has none, so
// without this a Doctor Who series row would reach apply unguarded.
func spFranchiseName(members []database.Series) string {
	for _, s := range members {
		if applygate.IsOwnerManualOnly("", s.Name) {
			return s.Name
		}
	}
	return ""
}

// authorName returns the author row's name, or "" when there is none or it
// cannot be read (display only; not part of any decision).
func (f *seriesPruneFixer) authorName(in *spInputs, id *int) string {
	if id == nil || *id == 0 {
		return ""
	}
	a, err := in.reader.GetAuthorByID(*id)
	if err != nil || a == nil {
		return ""
	}
	return a.Name
}

// spFingerprint hashes the row's identity, its skip and every decision input
// (extra).
func spFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

func intsText(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

// spErrorRow is a candidate Plan could not decide.
func spErrorRow(c dedup.SeriesPruneCandidate, err error) repairs.Row {
	class := spClassDuplicate
	if c.Kind == dedup.SeriesPruneOrphan {
		class = spClassOrphan
	}
	r := repairs.Row{RowID: spRowID(c), BookIDs: []string{}, Title: c.Members[0].Name, Class: class,
		Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	r.Fingerprint = spFingerprint(r, "error")
	return r
}

// spResolvedRow is the re-plan of a candidate the library no longer has.
func spResolvedRow(planned repairs.Row) repairs.Row {
	why := "The series is gone, or now lists books."
	if planned.Class == spClassDuplicate {
		why = "These series are no longer duplicates of each other (merged, renamed or removed)."
	}
	r := repairs.Row{RowID: planned.RowID, BookIDs: []string{}, Title: planned.Title, Class: planned.Class,
		Skipped: spSkipResolved, SkipReason: "no longer a candidate", Reason: why, Risk: planned.Risk, State: planned.State}
	r.Fingerprint = spFingerprint(r, "resolved")
	return r
}

// spWriteStore routes the cores' writes through the Repairs Writer: each
// repoint through Writer.Modify (fenced, lease-renewing, metadata history
// recorded), and each series delete behind a fresh reference count. Both run
// inside the ledger's Writer.JournalStep, which renews the lease (and checks
// the run's fence) before journaling, so a stopped or lease-lost run neither
// journals nor writes anything more.
type spWriteStore struct {
	store OpsStore
	w     *repairs.Writer
	// from is the series each book must still hold when it is repointed.
	from map[string]int
}

func (s spWriteStore) GetBookByID(id string) (*database.Book, error) { return s.store.GetBookByID(id) }

func (s spWriteStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	from, ok := s.from[id]
	if !ok {
		return nil, fmt.Errorf("%w: book %s is not in the planned membership", repairs.ErrChangedSincePlan, id)
	}
	var now *int
	if _, err := s.w.Modify(id, func(b *database.Book) error {
		// Under the write lock: the book must still be where the plan saw
		// it. The op has no such check; it reads membership a moment
		// earlier in the same run.
		if b.SeriesID == nil || *b.SeriesID != from {
			return fmt.Errorf("%w: book %s is no longer in series %d", repairs.ErrChangedSincePlan, id, from)
		}
		if err := fn(b); err != nil {
			return err
		}
		now = copyIntPtr(b.SeriesID)
		return nil
	}); err != nil {
		return nil, err
	}
	return &database.Book{ID: id, SeriesID: now}, nil
}

// DeleteSeries deletes series id only when NOTHING references it right now.
// The row was decided on reference counts read at the re-plan (the run's
// snapshot); a book another op pointed at the series since then would be
// stranded by the delete. So the one series is re-counted, unfiltered
// (trashed and non-primary rows included), immediately before the write: one
// index read, not a library scan (database.SeriesRefCount). A count that
// cannot be read refuses (fail closed). The window left is the count-to-delete
// moment; see the type comment for why no lock closes it.
func (s spWriteStore) DeleteSeries(id int) error {
	n, err := database.SeriesRefCount(s.store, id)
	if err != nil {
		return fmt.Errorf("re-count references to series %d before deleting it: %w", id, err)
	}
	if n != 0 {
		return fmt.Errorf("%w: %d book row(s) now name series %d", repairs.ErrChangedSincePlan, n, id)
	}
	return s.store.DeleteSeries(id)
}

// spJournalFirst is the fixer's ledger: every row is journaled under the
// apply op BEFORE the write it describes (Writer.JournalStep), and voided if
// the write is refused. A row that cannot be journaled is never written
// (repairs.ErrNotJournaled), so recErr is always nil here.
func spJournalFirst(w *repairs.Writer) dedup.SeriesPruneLedger {
	return func(c *database.OperationChange, write func() error) (error, error) {
		return w.JournalStep(c.BookID, repairs.UndoEntry{ChangeType: c.ChangeType, Field: c.FieldName, Old: c.OldValue, New: c.NewValue}, write), nil
	}
}

// Apply writes one fresh row through the extracted cores.
func (f *seriesPruneFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*spDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", seriesPruneFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	// Inside an apply run the caches are dropped once, when it ends
	// (BeginApply); a direct call drops them after its own write.
	wrote := func() {
		if s := spSessionOf(ctx); s != nil {
			s.wrote.Store(true)
			return
		}
		f.invalidateCaches()
	}
	if d.kind == dedup.SeriesPruneOrphan {
		return f.applyOrphan(w, store, d, wrote)
	}
	return f.applyMerge(w, store, d, wrote)
}

// spSeriesExist reports ErrChangedSincePlan unless every series still exists,
// and, for each series in deleted (the ones the row removes), unless it is
// still unheld. A delete of a missing series succeeds in the store, so without
// the existence check a second apply of the same row would journal a delete it
// never made, and a merge whose kept series is gone would point books at a
// series that no longer exists. A series held for an undo since the re-plan
// is never merged away: its books are not moved and nothing is deleted.
func spSeriesExist(store OpsStore, keep []database.Series, deleted ...database.Series) error {
	for i, s := range append(append([]database.Series(nil), keep...), deleted...) {
		got, err := store.GetSeriesByID(s.ID)
		if err != nil {
			return fmt.Errorf("read series %d: %w", s.ID, err)
		}
		if got == nil {
			return fmt.Errorf("%w: series %d is gone", repairs.ErrChangedSincePlan, s.ID)
		}
		if i >= len(keep) && database.SeriesHeld(*got) {
			return fmt.Errorf("%w: series %d is now held by %s for an undo", repairs.ErrChangedSincePlan, s.ID, got.HeldBy)
		}
	}
	return nil
}

func (f *seriesPruneFixer) applyOrphan(w *repairs.Writer, store OpsStore, d *spDecision, wrote func()) error {
	if err := spSeriesExist(store, nil, d.orphan); err != nil {
		return err
	}
	res := dedup.DeleteOrphanSeries(spWriteStore{store: store, w: w}, d.orphan, d.orphanRf, spJournalFirst(w))
	switch {
	case res.Referenced, res.Held:
		// evaluate never hands Apply such a row; the core's guard stands
		// anyway.
		return fmt.Errorf("%w: series %d is referenced or held", repairs.ErrChangedSincePlan, d.orphan.ID)
	case res.Err != nil:
		// The write was not made. A fence, a lost lease and a change since
		// the plan (a new reference, a hold) are all carried by WriteErr.
		if errors.Is(res.WriteErr, database.ErrSeriesHeld) {
			return fmt.Errorf("%w: %w", repairs.ErrChangedSincePlan, res.Err)
		}
		return errors.Join(res.WriteErr, res.Err)
	}
	wrote()
	if res.Unrecorded != nil {
		// Unreachable with the journal-first ledger (a row it cannot journal
		// is never written); kept so a ledger change cannot drop it silently.
		return fmt.Errorf("%w: series %d removed but not journaled: %w", repairs.ErrPartiallyApplied, d.orphan.ID, res.Unrecorded)
	}
	return nil
}

func (f *seriesPruneFixer) applyMerge(w *repairs.Writer, store OpsStore, d *spDecision, wrote func()) error {
	if err := spSeriesExist(store, []database.Series{d.keep}, d.away...); err != nil {
		return err
	}
	from := map[string]int{}
	for _, s := range d.away {
		for _, b := range d.members[s.ID] {
			from[b.ID] = s.ID
		}
	}
	// Every book must still be where the row says before the first write, so
	// a book moved since the re-plan refuses the row whole instead of leaving
	// it half-merged. The same check runs again under each book's write lock
	// (spWriteStore.ModifyBook) for a move that lands after this read.
	for id, sid := range from {
		b, err := store.GetBookByID(id)
		if err != nil {
			return fmt.Errorf("read book %s: %w", id, err)
		}
		if b == nil || b.SeriesID == nil || *b.SeriesID != sid {
			return fmt.Errorf("%w: book %s is no longer in series %d", repairs.ErrChangedSincePlan, id, sid)
		}
	}
	res := dedup.MergeSeriesGroup(spWriteStore{store: store, w: w, from: from}, d.keep, d.away, d.refs, d.members, spJournalFirst(w))
	moved := res.Repointed > 0 || res.Merged > 0
	if moved {
		wrote()
	}
	if len(res.Errors) == 0 && len(res.Unrecorded) == 0 {
		return nil
	}
	errs := make([]error, 0, 2+len(res.WriteErrs)+len(res.Unrecorded))
	// A stopped run (fence) or a lost lease is reported as such: the engine
	// checks them before partial. Such a row is not resumed as such: its
	// re-plan sees the books already moved and settles changed_since_plan
	// (see RESUME on seriesPruneFixer).
	errs = append(errs, res.WriteErrs...)
	if moved {
		errs = append(errs, repairs.ErrPartiallyApplied)
	}
	errs = append(errs, res.Unrecorded...)
	if len(res.Errors) > 0 {
		errs = append(errs, errors.New(strings.Join(res.Errors, "; ")))
	}
	if len(res.Unrecorded) > 0 && !moved {
		errs = append(errs, repairs.ErrPartiallyApplied)
	}
	return fmt.Errorf("%s: row %s (%d books moved, %d series removed): %w",
		seriesPruneFixerID, d.keep.Name, res.Repointed, res.Merged, errors.Join(errs...))
}

// invalidateCaches drops the cached series list and the duplicates cache, as
// the op does after a run that changed anything: the series list carries a
// 24-hour TTL, and a prune that merged correctly but left the list cached
// reads as a prune that did nothing.
func (f *seriesPruneFixer) invalidateCaches() {
	if f.p.deps == nil {
		return
	}
	f.p.deps.InvalidateSeriesCache()
	f.p.deps.InvalidateDedupCache()
}
