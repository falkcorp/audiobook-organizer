// file: internal/plugins/maintenance/series_denumber_op.go
// version: 4.0.0
// guid: 3f0b6c84-52d1-4a97-9e35-c8b71d0af426
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	sdn "github.com/falkcorp/audiobook-organizer/internal/seriesdenumber"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// SeriesDenumberParams are the JSON parameters for the series de-numbering.
// Unknown keys are rejected (decodeStrictParams): a mistyped scope key such as
// "baseNme" must fail the run, not become "no filter" on an op that writes.
type SeriesDenumberParams struct {
	// Apply, when true, performs the merges. Default false: dry run.
	Apply bool `json:"apply"`
	// Limit caps how many PLANS are applied (0 = all). It counts plans that move
	// at least one book, so a canary of 10 changes ten series' worth of books and
	// never spends a slot on a plan that moves nothing. It caps the APPLY set,
	// never the report: a dry run always reports every candidate.
	Limit int `json:"limit,omitempty"`
	// MinBooks keeps only plans that move at least this many BOOKS (default 1): a
	// book and its alternate versions count once, because that is one title.
	// Raise it to start a canary on the series where a wrong merge is most
	// visible.
	MinBooks int `json:"minBooks,omitempty"`
	// BaseName, when set, keeps only the plans folding into a base with this
	// name (case-insensitive), under ANY author: two authors who each have a
	// numbered "Court of Owls" are both selected. Combine it with minBooks and
	// limit to canary one series.
	BaseName string `json:"baseName,omitempty"`
	// ReportPath writes every candidate (all tiers, eligible or not) and every book
	// decision as TSV before a single row is touched, and the outcomes of an apply
	// to ReportPath + ".applied". REQUIRED to apply: the undo journal is the way
	// back, and the report is what a person audits an apply against, so an apply
	// that leaves no report is refused. A dry run may omit it.
	ReportPath string `json:"reportPath,omitempty"`
}

func (p SeriesDenumberParams) validate() error {
	if p.Limit < 0 {
		return fmt.Errorf("limit must not be negative (got %d)", p.Limit)
	}
	if p.MinBooks < 0 {
		return fmt.Errorf("minBooks must not be negative (got %d)", p.MinBooks)
	}
	if p.Apply && strings.TrimSpace(p.ReportPath) == "" {
		return errors.New("apply requires reportPath: an apply nobody can audit is refused (dry-run without it first)")
	}
	return nil
}

// seriesPlanRows renders every candidate as a report row, eligible or not.
func seriesPlanRows(plans []SeriesMergePlan) []sdn.ReportRow {
	rows := make([]sdn.ReportRow, 0, len(plans))
	for _, pl := range plans {
		rows = append(rows, sdn.ReportRow{
			Kind: sdn.KindPlan, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
			Position: pl.Position, Books: pl.Books, Shape: string(pl.Shape), Confidence: string(pl.Confidence),
			Eligible: ApplyEligible(pl), Reason: pl.Reason,
		})
	}
	return rows
}

// writeSeriesDenumberReport dumps every candidate as TSV, eligible or not, then
// the book rows.
//
// TSV rather than JSON because the point of this file is to be read by a person
// deciding whether to proceed, and sorted/grepped by shape or tier while they do
// it. The `eligible` column records the decision made at THIS invocation's
// settings, so the file explains itself later without the params alongside.
func writeSeriesDenumberReport(path string, plans []SeriesMergePlan, bookRows []sdn.ReportRow) error {
	rows := seriesPlanRows(plans)
	rows = append(rows, bookRows...)
	return writeReportRows(path, rows)
}

func writeReportRows(path string, rows []sdn.ReportRow) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(sdn.RenderReport(rows)), 0o664)
}

func (p *Plugin) seriesDenumberDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.series-denumber",
		Preview:     sdk.PreviewSpec{Params: SeriesDenumberParams{}},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Merge series that carry a book number in their name",
		Description: "A series name should name the series, but many carry the book's position instead " +
			"(\"Gotham Knights 05\", \"Court of Owls: Book 11\"), which splits one series into as many " +
			"one-book series as it has volumes. This merges them onto the base name and moves the number " +
			"into the book's series position. Only names that match one exact grammar are ever applied: " +
			"<base><separator><optional Book/Bk/Vol/Volume/No/#><number 1-99>, where the base has no digit, " +
			"no connector (of, to, &, and) before the number, no part/format/edition word and no volume word. " +
			"And the number must be corroborated: the book's own stored position equals it, or at least two " +
			"other series of the same author with the same base, each holding a book, form a run of three " +
			"consecutive numbers with it. Everything else is held and reported with the reason: a lone " +
			"\"Fahrenheit 451\" stays, and so do \"Part 2\", a fraction, a range, a base that is an author's " +
			"name, a series with no author or a placeholder author, a bracketed or leading number (reported " +
			"as medium or low, never applied). A book the owner locked (series or position) is held, a book " +
			"is never moved onto a position another book holds or given a position over one it already has, " +
			"and an emptied series is deleted only after its references are counted again right before the " +
			"delete. The apply stands the scanner down while it writes. Every change is journaled, so an " +
			"apply can be undone from the operation's change log. Dry-run by default; pass {\"apply\": true, " +
			"\"reportPath\": \"...\"} to merge (the report is required to apply), and {\"limit\": N, " +
			"\"minBooks\": N, \"baseName\": \"...\"} to canary.",
		// RESUME AUDIT 2026-09-11 (b): ResumeDrop, was ResumeRestart with no
		// checkpoint. A from-zero restart recomputes the plan from the current
		// series table, and the already-merged series are gone from it, so the
		// merges themselves would not repeat — but two things the operator
		// relies on would silently break: Limit caps the APPLY set per run, so a
		// canary of 10 would apply the NEXT 10 on resume; and the report at
		// ReportPath would be overwritten with a plan that no longer lists the
		// series the first attempt merged, losing the audit trail for them (the
		// undo journal is unaffected). Apply is a reviewed, deliberate step; an
		// interrupted one is surfaced as interrupted_dropped for the operator to
		// re-run.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.series-denumber",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ProgressTimeout: 30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSeriesDenumber,
	}
}

