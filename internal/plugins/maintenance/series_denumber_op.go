// file: internal/plugins/maintenance/series_denumber_op.go
// version: 5.2.0
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
			"And the number must be corroborated by the library: the series must hold exactly one book, and " +
			"at least two other series of the same author with the same base, each holding exactly one book " +
			"with its own title, must form a run of three or more consecutive numbers with it. A series that " +
			"holds several books, a run with a member that holds several, one whose books share a title, an " +
			"identifier or the base name itself, and a book's own stored position (the importer copies it from " +
			"the name) are NOT corroboration. Everything else is held and reported with the reason: a lone " +
			"\"Batman 66\" stays, and so do \"Part 2\", a fraction, a range, a base that is an author's name " +
			"or a part of it, a series with no author, a dangling author or a placeholder author, a bracketed " +
			"or leading number (reported as medium or low, never applied). A book the owner locked (series or " +
			"position) is held, a book is never moved onto a position another book holds (live or trashed) or " +
			"given a position over one it already has, and an emptied series is deleted only after its " +
			"references are counted again and its row is read again right before the delete. The apply stands " +
			"the scanner down while it writes. Every change is journaled, so an apply can be undone from the " +
			"operation's change log. Dry-run by default; pass {\"apply\": true, " +
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
		// The base's creation checks the new row is empty, and the delete looks at
		// the source's references before it journals anything: both need the
		// per-series count.
		if _, ok := database.AsCapability[database.SeriesBookRefCounter](store); !ok {
			return errors.New("series-denumber: refusing to apply: this store cannot count one series' references, so a delete could not be re-checked")
		}
		// The delete itself is the store's check-and-delete under the locks every
		// book write takes: a count here followed by a plain DeleteSeries leaves a
		// window in which a book written into the series is stranded on a row that
		// no longer exists.
		if _, ok := database.AsCapability[database.SeriesUnreferencedDeleter](store); !ok {
			return errors.New("series-denumber: refusing to apply: this store cannot delete a series only if nothing references it, so an emptied series could not be deleted safely")
		}
	}
	// A running scan rewrites book rows underneath an apply, so the scanner stands
	// down for the whole run, planning included: the plan is read from the library
	// the writes then change. A dry run writes nothing and takes no gate. The lease
	// is renewed per plan and per book; a lapsed lease stops the writes
	// (applySeriesDenumber).
	hold := t6g1Hold{Lost: func() bool { return false }, Release: func() {}}
	if params.Apply {
		var sdErr error
		if hold, sdErr = standDownForApply(ctx, p.deps, reporter); sdErr != nil {
			return sdErr
		}
		defer hold.Release()
	}
	log.Info("series-denumber: starting", "apply", params.Apply, "limit", params.Limit,
		"min_books", params.MinBooks, "base_name", params.BaseName)

	planStart := time.Now()
	run, err := planSeriesDenumber(store, params)
	if err != nil {
		return err
	}
	// The stand-down lease (5 minutes by default) is renewed per plan and per book
	// of the apply, not while planning: a plan that takes longer than the lease
	// lapses it, and the first check of the apply then stops the run before any
	// write. The duration is logged so that margin can be read off a real run.
	log.Info("series-denumber: plan complete", "series", run.scanned, "candidates", len(run.candidates),
		"eligible", len(run.plans), "selected", len(run.selected), "planning_ms", time.Since(planStart).Milliseconds())
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

