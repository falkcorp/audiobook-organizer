// file: internal/plugins/maintenance/series_phantom_repair.go
// version: 1.1.0
// guid: 7c2dfefe-ccbe-4a60-b69b-5baee504d537
// last-edited: 2026-09-12

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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.series-phantom-repair — SERIES-PHANTOM-REPAIR (TODO.md).
//
// A phantom series ID is a books.series_id value with no matching series row.
// Production measured 6,893 of them, held by 13,322 live books (+702 trashed),
// on 2026-08-14: every series-delete path counted "is anything still
// referencing this series?" against a FILTERED getter, deleted the row, and
// left the books pointing at nothing. Those books render with no series and
// nothing revisits them. #2908 and its successors stopped NEW phantoms (every
// delete site now consults the unfiltered database.SeriesRefCounts); this op
// is the other half — the repair of the ones already there.
//
// It is REPORT-FIRST, by design and by the TODO item's own wording: the
// default mode lists every phantom ID grouped by how many books hold it, so
// the operator can decide between the two repairs before either runs. Neither
// repair deletes anything:
//
//   - mode "null"     clears series_id on every holder (the books simply stop
//     pointing at a series that does not exist);
//   - mode "recreate" creates a series row per phantom ID from an
//     operator-supplied name and repoints the holders to it.
//     Book rows do not carry the series NAME (only the id, the
//     position and a secondary series), so the name cannot be
//     recovered from the books themselves; it has to be given.
//
// Both repairs default to dry_run=true, journal every book write to the undo
// ledger BEFORE the write (change_type metadata_update, field series_id, so
// internal/undo can replay it), respect the user's series lock, and hold the
// scan stand-down while writing. A phantom ID whose unfiltered count exceeds
// the holders this op can enumerate is reported as "unseen"; the op never
// pretends those rows were repaired.
const seriesPhantomRepairOpID = "maintenance.series-phantom-repair"

const (
	seriesPhantomModeReport   = "report"
	seriesPhantomModeNull     = "null"
	seriesPhantomModeRecreate = "recreate"

	// seriesPhantomTopGroups caps how many groups the persisted result carries.
	// The full list goes to the TSV (report_path) and the log; 6,893 groups of
	// JSON in an op result row is not a report anyone reads.
	seriesPhantomTopGroups = 200
	// seriesPhantomLogGroups caps the per-group log lines.
	seriesPhantomLogGroups = 50
	// seriesPhantomSampleTitles is how many holder titles a group carries, so a
	// reader can tell what the series probably was.
	seriesPhantomSampleTitles = 3
)

// seriesPhantomChangeType / seriesPhantomFieldName are the undo-ledger vocabulary
// for the writes this op makes: a metadata_update on series_id whose old value
// is the phantom id. The revert endpoint refuses to write such a row back
// (undo.CheckRestoreReferent: the series does not exist, and restoring it would
// recreate the dangling reference this op removed), so these rows are the audit
// record of what was cleared, not an undo path.
const (
	seriesPhantomChangeType = "metadata_update"
	seriesPhantomFieldName  = "series_id"
)

var errSeriesPhantomStandDownLost = errors.New("series-phantom-repair: scan stand-down lease lost; aborting remaining writes")

type seriesPhantomRepairParams struct {
	// Mode is "report" (default), "null" or "recreate". See the file comment.
	Mode string `json:"mode,omitempty"`
	// DryRun defaults to TRUE for the two repair modes and is ignored for
	// "report". A dry run does everything except the book writes (and, for
	// recreate, the series creates) and reports what it WOULD have done.
	DryRun *bool `json:"dry_run,omitempty"`
	// Names maps a phantom series id (as a decimal string, the JSON-object-key
	// form) to the name to recreate it under. Only consulted by "recreate";
	// phantom ids with no entry are skipped and counted.
	Names map[string]string `json:"names,omitempty"`
	// Limit caps how many BOOKS an apply writes (0 = all). A canary control; it
	// never caps the report.
	Limit int `json:"limit,omitempty"`
	// ReportPath, when set, writes every phantom id and every holder as TSV so
	// the decision between null and recreate can be made from the whole list.
	ReportPath string `json:"report_path,omitempty"`
}

// seriesPhantomHolder is one book that points at a phantom series id.
type seriesPhantomHolder struct {
	SeriesID int    `json:"series_id"`
	BookID   string `json:"book_id"`
	Title    string `json:"title"`
	AuthorID *int   `json:"author_id,omitempty"`
	Trashed  bool   `json:"trashed"`
}