func (p *Plugin) runSeriesDenumber(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params SeriesDenumberParams
	if err := decodeStrictParams(raw, &params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	if err := params.validate(); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()

	// 🔒 An apply that could not be undone must not start. Both checks run before
	// any read of the library, so a refusal has touched nothing:
	//   - the operation id keys every undo-journal row of this run;
	//   - RestoreSeries is what lets the revert put a deleted numbered series back
	//     under its own id, without which the books' links could not go back.
	var opID string
	if params.Apply {
		opID = registry.ReporterOpID(reporter)
		if opID == "" {
			return errors.New("series-denumber: refusing to apply: this run has no operation id, so its changes could not be journaled for undo")
		}
		if _, ok := database.AsCapability[database.SeriesRestorer](store); !ok {
			return errors.New("series-denumber: refusing to apply: this store cannot restore a deleted series, so the run could not be undone")
		}
		// The delete re-counts the series' references immediately before it, and the
		// base's creation checks the new row is empty: both need the per-series count.
		if _, ok := database.AsCapability[database.SeriesBookRefCounter](store); !ok {
			return errors.New("series-denumber: refusing to apply: this store cannot count one series' references, so a delete could not be re-checked")
		}
	}
	// A running scan rewrites book rows underneath an apply, so the scanner stands
	// down for the whole run, planning included: the plan is read from the library
	// the writes then change. A dry run writes nothing and takes no gate. The lease
	// is renewed per plan and per book; a lapsed lease stops the writes
	// (applySeriesDenumber).
	hold := t6g1Hold{Lost: func() bool { return false }, Release: func() {}}
	if params.Apply {
		h, sdErr := p.t6g1HeadStandDown(reporter)(ctx, "series-denumber apply")
		if sdErr != nil {
			return fmt.Errorf("series-denumber: refusing to apply: acquire scan stand-down: %w", sdErr)
		}
		defer h.Release()
		hold = h
	}
	log.Info("series-denumber: starting", "apply", params.Apply, "limit", params.Limit,
		"min_books", params.MinBooks, "base_name", params.BaseName)

	run, err := planSeriesDenumber(store, params)
	if err != nil {
		return err
	}
	log.Info("series-denumber: plan complete", "series", run.scanned, "candidates", len(run.candidates),
		"eligible", len(run.plans), "selected", len(run.selected))
	if len(run.candidates) == 0 {
		summary := fmt.Sprintf("series-denumber: %d series scanned, 0 carry a book number — nothing to do", run.scanned)
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

	// The report covers EVERY candidate, including the tiers that will never be
	// applied, and every book decision. Written before anything is touched.
	if params.ReportPath != "" {
		if err := writeSeriesDenumberReport(params.ReportPath, run.candidates, run.bookRows()); err != nil {
			// Fail closed: an apply nobody can audit is what the report prevents.
			return fmt.Errorf("write report %s: %w", params.ReportPath, err)
		}
		log.Info("series-denumber: report written", "path", params.ReportPath, "rows", len(run.candidates))
	}

	if !params.Apply {
		summary := "series-denumber (dry run): " + run.describe(params)
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(len(run.candidates), len(run.candidates), summary)
		return nil
	}

	if len(run.selected) == 0 {
		summary := "series-denumber: nothing to apply: " + run.describe(params)
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}
	return p.applySeriesDenumber(ctx, store, reporter, run, params, opID, hold)
}

// denumberGroup is every eligible plan that folds into one base series: the
// same resolved target, or the same base name under the same author while the
// base does not exist yet.
type denumberGroup struct {
	authorID *int
	baseName string
	// targetID is the base series when it exists, 0 when the run would create it.
	targetID int
	plans    []int // indices into denumberRun.plans
	decision sdn.GroupResult
}

// denumberRun is the read-only outcome of planning: everything the dry run
// reports and the apply executes, so the two cannot disagree.
type denumberRun struct {
	series     []database.Series
	seriesByID map[int]database.Series
	scanned    int
	candidates []SeriesMergePlan // every candidate after demotion, in report order
	plans      []SeriesMergePlan // the apply-eligible subset, same order
	results    []sdn.SourceResult
	groupOf    []int // plans index -> groups index
	groups     []*denumberGroup
	selected   []int // indices into plans, in apply order
	refCounts  map[int]int
	members    database.SeriesBooksMap

	byTier        map[SeriesConfidence]int
	booksByTier   map[SeriesConfidence]int
	byDemotion    map[string]int
	emptyNumbered int
}

func authorPtr(id int) *int {
	if id <= 0 {
		return nil
	}
	v := id
	return &v
}

// planSeriesDenumber reads the library and decides, without writing, which books
// move where. Every read fails closed: a run that cannot answer the unfiltered
// reference question or load membership aborts, dry or apply.
func planSeriesDenumber(store OpsStore, params SeriesDenumberParams) (*denumberRun, error) {
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	counts, cerr := store.GetAllSeriesBookCounts()
	if cerr != nil {
		// Fails closed like every other read here: with no counts every series
		// reads as empty, which misreports the plan and the empty-series total.
		return nil, fmt.Errorf("GetAllSeriesBookCounts: %w", cerr)
	}
	authors, aerr := store.GetAllAuthors()
	if aerr != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", aerr)
	}
	authorNames := make(map[int]string, len(authors))
	for _, a := range authors {
		authorNames[a.ID] = a.Name
	}

	run := &denumberRun{
		series: allSeries, seriesByID: make(map[int]database.Series, len(allSeries)), scanned: len(allSeries),
		byTier: map[SeriesConfidence]int{}, booksByTier: map[SeriesConfidence]int{}, byDemotion: map[string]int{},
	}
	in := make([]SeriesInput, 0, len(allSeries))
	for i := range allSeries {
		s := allSeries[i]
		run.seriesByID[s.ID] = s
		aid := 0
		if s.AuthorID != nil {
			aid = *s.AuthorID
		}
		in = append(in, SeriesInput{ID: s.ID, Name: s.Name, AuthorID: aid, Books: counts[s.ID]})
	}

	candidates := DemotePlans(SeriesDenumber(in), authorNames)
	// Deterministic order so a dry run and the apply that follows it agree.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].IntoName != candidates[j].IntoName {
			return candidates[i].IntoName < candidates[j].IntoName
		}
		if candidates[i].Position != candidates[j].Position {
			return candidates[i].Position < candidates[j].Position
		}
		return candidates[i].FromID < candidates[j].FromID
	})
	run.candidates = candidates

	// The unfiltered reference guard, fetched ONCE and BEFORE the dry-run branch:
	// it counts book rows in ANY state (live, trashed, non-primary), which the
	// membership read below cannot see (it excludes soft-deleted rows by design).
	// A source series whose unfiltered count exceeds the books this run can move
	// is still referenced and must not be deleted. Fails CLOSED: falling back to
	// the filtered counts is the bug, because it deletes rows while reporting
	// success. Compare internal/maintenance/jobs/cleanup_series.go.
	refCounts, rcErr := database.SeriesRefCounts(store)
	if rcErr != nil {
		return nil, fmt.Errorf("series-denumber: refusing to run without unfiltered series reference counts: %w", rcErr)
	}
	run.refCounts = refCounts

	for _, pl := range candidates {
		run.byTier[pl.Confidence]++
		run.booksByTier[pl.Confidence] += pl.Books
		if pl.DemotedBy != "" {
			run.byDemotion[pl.DemotedBy]++
		}
		// A numbered series with no books at all, in any state. Not a plan to
		// apply (nothing moves): dedup.series-prune's orphan class removes empty
		// series, and this op does not.
		if pl.Books == 0 && refCounts[pl.FromID] == 0 {
			run.emptyNumbered++
		}
		if ApplyEligible(pl) {
			run.plans = append(run.plans, pl)
		}
	}
	run.results = make([]sdn.SourceResult, len(run.plans))
	run.groupOf = make([]int, len(run.plans))
	if len(run.plans) == 0 {
		return run, nil
	}

	// Resolve every plan's target and group the plans that fold into the same
	// base. Keyed by the resolved series id when there is one, so two spellings
	// the store resolves to one row are decided together.
	nameTarget := map[string]int{} // base name + author -> resolved id (0: none)
	groupIdx := map[string]int{}
	for i, pl := range run.plans {
		ap := authorPtr(pl.AuthorID)
		nameKey := fmt.Sprintf("%s\x00%d", strings.ToLower(pl.IntoName), pl.AuthorID)
		targetID := pl.IntoID
		if targetID == 0 {
			id, seen := nameTarget[nameKey]
			if !seen {
				if s, gerr := store.GetSeriesByName(pl.IntoName, ap); gerr != nil {
					return nil, fmt.Errorf("series-denumber: resolve base series %q: %w", pl.IntoName, gerr)
				} else if s != nil {
					id = s.ID
				}
				nameTarget[nameKey] = id
			}
			targetID = id
		}
		key := "name:" + nameKey
		if targetID != 0 {
			key = "id:" + strconv.Itoa(targetID)
		}
		gi, ok := groupIdx[key]
		if !ok {
			gi = len(run.groups)
			groupIdx[key] = gi
			run.groups = append(run.groups, &denumberGroup{authorID: ap, baseName: pl.IntoName, targetID: targetID})
		}
		run.groups[gi].plans = append(run.groups[gi].plans, i)
		run.groupOf[i] = gi
	}

	// Membership of every source and every existing target, read ONCE
	// (SERIES-MEMBERSHIP-RESIDUAL-LOOPS: a per-plan AllVersions read is a full
	// "book:" scan once the memdb is tainted) and BEFORE the dry-run branch so the
	// preview and the apply see the same answer. Fails CLOSED: an empty map would
	// read as "every series is empty" and the apply deletes on that.
	memberIDs := make([]int, 0, 2*len(run.plans))
	for _, pl := range run.plans {
		memberIDs = append(memberIDs, pl.FromID)
	}
	for _, g := range run.groups {
		if g.targetID != 0 {
			memberIDs = append(memberIDs, g.targetID)
		}
	}
	members, mErr := database.SeriesMembershipAllVersions(store, memberIDs)
	if mErr != nil {
		return nil, fmt.Errorf("series-denumber: refusing to run without series membership: %w", mErr)
	}
	run.members = members

	// The owner's field locks of every book a plan could move, read once and in
	// parallel (one metadata-state read per book). A book whose locks cannot be
	// read is held, not guessed: see sdn.Book.LockUnreadable.
	var moverIDs []string
	for _, pl := range run.plans {
		for _, c := range members[pl.FromID] {
			moverIDs = append(moverIDs, c.ID)
		}
	}
	locks := loadBookLocks(store, moverIDs)

	// Every series by normalised name, any author: a base that does not exist for
	// THIS author but does for another is a second series of the same name if
	// created, which is for a person to decide (sdn.HoldBaseElsewhere).
	byName := map[string][]database.Series{}
	for _, s := range run.series {
		k := normSeriesName(s.Name)
		byName[k] = append(byName[k], s)
	}

	// A base that resolves to a numbered series some plan of this run folds away
	// (the store matches names case-insensitively, the planner by byte order)
	// takes no books this run: see sdn.Group.TargetIsSource.
	sourceIDs := make(map[int]bool, len(run.plans))
	for _, pl := range run.plans {
		sourceIDs[pl.FromID] = true
	}
	for _, g := range run.groups {
		grp := sdn.Group{TargetID: g.targetID, BaseName: g.baseName, TargetIsSource: g.targetID != 0 && sourceIDs[g.targetID]}
		if g.authorID != nil {
			grp.AuthorID = *g.authorID
		}
		if g.targetID != 0 {
			grp.Existing = coreToBooks(members[g.targetID], nil)
		} else {
			for _, other := range byName[normSeriesName(g.baseName)] {
				if g.authorID == nil || other.AuthorID == nil || *other.AuthorID != *g.authorID {
					grp.BaseElsewhere = describeOtherBase(other)
					break
				}
			}
		}
		for _, pi := range g.plans {
			pl := run.plans[pi]
			grp.Sources = append(grp.Sources, sdn.Source{
				FromID: pl.FromID, FromName: pl.FromName, Position: pl.Position,
				Books: coreToBooks(members[pl.FromID], locks), SiblingRun: pl.SiblingRun,
			})
		}
		g.decision = sdn.Decide(grp)
		for k, pi := range g.plans {
			run.results[pi] = g.decision.Sources[k]
		}
	}

	items := make([]sdn.Item, len(run.plans))
	for i, pl := range run.plans {
		items[i] = sdn.Item{IntoName: pl.IntoName, Position: pl.Position, Books: run.results[i].Books()}
	}
	run.selected = sdn.Select(items, sdn.Selection{MinBooks: params.MinBooks, BaseName: params.BaseName, Limit: params.Limit})
	return run, nil
}