// standDownForApply stands the scanner down for an apply and returns the gate the
// apply renews. It fails closed: a nil scan controller, or one that cannot take
// the gate, refuses the apply. The shared helper (acquireScanStandDownForApply)
// answers held=false with no error for a nil controller so that ops with no
// interlock run on; this one writes series rows library-wide and does not.
func standDownForApply(ctx context.Context, scan ScanController, reporter sdk.Reporter) (t6g1Hold, error) {
	holderID, held, release, err := acquireScanStandDownForApply(ctx, scan, reporter, "series-denumber apply")
	if err != nil {
		return t6g1Hold{}, fmt.Errorf("series-denumber: refusing to apply: acquire scan stand-down: %w", err)
	}
	if !held {
		release()
		return t6g1Hold{}, errors.New("series-denumber: refusing to apply: the scanner could not be stood down (no scan controller), so a running scan could rewrite the books underneath the writes")
	}
	return t6g1Hold{
		Lost:    func() bool { return scanStandDownLostForApply(scan, holderID, held) },
		Release: release,
	}, nil
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
	//
	// The siblings of every plan are read with it: a run is judged by what its
	// members' books are (CheckRun), and a sibling that is no plan of its own (a
	// held shape, another tier) is still a member of the run.
	memberIDs := make([]int, 0, 2*len(run.plans))
	seenMember := map[int]bool{}
	addMember := func(id int) {
		if !seenMember[id] {
			seenMember[id] = true
			memberIDs = append(memberIDs, id)
		}
	}
	for _, pl := range run.plans {
		addMember(pl.FromID)
		for _, sid := range pl.SiblingIDs {
			addMember(sid)
		}
	}
	for _, g := range run.groups {
		if g.targetID != 0 {
			addMember(g.targetID)
		}
	}
	members, mErr := database.SeriesMembershipAllVersions(store, memberIDs)
	if mErr != nil {
		return nil, fmt.Errorf("series-denumber: refusing to run without series membership: %w", mErr)
	}
	run.members = members

	// The trashed books of every existing target. The membership read above lists
	// live books only, but a trashed book keeps its position in the series and
	// returns to it on restore, so a mover must not take it. Read once, and only
	// when some target exists (the read is a scan of the trash).
	targetIDs := map[int]bool{}
	for _, g := range run.groups {
		if g.targetID != 0 {
			targetIDs[g.targetID] = true
		}
	}
	trashedIn, tErr := readTrashedMembers(store, targetIDs)
	if tErr != nil {
		return nil, fmt.Errorf("series-denumber: refusing to run without the trashed members of the base series: %w", tErr)
	}

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
			grp.Existing, grp.UnseenExisting = targetView(members[g.targetID], trashedIn[g.targetID], refCounts[g.targetID])
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
				Books: coreToBooks(members[pl.FromID], locks),
				Run:   runVerdictFor(pl, run.seriesByID, members),
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

// factsOf reduces membership rows to what the run check compares.
func factsOf(cores []database.BookCore) []sdn.Facts {
	out := make([]sdn.Facts, 0, len(cores))
	for _, c := range cores {
		f := sdn.Facts{ID: c.ID, Title: c.Title}
		if c.VersionGroupID != nil {
			f.Identity = *c.VersionGroupID
		}
		if c.ASIN != nil {
			f.ASIN = *c.ASIN
		}
		if c.ISBN10 != nil {
			f.ISBN10 = *c.ISBN10
		}
		if c.ISBN13 != nil {
			f.ISBN13 = *c.ISBN13
		}
		out = append(out, f)
	}
	return out
}

// runVerdictFor asks whether the series of a plan sits in a run of consecutive,
// single-book, distinctly titled series of its author and base. The siblings are
// numbered by what their own names carry.
func runVerdictFor(pl SeriesMergePlan, byID map[int]database.Series, members database.SeriesBooksMap) sdn.RunVerdict {
	self := sdn.RunSeries{ID: pl.FromID, N: pl.Position, Books: factsOf(members[pl.FromID])}
	var siblings []sdn.RunSeries
	for _, id := range pl.SiblingIDs {
		ser, ok := byID[id]
		if !ok {
			continue
		}
		parsed, _, ok := sdn.ParseName(ser.Name)
		if !ok {
			continue
		}
		siblings = append(siblings, sdn.RunSeries{ID: id, N: parsed.N, Books: factsOf(members[id])})
	}
	return sdn.CheckRun(pl.IntoName, self, siblings)
}

// trashLister is the one read of the trash this op needs.
type trashLister interface {
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
}

// readTrashedMembers returns the soft-deleted books that sit in any of the given
// series. It reads the whole trash once (there is no per-series trash read) and
// does not read it at all when no series is asked about.
func readTrashedMembers(store trashLister, ids map[int]bool) (map[int][]database.Book, error) {
	out := map[int][]database.Book{}
	if len(ids) == 0 {
		return out, nil
	}
	trashed, err := store.ListSoftDeletedBooks(0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("ListSoftDeletedBooks: %w", err)
	}
	for _, b := range trashed {
		if b.SeriesID != nil && ids[*b.SeriesID] {
			out[*b.SeriesID] = append(out[*b.SeriesID], b)
		}
	}
	return out, nil
}

// targetView is what a base series holds, as the position decision needs it: its
// live books and its trashed ones, plus how many book rows reference the series
// that are in neither list (refs is the unfiltered count). A non-zero unseen
// holds every move into the series: the decision cannot know a position is free.
func targetView(live []database.BookCore, trashed []database.Book, refs int) (existing []sdn.Book, unseen int) {
	existing = coreToBooks(live, nil)
	for _, b := range trashed {
		sb := sdn.Book{ID: b.ID}
		if b.SeriesSequence != nil {
			v := *b.SeriesSequence
			sb.Seq = &v
		}
		if b.VersionGroupID != nil {
			sb.Identity = *b.VersionGroupID
		}
		existing = append(existing, sb)
	}
	if n := refs - len(live) - len(trashed); n > 0 {
		unseen = n
	}
	return existing, unseen
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
	heldByCode, carrying := r.heldByCode()

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
			"%d plans are apply-eligible and %d of them move books; books held back, never moved, by reason: %s. "+
			"%d numbered series have no books at all (dedup.series-prune removes empty series; this op does not). "+
			"Selected (minBooks %d, base %q, limit %d): %d plans into %d base series (%d to be created), %d books would move, "+
			"%d would be merged but NOT deleted (still referenced by rows this run cannot move: %s) — by evidence: %s | "+
			"applying: %s | held: %s",
		r.scanned, len(r.candidates), tiers,
		sumCounts(r.byDemotion), strings.Join(demoted, " "),
		len(r.plans), carrying, formatHoldCounts(heldByCode),
		r.emptyNumbered,
		maxInt(params.MinBooks, 1), params.BaseName, params.Limit, len(r.selected), len(bases), len(newBases), booksMoving,
		heldBackByGuard, strings.Join(heldBackExamples, "; "),
		strings.Join(reasons, " "),
		strings.Join(examples, "; "), strings.Join(heldExamples, "; "))
}