// seriesPhantomGroup is one phantom series id and what holds it.
type seriesPhantomGroup struct {
	SeriesID int `json:"series_id"`
	// RefCount is the UNFILTERED reference count (database.SeriesRefCounts):
	// live, trashed and non-primary rows alike. It is the denominator.
	RefCount int `json:"ref_count"`
	Live     int `json:"live"`
	Trashed  int `json:"trashed"`
	// Unseen is RefCount minus the holders this op could enumerate — rows the
	// reference counter sees but neither book listing hydrates. Reported, never
	// repaired: a repair that claims those rows would be lying.
	Unseen       int      `json:"unseen"`
	SampleTitles []string `json:"sample_titles,omitempty"`

	holders []seriesPhantomHolder
}

type seriesPhantomRepairResult struct {
	Mode          string `json:"mode"`
	DryRun        bool   `json:"dry_run"`
	PhantomSeries int    `json:"phantom_series"`
	BooksLive     int    `json:"books_live"`
	BooksTrashed  int    `json:"books_trashed"`
	BooksUnseen   int    `json:"books_unseen"`
	// PhantomsByHolderCount is the "grouped by how many books hold each" view
	// the TODO item asks for: key "1" is phantom ids held by exactly one book,
	// "2" by two, … "10+" by ten or more.
	PhantomsByHolderCount map[string]int       `json:"phantoms_by_holder_count"`
	TopGroups             []seriesPhantomGroup `json:"top_groups"`
	ReportPath            string               `json:"report_path,omitempty"`

	// Apply-phase counters (also filled on a dry run, as "would").
	Nulled          int            `json:"nulled"`
	SeriesRecreated int            `json:"series_recreated"`
	Repointed       int            `json:"repointed"`
	Skipped         map[string]int `json:"skipped,omitempty"`
	Failed          int            `json:"failed"`
	Aborted         string         `json:"aborted,omitempty"`
}

func (p *Plugin) seriesPhantomRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              seriesPhantomRepairOpID,
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Report / repair phantom series IDs",
		Description:     "Lists every books.series_id value with no matching series row, grouped by how many books hold it (mode \"report\", the default). Two repairs, both dry-run by default and journaled to the undo ledger: mode \"null\" clears the dangling series_id on every holder; mode \"recreate\" creates a series row from an operator-supplied name (params.names[\"<id>\"]) and repoints the holders to it. Nothing is ever deleted.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  seriesPhantomRepairOpID,
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSeriesPhantomRepair,
	}
}

func (p *Plugin) runSeriesPhantomRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params seriesPhantomRepairParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("series-phantom-repair: invalid params: %w", err)
		}
	}
	result, err := p.seriesPhantomRepair(ctx, params, reporter)
	if result != nil {
		if serr := registry.ReporterSetResult(reporter, result); serr != nil {
			// A fake reporter cannot persist; the log lines already carry the
			// counts, so this is informational.
			reporter.Logger().Debug("series-phantom-repair: result not persisted", "err", serr)
		}
	}
	return err
}