// bookLock is what a book's field locks say about the two columns this op
// writes, or why they could not be read.
type bookLock struct {
	seriesName, seriesPosition bool
	err                        error
}

// loadBookLocks reads the field locks of every book id, concurrently and bounded
// by the CPU count. Fails closed per book: a book whose locks cannot be read is
// recorded with the error, and the decision holds it.
func loadBookLocks(store database.MetadataFieldStateReader, ids []string) map[string]bookLock {
	out := make(map[string]bookLock, len(ids))
	if len(ids) == 0 {
		return out
	}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		g.Go(func() error {
			l, err := database.LoadFieldLocks(store, id)
			bl := bookLock{err: err}
			if err == nil {
				bl.seriesName = l.Locked(database.FieldKeySeriesName)
				bl.seriesPosition = l.Locked(database.FieldKeySeriesPosition)
			}
			mu.Lock()
			out[id] = bl
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait() // every task returns nil: a failure is recorded per book
	return out
}

// coreToBooks reduces the membership rows to what the position decision reads.
// locks may be nil (the target's members are only compared with, never moved).
// A series_name lock protects both columns; a series_position lock matters only
// to a book the move would give a position, so a book already at its position is
// not held by it.
func coreToBooks(cores []database.BookCore, locks map[string]bookLock) []sdn.Book {
	out := make([]sdn.Book, 0, len(cores))
	for _, c := range cores {
		b := sdn.Book{ID: c.ID}
		if c.SeriesSequence != nil {
			v := *c.SeriesSequence
			b.Seq = &v
		}
		if c.VersionGroupID != nil {
			b.Identity = *c.VersionGroupID
		}
		if l, ok := locks[c.ID]; ok {
			switch {
			case l.err != nil:
				b.LockUnreadable = l.err.Error()
			case l.seriesName:
				b.LockedBy = database.FieldKeySeriesName
			case l.seriesPosition && c.SeriesSequence == nil:
				b.LockedBy = database.FieldKeySeriesPosition
			}
		}
		out = append(out, b)
	}
	return out
}

// normSeriesName is the store's name key: case and runs of space folded.
func normSeriesName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func describeOtherBase(s database.Series) string {
	if s.AuthorID == nil {
		return fmt.Sprintf("series %d, no author", s.ID)
	}
	return fmt.Sprintf("series %d under author %d", s.ID, *s.AuthorID)
}

// bookRows is one report row per book decision of every eligible plan: the
// planned move (or not_selected, when the canary controls leave the plan out)
// and the books held back with the reason.
func (r *denumberRun) bookRows() []sdn.ReportRow {
	selected := make(map[int]bool, len(r.selected))
	for _, i := range r.selected {
		selected[i] = true
	}
	var rows []sdn.ReportRow
	for i, pl := range r.plans {
		g := r.groups[r.groupOf[i]]
		target := "(new)"
		if g.targetID != 0 {
			target = strconv.Itoa(g.targetID)
		}
		status := "planned"
		if !selected[i] {
			status = "not_selected"
		}
		for _, mv := range r.results[i].Moves {
			rows = append(rows, sdn.ReportRow{
				Kind: sdn.KindBook, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
				Position: pl.Position, BookID: mv.BookID, OldSeq: sdn.SeqString(mv.OldSeq),
				NewSeriesID: target, NewSeq: strconv.Itoa(mv.NewSeq), Status: status,
			})
		}
		for _, h := range r.results[i].Held {
			rows = append(rows, sdn.ReportRow{
				Kind: sdn.KindHeldBook, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
				Position: pl.Position, BookID: h.BookID, Reason: h.Reason, Status: "held: " + string(h.Code),
			})
		}
	}
	return rows
}

// describe is the operator-facing summary of a plan: the same text heads a dry
// run and an apply that has nothing to do, so the two read alike.
func (r *denumberRun) describe(params SeriesDenumberParams) string {
	tiers := fmt.Sprintf("high=%d(%d books) medium=%d(%d books) low=%d(%d books)",
		r.byTier[ConfidenceHigh], r.booksByTier[ConfidenceHigh],
		r.byTier[ConfidenceMedium], r.booksByTier[ConfidenceMedium],
		r.byTier[ConfidenceLow], r.booksByTier[ConfidenceLow])

	demoted := make([]string, 0, len(r.byDemotion))
	for code, n := range r.byDemotion {
		demoted = append(demoted, fmt.Sprintf("%s=%d", code, n))
	}
	sort.Strings(demoted)

	// What the decision holds back, over every eligible plan (not only the
	// selected ones), so the canary controls do not hide it.
	heldByCode := map[sdn.HoldCode]int{}
	carrying := 0
	for i := range r.plans {
		if len(r.results[i].Moves) > 0 {
			carrying++
		}
		for _, h := range r.results[i].Held {
			heldByCode[h.Code]++
		}
	}

	// What the selected plans would do.
	bases := map[int]struct{}{}
	newBases := map[int]struct{}{}
	booksMoving, heldBackByGuard := 0, 0
	byReason := map[string]int{}
	var examples, heldBackExamples []string
	for _, i := range r.selected {
		pl := r.plans[i]
		gi := r.groupOf[i]
		bases[gi] = struct{}{}
		if r.groups[gi].targetID == 0 {
			newBases[gi] = struct{}{}
		}
		booksMoving += len(r.results[i].Moves)
		byReason[pl.Reason]++
		if len(examples) < 12 {
			examples = append(examples, fmt.Sprintf("%q → %q #%d [%s]", pl.FromName, pl.IntoName, pl.Position, pl.Reason))
		}
		if r.refCounts[pl.FromID] > len(r.members[pl.FromID]) {
			heldBackByGuard++
			if len(heldBackExamples) < 6 {
				heldBackExamples = append(heldBackExamples, fmt.Sprintf("%q (series %d)", pl.FromName, pl.FromID))
			}
		}
	}
	reasons := make([]string, 0, len(byReason))
	for reason, n := range byReason {
		reasons = append(reasons, fmt.Sprintf("%s=%d", reason, n))
	}
	sort.Strings(reasons)

	var heldExamples []string
	for _, pl := range r.candidates {
		if !ApplyEligible(pl) && len(heldExamples) < 6 {
			heldExamples = append(heldExamples, fmt.Sprintf("%q → %q #%d [%s/%s]", pl.FromName, pl.IntoName, pl.Position, pl.Shape, pl.Confidence))
		}
	}

	return fmt.Sprintf(
		"%d series scanned, %d carry a position (%s). Demoted to low by a shape check: %d (%s). "+
			"%d plans are apply-eligible and %d of them move books; books held back, never moved: "+
			"%d have a position of their own that differs, %d would share a position. "+
			"%d numbered series have no books at all (dedup.series-prune removes empty series; this op does not). "+
			"Selected (minBooks %d, base %q, limit %d): %d plans into %d base series (%d to be created), %d books would move, "+
			"%d would be merged but NOT deleted (still referenced by rows this run cannot move: %s) — by evidence: %s | "+
			"applying: %s | held: %s",
		r.scanned, len(r.candidates), tiers,
		sumCounts(r.byDemotion), strings.Join(demoted, " "),
		len(r.plans), carrying, heldByCode[sdn.HoldOwnPosition], heldByCode[sdn.HoldCollision],
		r.emptyNumbered,
		maxInt(params.MinBooks, 1), params.BaseName, params.Limit, len(r.selected), len(bases), len(newBases), booksMoving,
		heldBackByGuard, strings.Join(heldBackExamples, "; "),
		strings.Join(reasons, " "),
		strings.Join(examples, "; "), strings.Join(heldExamples, "; "))
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// applySeriesDenumber executes the selected plans. Every write is journaled for
// undo under opID BEFORE it is made, in the order the revert needs (it runs
// newest first):
//
//  1. a series_create row, AFTER the base series was really created by this run
//     (the new row is empty; one that already holds books was somebody else's and
//     is neither used nor journaled) and before any book moves into it;
//  2. per book, inside the ModifyBook callback, a series_id row and, when the
//     book had no position, a series_sequence row (sdn.MoveRows). A journal
//     failure aborts the write and a skipped book leaves no row; and a row whose
//     write then FAILED is voided (Voided + RevertedAt), so a revert never acts
//     on a move that did not happen, whatever another writer did to the book since;
//  3. a series_row_delete row (snapshot) before an emptied numbered series is
//     deleted, so the revert can recreate it under its own id; voided when the
//     delete fails.
//
// 🔒 The scanner stands down for the whole run (hold), and the lease is checked
// before every plan and every book.
//
// 🔒 SEQUENTIAL ON PURPOSE. Two plans folding onto one base must not both decide
// the base is missing and create it; series creation is the shared mutable state
// here (contrast dedupe-book-file-rows, where every book is disjoint). The
// position decision is made per base over the whole eligible set before this
// loop (sdn.Decide), so the loop never has to compare books itself.
//
// Cancelled or failed runs still drop the series cache and write the outcome
// report for what they did write (the defer below): a half-applied run is the one
// whose report matters most.
func (p *Plugin) applySeriesDenumber(ctx context.Context, store OpsStore, reporter sdk.Reporter,
	run *denumberRun, params SeriesDenumberParams, opID string, hold t6g1Hold) (err error) {
	log := reporter.Logger()
	record := func(c *database.OperationChange) error {
		c.ID = ulid.Make().String()
		c.OperationID = opID
		return database.RecordOperationChange(store, c)
	}
	// void takes back a journal row whose write was refused or failed: rewritten
	// under its id with Voided and RevertedAt, so no revert acts on it.
	void := func(rows []*database.OperationChange) error {
		var errs []error
		now := time.Now()
		for _, r := range rows {
			v := *r
			v.RevertedAt, v.Voided = &now, true
			if verr := database.RecordOperationChange(store, &v); verr != nil {
				errs = append(errs, fmt.Errorf("void journal row %s: %w", v.ID, verr))
			}
		}
		return errors.Join(errs...)
	}
	counter, _ := database.AsCapability[database.SeriesBookRefCounter](store) // checked present by runSeriesDenumber

	targets := make(map[int]int, len(run.groups)) // group index -> base series in use
	for gi, g := range run.groups {
		if g.targetID != 0 {
			targets[gi] = g.targetID
		}
	}
	var outcomes []sdn.ReportRow
	var merged, movedBooks, created, deleted, failed, stayed, refusedByGuard, targetAppeared, heldDelete int

	defer func() {
		// Creating base series, moving books between them and deleting the emptied
		// ones all change the cached series list (24-hour TTL, warmed at startup).
		// Without this the result is invisible on /api/v1/series until the next
		// restart, which reads as an op that did nothing.
		if created > 0 || deleted > 0 || movedBooks > 0 {
			p.deps.InvalidateSeriesCache()
		}
		if params.ReportPath != "" {
			if werr := writeReportRows(params.ReportPath+".applied", outcomes); werr != nil {
				log.Warn("series-denumber: could not write the outcome report", "path", params.ReportPath+".applied", "err", werr)
			}
		}
	}()

	for n, idx := range run.selected {
		if ctx.Err() != nil {
			log.Warn("series-denumber: cancelled", "merged", merged, "remaining", len(run.selected)-n)
			return ctx.Err()
		}
		if hold.Lost() {
			return fmt.Errorf("series-denumber: scan stand-down lease lapsed before plan %d of %d; stopped with %d books moved", n+1, len(run.selected), movedBooks)
		}
		pl := run.plans[idx]
		sr := run.results[idx]
		gi := run.groupOf[idx]
		g := run.groups[gi]

		targetID := targets[gi]
		if targetID == 0 {
			// The base did not exist when the position decision was made. One that
			// exists now was made by somebody else, and its books were never
			// compared with the movers', so the plan is skipped, not merged blind.
			existing, gerr := store.GetSeriesByName(pl.IntoName, g.authorID)
			switch {
			case gerr != nil:
				failed++
				log.Warn("series-denumber: base lookup failed, plan skipped", "name", pl.IntoName, "err", gerr)
				continue
			case existing != nil:
				targetAppeared++
				log.Warn("series-denumber: base series appeared since the plan was made, plan skipped", "name", pl.IntoName, "series", existing.ID)
				continue
			}
			ns, cerr := store.CreateSeries(pl.IntoName, g.authorID)
			if cerr != nil || ns == nil {
				failed++
				log.Warn("series-denumber: CreateSeries failed", "name", pl.IntoName, "err", cerr)
				continue
			}
			// CreateSeries resolves-or-creates: a row somebody made between the lookup
			// above and this call comes back as if it were new. The row this run
			// creates is empty, so one that holds books is not ours: not used, not
			// journaled (a revert of a series_create would delete THEIR series).
			refs, rerr := counter.GetSeriesBookRefCount(ns.ID)
			switch {
			case rerr != nil:
				failed++
				log.Warn("series-denumber: could not tell whether the base is new, plan skipped", "series", ns.ID, "name", pl.IntoName, "err", rerr)
				continue
			case refs > 0:
				targetAppeared++
				log.Warn("series-denumber: base series appeared while it was being created, plan skipped", "name", pl.IntoName, "series", ns.ID)
				continue
			}
			name := ns.Name
			if name == "" {
				name = pl.IntoName
			}
			snap, serr := undo.NewSeriesSnapshotJSON(undo.SeriesSnapshot{ID: ns.ID, Name: name, AuthorID: ns.AuthorID})
			nsID := ns.ID
			var jerr error
			if serr != nil {
				jerr = serr
			} else {
				jerr = record(&database.OperationChange{SeriesID: &nsID, ChangeType: undo.ChangeTypeSeriesCreate, NewValue: snap})
			}
			if jerr != nil {
				// Created but not journaled: no book moves into it. It is an empty
				// series, which dedup.series-prune removes.
				failed++
				log.Warn("series-denumber: base series created but not journaled, plan skipped", "series", ns.ID, "name", name, "err", jerr)
				continue
			}
			targetID = ns.ID
			targets[gi] = targetID
			created++
			outcomes = append(outcomes, sdn.ReportRow{
				Kind: sdn.KindCreatedSeries, IntoName: name, NewSeriesID: strconv.Itoa(ns.ID), Status: "created",
			})
		}
		if targetID == pl.FromID {
			continue // already the canonical row
		}

		// The unfiltered reference guard (see planSeriesDenumber): whether the
		// emptied source may be deleted does not depend on what this loop moved
		// alone; rows it cannot see still hold the series.
		sourceBooks := len(run.members[pl.FromID])
		safeToDelete := run.refCounts[pl.FromID] <= sourceBooks
		if !safeToDelete {
			refusedByGuard++
			log.Warn("series-denumber: series still referenced by rows this run cannot move — merging books but not deleting",
				"series", pl.FromID, "name", pl.FromName, "unfiltered_refs", run.refCounts[pl.FromID], "movable_books", sourceBooks)
		}

		movedAll := true
		stay := 0
		for _, mv := range sr.Moves {
			if hold.Lost() {
				return fmt.Errorf("series-denumber: scan stand-down lease lapsed at book %s; stopped with %d books moved", mv.BookID, movedBooks)
			}
			sid := targetID
			did := false
			skip := ""
			var journaled []*database.OperationChange
			// Write only SeriesID (and a missing SeriesSequence), under the book's
			// write lock, so a column another writer commits meanwhile is not
			// reverted. The fresh row decides: a book moved off the source meanwhile,
			// given a position of its own, or locked by the owner since the plan was
			// made, is left alone, and so is a base that was deleted meanwhile.
			written, uerr := store.ModifyBook(mv.BookID, func(full *database.Book) error {
				if full.SeriesID == nil || *full.SeriesID != pl.FromID {
					skip = "left the source series meanwhile"
					return database.ErrSkipBookWrite
				}
				if full.SeriesSequence != nil && *full.SeriesSequence != mv.NewSeq {
					skip = fmt.Sprintf("has its own position %d", *full.SeriesSequence)
					return database.ErrSkipBookWrite
				}
				// The owner's locks, read again now: one set after the plan was made
				// still holds the book. Fails closed (an error aborts the write).
				locks, lerr := database.LoadFieldLocks(store, full.ID)
				if lerr != nil {
					return fmt.Errorf("field locks unavailable, book NOT written: %w", lerr)
				}
				cur := mv
				cur.SetSeq = full.SeriesSequence == nil
				switch {
				case locks.Locked(database.FieldKeySeriesName):
					skip = string(sdn.HoldLocked) + ": the owner locked " + database.FieldKeySeriesName
					return database.ErrSkipBookWrite
				case cur.SetSeq && locks.Locked(database.FieldKeySeriesPosition):
					skip = string(sdn.HoldLocked) + ": the owner locked " + database.FieldKeySeriesPosition
					return database.ErrSkipBookWrite
				}
				// The base must still exist: a concurrent delete (dedup.series-prune)
				// would otherwise leave the book pointing at a deleted series.
				if tgt, terr := store.GetSeriesByID(targetID); terr != nil {
					return fmt.Errorf("read base series %d: %w", targetID, terr)
				} else if tgt == nil {
					skip = fmt.Sprintf("the base series %d no longer exists", targetID)
					return database.ErrSkipBookWrite
				}
				for _, jr := range sdn.MoveRows(cur, targetID) {
					c := &database.OperationChange{
						BookID: jr.BookID, ChangeType: jr.ChangeType, FieldName: jr.FieldName,
						OldValue: jr.OldValue, NewValue: jr.NewValue,
					}
					if err := record(c); err != nil {
						return err
					}
					journaled = append(journaled, c)
				}
				full.SeriesID = &sid
				if cur.SetSeq {
					pos := mv.NewSeq
					full.SeriesSequence = &pos
				}
				did = true
				return nil
			})
			wrote := uerr == nil && written != nil && did
			if len(journaled) > 0 && !wrote {
				// The rows describe a write that did not happen (a later row failed,
				// or the book write itself did). Voided, a revert would otherwise
				// replay the old value over whatever a later writer committed.
				if verr := void(journaled); verr != nil {
					failed++
					log.Warn("series-denumber: could not void the journal rows of a failed write", "book", mv.BookID, "err", verr)
				}
			}
			row := sdn.ReportRow{
				Kind: sdn.KindBook, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
				Position: pl.Position, BookID: mv.BookID, OldSeq: sdn.SeqString(mv.OldSeq),
				NewSeriesID: strconv.Itoa(targetID), NewSeq: strconv.Itoa(mv.NewSeq),
			}
			switch {
			case uerr != nil:
				failed++
				movedAll = false
				log.Warn("series-denumber: ModifyBook failed", "book", mv.BookID, "err", uerr)
				row.Status = "failed: " + uerr.Error()
			case written == nil:
				failed++
				movedAll = false
				row.Status = "failed: book not written"
			case !did:
				// Nothing was written and nothing journaled; the book keeps the
				// source series referenced, so the source is not deleted.
				stayed++
				stay++
				log.Info("series-denumber: book not moved", "book", mv.BookID, "why", skip)
				row.Status = "skipped: " + skip
			default:
				// 🔑 LOAD-BEARING: the emptiness check below reads this map. Without
				// the Move it still lists every book this plan just moved, and the
				// emptied source is never deleted.
				run.members.Move(mv.BookID, pl.FromID, &sid)
				movedBooks++
				row.Status = "moved"
			}
			outcomes = append(outcomes, row)
		}

		// Delete the emptied source only when every book it held moved, none was
		// held back or skipped, the unfiltered guard confirms nothing else
		// references it, and it is not a held row. The references are counted AGAIN
		// immediately before the delete: the planning-time map is minutes old and a
		// book may have been linked into the series since. The snapshot is journaled
		// BEFORE the delete: a revert recreates the row from it. A delete that then
		// fails voids the row.
		src := run.seriesByID[pl.FromID]
		if movedAll && stay == 0 && len(sr.Held) == 0 && safeToDelete &&
			len(run.members[pl.FromID]) == 0 && !database.SeriesHeld(src) {
			if refs, rerr := counter.GetSeriesBookRefCount(src.ID); rerr != nil {
				failed++
				heldDelete++
				log.Warn("series-denumber: not deleting emptied series: its references could not be counted", "series", src.ID, "err", rerr)
				outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: references not countable: " + rerr.Error()})
			} else if refs != 0 {
				heldDelete++
				log.Warn("series-denumber: not deleting emptied series: a book references it now", "series", src.ID, "name", src.Name, "refs", refs)
				outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: fmt.Sprintf("kept: %d book rows reference it since the plan", refs)})
			} else {
				snap, serr := undo.NewSeriesSnapshotJSON(undo.SeriesSnapshot{ID: src.ID, Name: src.Name, AuthorID: src.AuthorID})
				srcID := src.ID
				var jerr error
				var delRow *database.OperationChange
				if serr != nil {
					jerr = serr
				} else {
					delRow = &database.OperationChange{SeriesID: &srcID, ChangeType: undo.ChangeTypeSeriesRowDelete, OldValue: snap}
					jerr = record(delRow)
				}
				switch {
				case jerr != nil:
					failed++
					log.Warn("series-denumber: not deleting emptied series: its undo row was not written", "series", src.ID, "err", jerr)
				default:
					if derr := store.DeleteSeries(src.ID); derr != nil {
						// The row stays, so its undo row is voided: a revert has nothing to
						// restore.
						failed++
						log.Warn("series-denumber: DeleteSeries failed", "series", src.ID, "err", derr)
						if verr := void([]*database.OperationChange{delRow}); verr != nil {
							log.Warn("series-denumber: could not void the delete's undo row", "series", src.ID, "err", verr)
						}
					} else {
						deleted++
						outcomes = append(outcomes, sdn.ReportRow{
							Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "deleted",
						})
					}
				}
			}
		}
		merged++
		if (n+1)%25 == 0 || n+1 == len(run.selected) {
			_ = reporter.UpdateProgress(n+1, len(run.selected),
				fmt.Sprintf("merged %d/%d series (%d books moved)", merged, len(run.selected), movedBooks))
		}
	}

	bases := map[int]struct{}{}
	for _, i := range run.selected {
		bases[run.groupOf[i]] = struct{}{}
	}
	summary := fmt.Sprintf(
		"series-denumber: %d series scanned, merged %d plans into %d base series "+
			"(%d books moved, %d base series created, %d emptied series deleted, %d emptied series kept at the last count, "+
			"%d books skipped, %d held back by the reference guard, %d skipped because the base appeared mid-run), "+
			"failed %d | undo: revert operation %s",
		run.scanned, merged, len(bases), movedBooks, created, deleted, heldDelete, stayed, refusedByGuard, targetAppeared, failed, opID)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(run.selected), len(run.selected), summary)
	if failed > 0 {
		// A run with failed writes is not a success: the op's status says so, the
		// summary above says what happened, and the journal still covers what landed.
		return fmt.Errorf("series-denumber: %d writes failed (the rest of the run is journaled and can be undone): %s", failed, summary)
	}
	return nil
}