// formatHoldCounts renders every hold code with its count, in code order, so a
// summary never leaves a code out: a hold the line does not show is a hold nobody
// looks at. "none" when nothing was held.
func formatHoldCounts(m map[sdn.HoldCode]int) string {
	if len(m) == 0 {
		return "none"
	}
	codes := make([]string, 0, len(m))
	for c := range m {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%s=%d", c, m[sdn.HoldCode(c)]))
	}
	return strings.Join(parts, " ")
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

// journalRecorder writes one undo-journal row under the run's operation id.
type journalRecorder func(*database.OperationChange) error

// versionResult is what moving one row of a book did.
type versionResult struct {
	written   *database.Book
	did       bool
	skip      string
	journaled []*database.OperationChange
	err       error
}

// moveVersion repoints one book row from fromID to targetID, writing only
// SeriesID (and a missing SeriesSequence) under the book's write lock, so a
// column another writer commits meanwhile is not reverted. The fresh row
// decides: a book moved off the source meanwhile, given a position of its own,
// or locked by the owner since the plan was made is left alone, and so is a base
// that was deleted meanwhile. The owner's locks go through
// database.ApplyRespectingLocks, the one place that maps a lock to its columns
// (series_name protects the series and the position, series_position the
// position); a mutation it had to take back is a book that stays.
//
// dry runs every check and the lock-respecting mutation on the fresh row and
// returns without writing or journaling: the preflight of a book's versions.
//
// 🔒 Journaling order, inside the callback: the rows are written after every
// check passed and BEFORE ModifyBook writes the row, so a crash leaves a row for a
// write that did not happen (voided by the caller, never replayed), never a write
// without a row.
func moveVersion(store OpsStore, record journalRecorder, mv sdn.Move, fromID, targetID int, dry bool) versionResult {
	var r versionResult
	sid := targetID
	written, err := store.ModifyBook(mv.BookID, func(full *database.Book) error {
		if full.SeriesID == nil || *full.SeriesID != fromID {
			r.skip = "left the source series meanwhile"
			return database.ErrSkipBookWrite
		}
		if full.SeriesSequence != nil && *full.SeriesSequence != mv.NewSeq {
			r.skip = fmt.Sprintf("has its own position %d", *full.SeriesSequence)
			return database.ErrSkipBookWrite
		}
		cur := mv
		cur.SetSeq = full.SeriesSequence == nil
		oldSeries, oldSeq := full.SeriesID, full.SeriesSequence
		restored, lerr := database.ApplyRespectingLocks(store, full, func(b *database.Book) {
			b.SeriesID = &sid
			if cur.SetSeq {
				pos := mv.NewSeq
				b.SeriesSequence = &pos
			}
		})
		if lerr != nil {
			return fmt.Errorf("field locks unavailable, book NOT written: %w", lerr)
		}
		if len(restored) > 0 {
			// A column the owner locked was taken back. The other column may still
			// hold the mutation: the move is all or nothing, so put both back.
			full.SeriesID, full.SeriesSequence = oldSeries, oldSeq
			r.skip = string(sdn.HoldLocked) + ": the owner locked " + strings.Join(restored, ", ")
			return database.ErrSkipBookWrite
		}
		// The base must still exist: a concurrent delete (dedup.series-prune)
		// would otherwise leave the book pointing at a deleted series.
		if tgt, terr := store.GetSeriesByID(targetID); terr != nil {
			return fmt.Errorf("read base series %d: %w", targetID, terr)
		} else if tgt == nil {
			r.skip = fmt.Sprintf("the base series %d no longer exists", targetID)
			return database.ErrSkipBookWrite
		}
		if dry {
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
			r.journaled = append(r.journaled, c)
		}
		r.did = true
		return nil
	})
	r.written, r.err = written, err
	if err != nil || written == nil {
		r.did = false
	}
	return r
}

// restoreVersion takes one moved row back to the source series (the
// compensation of a book whose later version could not move). It acts only on a
// row still in the target: one that moved again belongs to another writer now.
func restoreVersion(store OpsStore, mv sdn.Move, fromID, targetID int) error {
	moved := false
	_, err := store.ModifyBook(mv.BookID, func(full *database.Book) error {
		if full.SeriesID == nil || *full.SeriesID != targetID {
			return database.ErrSkipBookWrite
		}
		from := fromID
		full.SeriesID = &from
		if mv.SetSeq && full.SeriesSequence != nil && *full.SeriesSequence == mv.NewSeq {
			full.SeriesSequence = nil
		}
		moved = true
		return nil
	})
	if err != nil {
		return err
	}
	if !moved {
		return errors.New("the row is no longer in the base series")
	}
	return nil
}

// moveGroups splits a plan's moves into the versions of one book each, in order.
func moveGroups(moves []sdn.Move) [][]sdn.Move {
	var order []string
	by := map[string][]sdn.Move{}
	for _, m := range moves {
		if _, seen := by[m.Identity]; !seen {
			order = append(order, m.Identity)
		}
		by[m.Identity] = append(by[m.Identity], m)
	}
	out := make([][]sdn.Move, 0, len(order))
	for _, k := range order {
		out = append(out, by[k])
	}
	return out
}

// bookOfMove is the decision's view of a book a move put into the target.
func bookOfMove(mv sdn.Move) sdn.Book {
	seq := mv.NewSeq
	b := sdn.Book{ID: mv.BookID, Seq: &seq}
	if id, ok := strings.CutPrefix(mv.Identity, "g:"); ok {
		b.Identity = id
	}
	return b
}

// targetState is what the apply knows about one base series: every book it holds
// (live, trashed, and the ones this run put there) and how many rows that is.
type targetState struct {
	existing []sdn.Book
	tracked  int
}

// readTargetState reads a base series' members again, live and trashed, right
// before the first write into it.
func readTargetState(store OpsStore, targetID int) (*targetState, error) {
	live, err := database.SeriesMembershipAllVersions(store, []int{targetID})
	if err != nil {
		return nil, fmt.Errorf("re-read members of series %d: %w", targetID, err)
	}
	trashed, err := readTrashedMembers(store, map[int]bool{targetID: true})
	if err != nil {
		return nil, fmt.Errorf("re-read trashed members of series %d: %w", targetID, err)
	}
	existing, _ := targetView(live[targetID], trashed[targetID], 0)
	return &targetState{existing: existing, tracked: len(live[targetID]) + len(trashed[targetID])}, nil
}

// heldByCode counts the books the decision holds, per code, over every eligible
// plan (the canary controls do not hide it), and how many plans move books.
func (r *denumberRun) heldByCode() (map[sdn.HoldCode]int, int) {
	held := map[sdn.HoldCode]int{}
	carrying := 0
	for i := range r.plans {
		if len(r.results[i].Moves) > 0 {
			carrying++
		}
		for _, h := range r.results[i].Held {
			held[h.Code]++
		}
	}
	return held, carrying
}

// applySeriesDenumber executes the selected plans. Every write is journaled for
// undo under opID, in the order the revert needs (it runs newest first). The
// order is NOT the same for every row, and the difference matters after a crash:
//
//  1. a series_create row is written AFTER the base series was really created by
//     this run (the new row is empty; one that already holds books was somebody
//     else's and is neither used nor journaled) and BEFORE any book moves into it.
//     A crash between the create and its row leaves an empty, unjournaled series,
//     which dedup.series-prune removes; it never leaves a book in an unjournaled
//     series;
//  2. per book version, inside the ModifyBook callback, the series_id row and, when
//     the book had no position, the series_sequence row (sdn.MoveRows) are written
//     BEFORE the row is. A journal failure aborts the write and a skipped book
//     leaves no row; a row whose write then FAILED is voided (Voided +
//     RevertedAt), so a revert never acts on a move that did not happen, whatever
//     another writer did to the book since;
//  3. a series_row_delete row (snapshot of the row read again at delete time) is
//     written BEFORE an emptied numbered series is deleted, so the revert can
//     recreate it under its own id; voided when the delete fails.
//
// 🔒 The scanner stands down for the whole run (hold), and the lease is checked
// before every plan, every book and every delete.
//
// 🔒 SEQUENTIAL ON PURPOSE. Two plans folding onto one base must not both decide
// the base is missing and create it; series creation is the shared mutable state
// here (contrast dedupe-book-file-rows, where every book is disjoint). The
// position decision is made per base over the whole eligible set before this
// loop (sdn.Decide), and re-checked against the target right before the first
// write into it (sdn.Recheck), so the loop never has to compare books itself.
//
// 🔒 A book and its alternate versions move together or not at all: every version
// is checked on its fresh row (moveVersion, dry) before the first is written, and
// a version that then fails takes the earlier ones back.
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
	counter, _ := database.AsCapability[database.SeriesBookRefCounter](store)      // checked present by runSeriesDenumber
	deleter, _ := database.AsCapability[database.SeriesUnreferencedDeleter](store) // checked present by runSeriesDenumber

	targets := make(map[int]int, len(run.groups)) // group index -> base series in use
	for gi, g := range run.groups {
		if g.targetID != 0 {
			targets[gi] = g.targetID
		}
	}
	states := map[int]*targetState{} // group index -> what the base holds, read at the first write
	var outcomes []sdn.ReportRow
	var merged, plansMoving, movedBooks, created, deleted, failed, stayed, refusedByGuard, targetAppeared, heldDelete int
	appliedHeld := map[sdn.HoldCode]int{}
	receivedBase := map[int]bool{}

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
			states[gi] = &targetState{} // empty by the check above
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

		// 🔒 The base as it is NOW. The decision read it when the plan was made,
		// which is as old as every plan before this one took to apply: a book moved
		// into it, restored from the trash into it, or linked to it by a row nothing
		// lists since then holds a position this plan would take. Members are read
		// again, live and trashed, once per base before the first write into it;
		// the row count is checked again for every plan, and the books this run
		// moved in are tracked in between.
		st := states[gi]
		if st == nil {
			var rerr error
			if st, rerr = readTargetState(store, targetID); rerr != nil {
				failed++
				log.Warn("series-denumber: could not re-read the base before writing, plan skipped", "series", targetID, "err", rerr)
				continue
			}
			states[gi] = st
		}
		refs, rerr := counter.GetSeriesBookRefCount(targetID)
		if rerr != nil {
			failed++
			log.Warn("series-denumber: could not count the base's rows before writing, plan skipped", "series", targetID, "err", rerr)
			continue
		}
		unseen := max(refs-st.tracked, 0)
		keep, heldNow := sdn.Recheck(pl.IntoName, sr.Moves, st.existing, unseen)

		movedAll := true
		stay := 0
		movedHere := 0
		for _, h := range heldNow {
			appliedHeld[h.Code]++
			stayed++
			stay++
			log.Info("series-denumber: book not moved", "book", h.BookID, "why", string(h.Code)+": "+h.Reason)
			outcomes = append(outcomes, sdn.ReportRow{
				Kind: sdn.KindBook, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
				Position: pl.Position, BookID: h.BookID, NewSeriesID: strconv.Itoa(targetID),
				Status: "skipped: " + string(h.Code) + ": " + h.Reason,
			})
		}

		for _, versions := range moveGroups(keep) {
			if hold.Lost() {
				return fmt.Errorf("series-denumber: scan stand-down lease lapsed at book %s; stopped with %d books moved", versions[0].BookID, movedBooks)
			}
			rowFor := func(mv sdn.Move, status string) sdn.ReportRow {
				return sdn.ReportRow{
					Kind: sdn.KindBook, FromSeriesID: pl.FromID, FromName: pl.FromName, IntoName: pl.IntoName,
					Position: pl.Position, BookID: mv.BookID, OldSeq: sdn.SeqString(mv.OldSeq),
					NewSeriesID: strconv.Itoa(targetID), NewSeq: strconv.Itoa(mv.NewSeq), Status: status,
				}
			}
			// leave records every version of the book as not moved.
			leave := func(why string) {
				for _, mv := range versions {
					stayed++
					stay++
					outcomes = append(outcomes, rowFor(mv, "skipped: "+why))
				}
			}

			// Preflight: with more than one version, every one is checked on its
			// fresh row before the first is written, so a lock or a changed position
			// found late does not split the book across two series.
			if len(versions) > 1 {
				blocked := ""
				for _, mv := range versions {
					r := moveVersion(store, record, mv, pl.FromID, targetID, true)
					if r.err != nil {
						blocked = "its version " + mv.BookID + " could not be checked: " + r.err.Error()
						break
					}
					if r.skip != "" {
						blocked = "its version " + mv.BookID + " " + r.skip
						break
					}
				}
				if blocked != "" {
					log.Info("series-denumber: book not moved", "book", versions[0].BookID, "why", blocked)
					leave("none of its versions moved, because " + blocked)
					continue
				}
			}

			type landed struct {
				mv sdn.Move
				r  versionResult
			}
			var done []landed
			for _, mv := range versions {
				r := moveVersion(store, record, mv, pl.FromID, targetID, false)
				wrote := r.err == nil && r.written != nil && r.did
				if len(r.journaled) > 0 && !wrote {
					// The rows describe a write that did not happen (a later row failed,
					// or the book write itself did). Voided, a revert would otherwise
					// replay the old value over whatever a later writer committed.
					if verr := void(r.journaled); verr != nil {
						failed++
						log.Warn("series-denumber: could not void the journal rows of a failed write", "book", mv.BookID, "err", verr)
					}
				}
				if wrote {
					done = append(done, landed{mv, r})
					continue
				}

				// This version did not move. The earlier ones of the same book did:
				// take them back, so the book is not split across two series.
				for di := len(done) - 1; di >= 0; di-- {
					d := done[di]
					if rerr := restoreVersion(store, d.mv, pl.FromID, targetID); rerr != nil {
						// It stays in the base. It is journaled, so a revert still puts it
						// back; the run reports the split loudly and does not delete.
						failed++
						movedAll = false
						log.Error("series-denumber: a book's versions are SPLIT across two series: a later version did not move and this one could not be taken back",
							"book", d.mv.BookID, "from_series", pl.FromID, "to_series", targetID, "err", rerr)
						continue
					}
					if verr := void(d.r.journaled); verr != nil {
						failed++
						log.Warn("series-denumber: could not void the journal rows of a move that was taken back", "book", d.mv.BookID, "err", verr)
					}
					done = append(done[:di], done[di+1:]...)
				}
				switch {
				case r.err != nil:
					failed++
					movedAll = false
					log.Warn("series-denumber: ModifyBook failed", "book", mv.BookID, "err", r.err)
					outcomes = append(outcomes, rowFor(mv, "failed: "+r.err.Error()))
				case r.written == nil:
					failed++
					movedAll = false
					outcomes = append(outcomes, rowFor(mv, "failed: book not written"))
				default:
					// Nothing was written and nothing journaled; the book keeps the
					// source series referenced, so the source is not deleted.
					stayed++
					stay++
					log.Info("series-denumber: book not moved", "book", mv.BookID, "why", r.skip)
					outcomes = append(outcomes, rowFor(mv, "skipped: "+r.skip))
				}
				for _, other := range versions {
					if other.BookID == mv.BookID {
						continue
					}
					kept := false
					for _, d := range done {
						if d.mv.BookID == other.BookID {
							kept = true // could not be taken back; reported as moved below
						}
					}
					if !kept {
						stayed++
						stay++
						outcomes = append(outcomes, rowFor(other, "skipped: another version of this book did not move"))
					}
				}
				break
			}
			for _, d := range done {
				// 🔑 LOAD-BEARING: the emptiness check below reads this map. Without
				// the Move it still lists every book this plan just moved, and the
				// emptied source is never deleted.
				sid := targetID
				run.members.Move(d.mv.BookID, pl.FromID, &sid)
				st.existing = append(st.existing, bookOfMove(d.mv))
				st.tracked++
				movedBooks++
				movedHere++
				receivedBase[targetID] = true
				outcomes = append(outcomes, rowFor(d.mv, "moved"))
			}
		}
		if movedHere > 0 {
			plansMoving++
		}

		// Delete the emptied source only when every book it held moved, none was
		// held back or skipped, the unfiltered guard confirms nothing else
		// references it, and it is not a held row. The planning-time map is minutes
		// old and a book may have been linked into the series since, so the
		// references are counted AGAIN before anything is journaled (a cheap early
		// exit: a series that is plainly still referenced costs no journal row) and
		// then ONCE MORE by the delete itself. That last check is the one that
		// counts: DeleteSeriesIfUnreferenced counts and deletes under every book
		// write lock, so no book can be written into the series between the two.
		// The row is read again too: the snapshot a revert recreates it from must
		// be the row that is deleted, not the one planned, and a series renamed or
		// re-authored since the plan is somebody's edit this run does not delete
		// over. The delete is passed that row's name and author and compares them under
		// the store's lock, so a rename between this read and the delete is refused
		// too. The snapshot is journaled BEFORE the delete; a delete that then fails,
		// or that finds the series referenced, changed or held, voids the row.
		src := run.seriesByID[pl.FromID]
		if movedAll && stay == 0 && len(sr.Held) == 0 && len(heldNow) == 0 && safeToDelete &&
			len(run.members[pl.FromID]) == 0 && !database.SeriesHeld(src) {
			// The lease is checked again here: this is the last write of the plan and
			// the only one with no book loop around it to renew it.
			if hold.Lost() {
				return fmt.Errorf("series-denumber: scan stand-down lease lapsed before deleting series %d; stopped with %d books moved", src.ID, movedBooks)
			}
			cur, gerr := store.GetSeriesByID(src.ID)
			switch {
			case gerr != nil:
				failed++
				heldDelete++
				log.Warn("series-denumber: not deleting emptied series: it could not be read again", "series", src.ID, "err", gerr)
				outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: row not readable: " + gerr.Error()})
			case cur == nil:
				// Already gone: nothing to delete and nothing to journal.
				log.Info("series-denumber: emptied series is already deleted", "series", src.ID)
				outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: already deleted by someone else"})
			case cur.Name != src.Name || !sameAuthorPtr(cur.AuthorID, src.AuthorID) || database.SeriesHeld(*cur):
				heldDelete++
				log.Warn("series-denumber: not deleting emptied series: it changed since the plan was made",
					"series", src.ID, "planned_name", src.Name, "name_now", cur.Name)
				outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: the series was renamed, re-authored or held since the plan"})
			default:
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
					snap, serr := undo.NewSeriesSnapshotJSON(undo.SeriesSnapshot{ID: cur.ID, Name: cur.Name, AuthorID: cur.AuthorID})
					srcID := cur.ID
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
						gone, derr := deleter.DeleteSeriesIfUnreferenced(src.ID, cur.Name, cur.AuthorID)
						if !gone {
							// The row stays, so its undo row is voided: a revert has nothing to
							// restore.
							if verr := void([]*database.OperationChange{delRow}); verr != nil {
								log.Warn("series-denumber: could not void the delete's undo row", "series", src.ID, "err", verr)
							}
						}
						switch {
						case derr != nil && errors.Is(derr, database.ErrSeriesNotFound):
							log.Info("series-denumber: emptied series was deleted by someone else before this run's delete", "series", src.ID)
							outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: already deleted by someone else"})
						case errors.Is(derr, database.ErrSeriesChanged):
							// Renamed or re-authored after this run's re-read: the journaled
							// snapshot would not describe the row, so nothing is deleted.
							heldDelete++
							log.Warn("series-denumber: not deleting emptied series: it was renamed or re-authored at delete time", "series", src.ID, "planned_name", src.Name)
							outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: the series was renamed or re-authored at delete time"})
						case errors.Is(derr, database.ErrSeriesHeld):
							// A repair took the series between the re-read and the delete: its
							// books are linked back by an undo, so the row stays.
							heldDelete++
							log.Warn("series-denumber: not deleting emptied series: it was held at delete time", "series", src.ID, "name", src.Name)
							outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: held by a repair at delete time"})
						case derr != nil:
							// The store would not answer "unreferenced" (a memdb that is not
							// serving or is missing rows refuses rather than guess, the series
							// is held, or the write failed): the series stays, the run fails,
							// and the report says why, as it does for the count above.
							failed++
							heldDelete++
							log.Warn("series-denumber: DeleteSeriesIfUnreferenced failed", "series", src.ID, "err", derr)
							outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: the delete could not verify the series is unreferenced: " + derr.Error()})
						case !gone:
							heldDelete++
							log.Warn("series-denumber: not deleting emptied series: a book was written into it before the delete", "series", src.ID, "name", src.Name)
							outcomes = append(outcomes, sdn.ReportRow{Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "kept: a book row references it at delete time"})
						default:
							deleted++
							outcomes = append(outcomes, sdn.ReportRow{
								Kind: sdn.KindDeletedSeries, FromSeriesID: src.ID, FromName: src.Name, Status: "deleted",
							})
						}
					}
				}
			}
		}
		merged++
		if (n+1)%25 == 0 || n+1 == len(run.selected) {
			_ = reporter.UpdateProgress(n+1, len(run.selected),
				fmt.Sprintf("processed %d/%d plans (%d books moved)", merged, len(run.selected), movedBooks))
		}
	}

	heldPlan, _ := run.heldByCode()
	summary := fmt.Sprintf(
		"series-denumber: %d series scanned, %d of %d selected plans moved at least one book, %d books moved into %d base series "+
			"(%d base series created, %d emptied series deleted, %d emptied series NOT deleted because a book or a change appeared since the plan, "+
			"%d plans' sources kept by the reference guard, %d plans skipped because the base appeared mid-run, "+
			"%d books skipped at apply time: %s, failed %d) | held at plan time over every eligible plan, by reason: %s | "+
			"demoted to low, never applied: %s | undo: revert operation %s",
		run.scanned, plansMoving, len(run.selected), movedBooks, len(receivedBase),
		created, deleted, heldDelete, refusedByGuard, targetAppeared,
		stayed, formatHoldCounts(appliedHeld), failed,
		formatHoldCounts(heldPlan), run.demotedSummary(), opID)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(run.selected), len(run.selected), summary)
	if failed > 0 {
		// A run with failed writes is not a success: the op's status says so, the
		// summary above says what happened, and the journal still covers what landed.
		return fmt.Errorf("series-denumber: %d writes failed (the rest of the run is journaled and can be undone): %s", failed, summary)
	}
	return nil
}

// sameAuthorPtr compares two optional author ids.
func sameAuthorPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// demotedSummary lists the shape checks that moved plans to low, with counts:
// grammar and author holds, and the lone unpadded names held as
// position_uncorroborated.
func (r *denumberRun) demotedSummary() string {
	if len(r.byDemotion) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(r.byDemotion))
	for code, n := range r.byDemotion {
		parts = append(parts, fmt.Sprintf("%s=%d", code, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