// seriesPhantomRepair is the op body, split from the Run wrapper so tests can
// assert on the result struct instead of scraping log lines. It returns the
// result whenever the census completed, even when the apply then failed.
func (p *Plugin) seriesPhantomRepair(ctx context.Context, params seriesPhantomRepairParams, reporter sdk.Reporter) (*seriesPhantomRepairResult, error) {
	mode := params.Mode
	if mode == "" {
		mode = seriesPhantomModeReport
	}
	switch mode {
	case seriesPhantomModeReport, seriesPhantomModeNull, seriesPhantomModeRecreate:
	default:
		return nil, fmt.Errorf("series-phantom-repair: unknown mode %q (want %q, %q or %q)",
			mode, seriesPhantomModeReport, seriesPhantomModeNull, seriesPhantomModeRecreate)
	}
	// Dry run is the default for BOTH repairs and is not a choice for the report.
	dryRun := true
	if mode != seriesPhantomModeReport && params.DryRun != nil {
		dryRun = *params.DryRun
	}

	store := p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("series-phantom-repair: no store")
	}
	log := reporter.Logger()

	// 1. The denominator: every series id any book row references, in ANY
	// state. Fails CLOSED. A filtered count is the exact instrument that created
	// the phantoms, and this op must not measure them with it.
	refCounts, err := database.SeriesRefCounts(store)
	if err != nil {
		return nil, fmt.Errorf("series-phantom-repair: refusing to run without unfiltered reference counts: %w", err)
	}
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("series-phantom-repair: GetAllSeries: %w", err)
	}
	existing := make(map[int]struct{}, len(allSeries))
	for i := range allSeries {
		existing[allSeries[i].ID] = struct{}{}
	}

	groups := make(map[int]*seriesPhantomGroup)
	for id, n := range refCounts {
		if _, ok := existing[id]; ok {
			continue
		}
		groups[id] = &seriesPhantomGroup{SeriesID: id, RefCount: n}
	}

	result := &seriesPhantomRepairResult{
		Mode:                  mode,
		DryRun:                dryRun,
		PhantomsByHolderCount: map[string]int{},
		Skipped:               map[string]int{},
	}
	if len(groups) == 0 {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: %d series referenced, %d exist, 0 phantom ids", len(refCounts), len(allSeries)))
		return result, nil
	}

	// 2. The holders. Live rows from the completeness-guarded listing (a memdb
	// known to be short would otherwise hide holders and make a phantom look
	// smaller than it is); trashed rows from the soft-deleted lister. Anything
	// the counter sees beyond those two is "unseen" and stays reported.
	live, err := store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, fmt.Errorf("series-phantom-repair: refusing to enumerate holders from an incomplete listing: %w", err)
	}
	for i := range live {
		b := &live[i]
		if b.SeriesID == nil {
			continue
		}
		g, ok := groups[*b.SeriesID]
		if !ok {
			continue
		}
		g.Live++
		g.holders = append(g.holders, seriesPhantomHolder{SeriesID: g.SeriesID, BookID: b.ID, Title: b.Title, AuthorID: b.AuthorID})
	}
	trashed, err := store.ListSoftDeletedBooks(0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("series-phantom-repair: ListSoftDeletedBooks: %w", err)
	}
	for i := range trashed {
		b := &trashed[i]
		if b.SeriesID == nil {
			continue
		}
		g, ok := groups[*b.SeriesID]
		if !ok {
			continue
		}
		g.Trashed++
		g.holders = append(g.holders, seriesPhantomHolder{SeriesID: g.SeriesID, BookID: b.ID, Title: b.Title, AuthorID: b.AuthorID, Trashed: true})
	}

	// 3. Shape the report.
	sorted := make([]*seriesPhantomGroup, 0, len(groups))
	for _, g := range groups {
		g.Unseen = max(g.RefCount-g.Live-g.Trashed, 0)
		sort.SliceStable(g.holders, func(a, b int) bool {
			// Live before trashed, then by id, so the sample titles and the TSV
			// are stable across runs.
			if g.holders[a].Trashed != g.holders[b].Trashed {
				return !g.holders[a].Trashed
			}
			return g.holders[a].BookID < g.holders[b].BookID
		})
		for _, h := range g.holders {
			if len(g.SampleTitles) >= seriesPhantomSampleTitles {
				break
			}
			g.SampleTitles = append(g.SampleTitles, h.Title)
		}
		result.BooksLive += g.Live
		result.BooksTrashed += g.Trashed
		result.BooksUnseen += g.Unseen
		result.PhantomsByHolderCount[holderCountBucket(g.RefCount)]++
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(a, b int) bool {
		if sorted[a].RefCount != sorted[b].RefCount {
			return sorted[a].RefCount > sorted[b].RefCount
		}
		return sorted[a].SeriesID < sorted[b].SeriesID
	})
	result.PhantomSeries = len(sorted)
	for i, g := range sorted {
		if i >= seriesPhantomTopGroups {
			break
		}
		result.TopGroups = append(result.TopGroups, *g)
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"series-phantom-repair: %d phantom series id(s) held by %d live + %d trashed book(s) (%d reference(s) unseen by either listing); by holder count: %s",
		result.PhantomSeries, result.BooksLive, result.BooksTrashed, result.BooksUnseen, formatHolderHistogram(result.PhantomsByHolderCount)))
	for i, g := range sorted {
		if i >= seriesPhantomLogGroups {
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: … %d more phantom id(s); pass report_path for the full list", len(sorted)-i))
			break
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: series_id=%d refs=%d live=%d trashed=%d unseen=%d titles=%q",
			g.SeriesID, g.RefCount, g.Live, g.Trashed, g.Unseen, g.SampleTitles))
	}
	if params.ReportPath != "" {
		if werr := writeSeriesPhantomReport(params.ReportPath, sorted); werr != nil {
			// Not fatal: the counts above are still the answer. Loud, because a
			// missing TSV is the one artifact the operator asked for by name.
			log.Error("series-phantom-repair: report write failed", "path", params.ReportPath, "err", werr)
			_ = reporter.Log(slog.LevelError, fmt.Sprintf("series-phantom-repair: report NOT written to %s: %v", params.ReportPath, werr))
		} else {
			result.ReportPath = params.ReportPath
		}
	}
	if mode == seriesPhantomModeReport {
		return result, nil
	}

	// 4. Plan the repair: a flat list of (holder -> target series id), where
	// target nil means "clear it". Recreate resolves its targets first,
	// sequentially — a handful of series creates — so the per-book writes below
	// can run in parallel over disjoint books.
	type plannedWrite struct {
		holder seriesPhantomHolder
		target *int
	}
	var plan []plannedWrite
	switch mode {
	case seriesPhantomModeNull:
		for _, g := range sorted {
			for _, h := range g.holders {
				plan = append(plan, plannedWrite{holder: h})
			}
		}
	case seriesPhantomModeRecreate:
		for _, g := range sorted {
			name := strings.TrimSpace(params.Names[strconv.Itoa(g.SeriesID)])
			if name == "" {
				result.Skipped["no_name"] += len(g.holders)
				continue
			}
			authorID := majorityAuthorID(g.holders)
			var targetID int
			if existing, gerr := store.GetSeriesByName(name, authorID); gerr == nil && existing != nil {
				targetID = existing.ID
			} else if dryRun {
				// Would create. A placeholder id keeps the plan countable
				// without minting a row on a dry run.
				targetID = -1
				result.SeriesRecreated++
			} else {
				created, cerr := store.CreateSeries(name, authorID)
				if cerr != nil || created == nil {
					log.Warn("series-phantom-repair: CreateSeries failed; holders left as they are", "phantom_series_id", g.SeriesID, "name", name, "err", cerr)
					result.Failed += len(g.holders)
					continue
				}
				targetID = created.ID
				result.SeriesRecreated++
			}
			for _, h := range g.holders {
				t := targetID
				plan = append(plan, plannedWrite{holder: h, target: &t})
			}
		}
	}
	if params.Limit > 0 && len(plan) > params.Limit {
		result.Skipped["over_limit"] = len(plan) - params.Limit
		plan = plan[:params.Limit]
	}

	if dryRun {
		for _, w := range plan {
			if w.target == nil {
				result.Nulled++
			} else {
				result.Repointed++
			}
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
			"series-phantom-repair DRY RUN (mode=%s): would clear series_id on %d book(s), recreate %d series and repoint %d book(s); skipped %v. Pass {\"dry_run\": false} to apply.",
			mode, result.Nulled, result.SeriesRecreated, result.Repointed, result.Skipped))
		return result, nil
	}

	// 5. Apply. Hold the scan stand-down: a concurrent library.scan rewrites
	// the same book rows this op edits.
	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "series-phantom-repair apply")
	if sdErr != nil {
		return nil, fmt.Errorf("series-phantom-repair: could not acquire scan stand-down; refusing to write: %w", sdErr)
	}
	defer release()

	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		log.Warn("series-phantom-repair: no operation id; undo-ledger rows will be uncorrelated (replay by book_id still works)")
	}

	var mu sync.Mutex
	aborted := false
	runErr := registry.RunItems(ctx, reporter, plan, func(ctx context.Context, w plannedWrite) error {
		if scanStandDownLostForApply(p.deps, holderID, held) {
			mu.Lock()
			if !aborted {
				aborted = true
				result.Aborted = errSeriesPhantomStandDownLost.Error()
			}
			mu.Unlock()
			return errSeriesPhantomStandDownLost
		}
		outcome, ferr := seriesPhantomRepointOne(store, opID, w.holder, w.target)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case ferr != nil:
			result.Failed++
			log.Warn("series-phantom-repair: book left as it was", "book_id", w.holder.BookID, "phantom_series_id", w.holder.SeriesID, "err", ferr)
		case outcome != "":
			result.Skipped[outcome]++
		case w.target == nil:
			result.Nulled++
		default:
			result.Repointed++
		}
		return nil
	}, registry.RunItemsOptions{
		// Disjoint by construction: every plannedWrite is a different book (a
		// book has one series_id, so it appears under one phantom id once).
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeFail,
		Label: func(i, total int) string {
			return fmt.Sprintf("book %d/%d", i+1, total)
		},
	})

	if result.Nulled > 0 || result.Repointed > 0 || result.SeriesRecreated > 0 {
		// Series book counts (and, for recreate, the list itself) changed.
		p.deps.InvalidateSeriesCache()
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"series-phantom-repair APPLY (mode=%s): cleared %d, recreated %d series, repointed %d, skipped %v, failed %d%s",
		mode, result.Nulled, result.SeriesRecreated, result.Repointed, result.Skipped, result.Failed,
		map[bool]string{true: "; ABORTED: " + result.Aborted, false: ""}[result.Aborted != ""]))
	if runErr != nil && !errors.Is(runErr, errSeriesPhantomStandDownLost) {
		return result, runErr
	}
	if result.Aborted != "" {
		return result, errSeriesPhantomStandDownLost
	}
	return result, nil
}

// seriesPhantomRepointOne writes ONE book's series_id (nil = clear) with the
// three guards every write here needs, in this order:
//
//  1. re-read the row and confirm it still points at the phantom id — a book
//     edited underneath the plan is skipped, not clobbered;
//  2. apply the edit in memory through ApplyRespectingLocks — a user who
//     locked the series field keeps their value, the skip is counted, and no
//     ledger row is written for a write that will not happen;
//  3. journal the change to the undo ledger BEFORE writing — a journal that
//     fails aborts this book's write, because an unreplayable edit is worse
//     than a dangling id that survives to the next run.
//
// It returns a non-empty skip reason, or an error for a failed step.
func seriesPhantomRepointOne(store OpsStore, opID string, h seriesPhantomHolder, target *int) (skip string, err error) {
	full, err := store.GetBookByID(h.BookID)
	if err != nil {
		return "", fmt.Errorf("GetBookByID: %w", err)
	}
	if full == nil {
		return "vanished", nil
	}
	if full.SeriesID == nil || *full.SeriesID != h.SeriesID {
		return "changed_underneath", nil
	}
	kept, lerr := database.ApplyRespectingLocks(store, full, func(b *database.Book) {
		b.SeriesID = target
	})
	if lerr != nil {
		return "", fmt.Errorf("field locks unavailable, book NOT written: %w", lerr)
	}
	if slices.Contains(kept, database.FieldKeySeriesName) {
		return "locked", nil
	}
	newValue := ""
	if target != nil {
		newValue = strconv.Itoa(*target)
	}
	if jerr := store.CreateOperationChange(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		BookID:      h.BookID,
		ChangeType:  seriesPhantomChangeType,
		FieldName:   seriesPhantomFieldName,
		OldValue:    strconv.Itoa(h.SeriesID),
		NewValue:    newValue,
	}); jerr != nil {
		return "", fmt.Errorf("undo-ledger write failed, book NOT written: %w", jerr)
	}
	if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
		return "", fmt.Errorf("UpdateBook: %w", uerr)
	}
	return "", nil
}

// majorityAuthorID picks the author most of the holders share, or nil when the
// holders carry none. CreateSeries takes an author so the recreated row lands
// where the books' author page already looks for it.
func majorityAuthorID(holders []seriesPhantomHolder) *int {
	counts := map[int]int{}
	best, bestN := 0, 0
	for _, h := range holders {
		if h.AuthorID == nil {
			continue
		}
		counts[*h.AuthorID]++
		if n := counts[*h.AuthorID]; n > bestN || (n == bestN && *h.AuthorID < best) {
			best, bestN = *h.AuthorID, n
		}
	}
	if bestN == 0 {
		return nil
	}
	return &best
}

func holderCountBucket(n int) string {
	if n >= 10 {
		return "10+"
	}
	return strconv.Itoa(n)
}

func formatHolderHistogram(h map[string]int) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		ai, aerr := strconv.Atoi(keys[a])
		bi, berr := strconv.Atoi(keys[b])
		if aerr != nil || berr != nil {
			return aerr == nil // numeric keys first, "10+" last
		}
		return ai < bi
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s book(s)=%d", k, h[k]))
	}
	return strings.Join(parts, ", ")
}

// writeSeriesPhantomReport dumps EVERY phantom id and EVERY holder, one row
// per holder (a phantom id with no enumerable holder still gets one row with an
// empty book_id, so the unseen count is visible). TSV for the same reason
// writeRepointReport is: the file exists to be sorted and grepped by a person
// deciding which repair to run.
func writeSeriesPhantomReport(path string, groups []*seriesPhantomGroup) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("series_id\tref_count\tlive\ttrashed\tunseen\tbook_id\ttitle\tbook_trashed\tauthor_id\n")
	for _, g := range groups {
		if len(g.holders) == 0 {
			fmt.Fprintf(&b, "%d\t%d\t%d\t%d\t%d\t\t\t\t\n", g.SeriesID, g.RefCount, g.Live, g.Trashed, g.Unseen)
			continue
		}
		for _, h := range g.holders {
			author := ""
			if h.AuthorID != nil {
				author = strconv.Itoa(*h.AuthorID)
			}
			fmt.Fprintf(&b, "%d\t%d\t%d\t%d\t%d\t%s\t%s\t%t\t%s\n",
				g.SeriesID, g.RefCount, g.Live, g.Trashed, g.Unseen, h.BookID, clean(h.Title), h.Trashed, author)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
