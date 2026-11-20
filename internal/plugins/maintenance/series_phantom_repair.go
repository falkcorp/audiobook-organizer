// file: internal/plugins/maintenance/series_phantom_repair.go
// version: 1.5.1
// guid: 7c2dfefe-ccbe-4a60-b69b-5baee504d537
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/tasks"
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
	//
	// dryRun (camelCase) is accepted as an alias; sending both with different
	// values is refused by opmode.ResolveDryRun, never guessed at.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// Names maps a phantom series id (as a decimal string, the JSON-object-key
	// form) to the name to recreate it under. Only consulted by "recreate";
	// phantom ids with no entry are skipped and counted.
	Names map[string]string `json:"names,omitempty"`
	// Limit caps how many BOOKS an apply writes (0 = all). A canary control; it
	// never caps the report.
	Limit int `json:"limit,omitempty"`
	// ReportPath, when set, writes every phantom id and every holder as TSV so
	// the decision between null and recreate can be made from the whole list. It
	// must be a file name directly in {root_dir}/.reports (a bare name, or an
	// absolute path whose directory is that one); a name with a directory in it,
	// or a path anywhere else, is refused.
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

// seriesPhantomRepairHelp is the def's Description, which the native kind
// carries as its Help.
const seriesPhantomRepairHelp = "Lists every books.series_id value with no matching series row, grouped by how many books hold it (mode \"report\", the default). Two repairs, both dry-run by default and journaled to the undo ledger: mode \"null\" clears the dangling series_id on every holder; mode \"recreate\" creates a series row from an operator-supplied name (params.names[\"<id>\"]) and repoints the holders to it. Nothing is ever deleted."

// seriesPhantomRepairKind is rule R3's kind for the def below.
const seriesPhantomRepairKind = tasks.Kind("books.maintenance.series-phantom-repair")

func (p *Plugin) seriesPhantomRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              seriesPhantomRepairOpID,
		Preview:         sdk.PreviewSpec{Params: seriesPhantomRepairParams{}, StartCheck: seriesPhantomStartCheck},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Report / repair phantom series IDs",
		Description:     seriesPhantomRepairHelp,
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

// seriesPhantomStartCheck refuses a live start whose mode cannot write: mode
// "report" (the default when mode is left out) ignores dry_run, so a live run
// of it would answer 202 and write nothing. A preview of any mode is fine.
func seriesPhantomStartCheck(params map[string]json.RawMessage, live bool) error {
	if !live {
		return nil
	}
	mode := seriesPhantomModeReport
	if raw, ok := params["mode"]; ok {
		if err := json.Unmarshal(raw, &mode); err != nil {
			return fmt.Errorf("mode must be a string")
		}
		if mode == "" {
			mode = seriesPhantomModeReport
		}
	}
	if mode == seriesPhantomModeReport {
		return fmt.Errorf("a live run of %s needs mode %q or %q in params; mode %q only reports",
			seriesPhantomRepairOpID, seriesPhantomModeNull, seriesPhantomModeRecreate, seriesPhantomModeReport)
	}
	return nil
}

// seriesPhantomRepair is the op body, split from the Run wrapper so tests can
// assert on the result struct instead of scraping log lines. It returns the
// result whenever the census completed, even when the apply then failed.
func (p *Plugin) seriesPhantomRepair(ctx context.Context, params seriesPhantomRepairParams, reporter sdk.Reporter) (*seriesPhantomRepairResult, error) {
	mode, err := seriesPhantomMode(params.Mode)
	if err != nil {
		return nil, err
	}
	// Dry run is the default for BOTH repairs and is not a choice for the report.
	// The two spellings are resolved for every mode, so a body that states them
	// with different values is refused even where the flag is then ignored.
	dryRun, modeErr := opmode.ResolveDryRun(seriesPhantomRepairOpID, params.DryRun, params.DryRunCamel)
	if modeErr != nil {
		return nil, modeErr
	}

	store := p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("series-phantom-repair: no store")
	}
	env := seriesPhantomRepairEnv{
		t6g7Env: t6g7HeadEnv(reporter, !dryRun),
		Store:   seriesPhantomHeadReads{store},
		Writer:  store,
		ResolveReport: func(explicit string) (string, error) {
			return p.resolveReportPath(explicit, opReportFileName(reporter, "series-phantom-repair"))
		},
		InvalidateSeriesCache: p.deps.InvalidateSeriesCache,
	}
	env.StandDown = t6g7HeadStandDown(p.deps, reporter)
	return seriesPhantomRepairCore(ctx, mode, dryRun, params, env)
}

// seriesPhantomMode is params.mode with its default, refusing an unknown one.
func seriesPhantomMode(mode string) (string, error) {
	if mode == "" {
		mode = seriesPhantomModeReport
	}
	switch mode {
	case seriesPhantomModeReport, seriesPhantomModeNull, seriesPhantomModeRecreate:
		return mode, nil
	}
	return "", fmt.Errorf("series-phantom-repair: unknown mode %q (want %q, %q or %q)",
		mode, seriesPhantomModeReport, seriesPhantomModeNull, seriesPhantomModeRecreate)
}

// seriesPhantomReads is every read the op makes.
type seriesPhantomReads interface {
	// GetAllSeriesBookRefCounts is the UNFILTERED series reference count
	// (database.SeriesBookRefStore): no filtered stand-in exists, and a store that
	// cannot answer it fails the run.
	GetAllSeriesBookRefCounts() (map[int]int, error)
	GetAllSeries() ([]database.Series, error)
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	GetBookByID(id string) (*database.Book, error)
	database.MetadataFieldStateReader
}

// seriesPhantomWriter is the op's three writes: a recreated series row, a book's
// series_id under the book's write lock, and the undo-ledger row that precedes it.
type seriesPhantomWriter interface {
	CreateSeries(name string, authorID *int) (*database.Series, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	CreateOperationChange(change *database.OperationChange) error
}

// seriesPhantomRepointReads is what ONE book's repoint reads: the row and its
// field locks. The ledger row and the book write go through the run's writer,
// so the repoint takes the two sides separately and never forwards a journal
// write (the journal-direct-writers guard).
type seriesPhantomRepointReads interface {
	GetBookByID(id string) (*database.Book, error)
	database.MetadataFieldStateReader
}

// seriesPhantomHeadReads is HEAD's reads: the unfiltered count resolves through
// the store's capability (database.SeriesRefCounts), which fails closed.
type seriesPhantomHeadReads struct {
	seriesPhantomHeadBase
}

// seriesPhantomHeadBase is the reads HEAD's store answers directly.
type seriesPhantomHeadBase interface {
	GetAllSeries() ([]database.Series, error)
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	GetBookByID(id string) (*database.Book, error)
	database.MetadataFieldStateReader
}

func (s seriesPhantomHeadReads) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return database.SeriesRefCounts(s.seriesPhantomHeadBase)
}

// seriesPhantomRepairEnv is what the core takes from its shell.
type seriesPhantomRepairEnv struct {
	t6g7Env
	Store seriesPhantomReads
	// Writer is nil in a dry run (HEAD) or a Preview run (native): the plan is
	// built and counted, nothing is created or written.
	Writer seriesPhantomWriter
	// ResolveReport confines params.report_path to the run's report directory.
	ResolveReport func(explicit string) (string, error)
	// InvalidateSeriesCache drops the series list and book-count caches after a
	// write changed them.
	InvalidateSeriesCache func()
}

// seriesPhantomRepairParallelism is the apply loop's concurrency, one worker per
// core as HEAD's pool ran. Every item is a different book (a book has one
// series_id), so no two workers write the same row.
func seriesPhantomRepairParallelism() tasks.Parallelism { return tasks.CPU() }

// seriesPhantomRepairNative is the native kind. Preview is the dry run HEAD's
// default was; Live applies mode "null" or "recreate". Mode "report" only
// reports, so a Live run of it is refused as HEAD's start check refused it.
func (p *Plugin) seriesPhantomRepairNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(seriesPhantomRepairKind, sched)
	c.Title = "Report / repair phantom series IDs"
	c.Help = seriesPhantomRepairHelp
	c.Effects = tasks.Writes(t6g7Res(resources.Union(t6WriterResources["CreateSeries"],
		t6WriterResources["ModifyBook"], t6WriterResources["CreateOperationChange"],
		[]ops.Resource{ops.ResFiles})...)...)
	// HEAD: ResumeRestart, and the body reads no checkpoint: the census and the
	// plan are rebuilt from the store, and a book already repointed no longer
	// holds a phantom id.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.series-phantom-repair")
	c.Timeout = 2 * time.Hour
	return tasks.Task(seriesPhantomRepairKind, tasks.TaskSpec[json.RawMessage, *seriesPhantomRepairResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (*seriesPhantomRepairResult, error) {
			params, err := t6g7DecodeNative[seriesPhantomRepairParams](seriesPhantomRepairKind, raw)
			if err != nil {
				return nil, err
			}
			mode, err := seriesPhantomMode(params.Mode)
			if err != nil {
				return nil, err
			}
			live := rc.Mode() == tasks.ModeLive
			if live && mode == seriesPhantomModeReport {
				return nil, fmt.Errorf("a live run of %s needs mode %q or %q in params; mode %q only reports",
					seriesPhantomRepairOpID, seriesPhantomModeNull, seriesPhantomModeRecreate, seriesPhantomModeReport)
			}
			for _, d := range []struct {
				name string
				set  bool
			}{{"Store", deps.Store != nil}, {"ResolveReportPath", deps.ResolveReportPath != nil},
				{"InvalidateSeriesCache", deps.InvalidateSeriesCache != nil}} {
				if err := t6g7NeedDep(seriesPhantomRepairKind, d.name, d.set); err != nil {
					return nil, err
				}
			}
			env := seriesPhantomRepairEnv{
				t6g7Env: t6g7NativeEnv(rc),
				Store:   deps.Store,
				ResolveReport: func(explicit string) (string, error) {
					return deps.ResolveReportPath(explicit, "series-phantom-repair-"+string(rc.RunID())+".tsv")
				},
				InvalidateSeriesCache: deps.InvalidateSeriesCache,
			}
			if live {
				w, err := t6g7NewWriter(seriesPhantomRepairKind, deps, rc)
				if err != nil {
					return nil, err
				}
				env.Writer = w
				env.StandDown = t6g7NativeStandDown(rc, deps.StandDownKey)
			}
			return seriesPhantomRepairCore(rc.Context(), mode, !live, params, env)
		},
	})
}

// seriesPhantomRepairCore is the body of seriesPhantomRepair; mode is validated
// and dryRun decided by the shell (a report is always a dry run).
func seriesPhantomRepairCore(ctx context.Context, mode string, dryRun bool, params seriesPhantomRepairParams, env seriesPhantomRepairEnv) (*seriesPhantomRepairResult, error) {
	if mode == seriesPhantomModeReport {
		dryRun = true
	}
	if env.Store == nil {
		return nil, fmt.Errorf("series-phantom-repair: no store")
	}
	if !dryRun && env.Writer == nil {
		return nil, fmt.Errorf("series-phantom-repair: no writer for a live run")
	}

	// The caller's report_path is confined to {root_dir}/.reports before any read,
	// so a path that would escape it is refused with nothing done.
	if params.ReportPath != "" {
		resolved, rpErr := env.ResolveReport(params.ReportPath)
		if rpErr != nil {
			return nil, fmt.Errorf("series-phantom-repair: %w", rpErr)
		}
		params.ReportPath = resolved
	}

	// 1. The denominator: every series id any book row references, in ANY
	// state. Fails CLOSED. A filtered count is the exact instrument that created
	// the phantoms, and this op must not measure them with it.
	refCounts, err := env.Store.GetAllSeriesBookRefCounts()
	if err != nil {
		return nil, fmt.Errorf("series-phantom-repair: refusing to run without unfiltered reference counts: %w", err)
	}
	allSeries, err := env.Store.GetAllSeries()
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
		env.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: %d series referenced, %d exist, 0 phantom ids", len(refCounts), len(allSeries)))
		return result, nil
	}

	// 2. The holders. Live rows from the completeness-guarded listing (a memdb
	// known to be short would otherwise hide holders and make a phantom look
	// smaller than it is); trashed rows from the soft-deleted lister. Anything
	// the counter sees beyond those two is "unseen" and stays reported.
	live, err := env.Store.GetAllBooksCoreComplete(0, 0)
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
	trashed, err := env.Store.ListSoftDeletedBooks(0, 0, nil)
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

	env.Log(slog.LevelInfo, fmt.Sprintf(
		"series-phantom-repair: %d phantom series id(s) held by %d live + %d trashed book(s) (%d reference(s) unseen by either listing); by holder count: %s",
		result.PhantomSeries, result.BooksLive, result.BooksTrashed, result.BooksUnseen, formatHolderHistogram(result.PhantomsByHolderCount)))
	for i, g := range sorted {
		if i >= seriesPhantomLogGroups {
			env.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: … %d more phantom id(s); pass report_path for the full list", len(sorted)-i))
			break
		}
		env.Log(slog.LevelInfo, fmt.Sprintf("series-phantom-repair: series_id=%d refs=%d live=%d trashed=%d unseen=%d titles=%q",
			g.SeriesID, g.RefCount, g.Live, g.Trashed, g.Unseen, g.SampleTitles))
	}
	if params.ReportPath != "" {
		if werr := writeSeriesPhantomReport(params.ReportPath, sorted); werr != nil {
			// Not fatal: the counts above are still the answer. Loud, because a
			// missing TSV is the one artifact the operator asked for by name.
			env.Logger.Error("series-phantom-repair: report write failed", "path", params.ReportPath, "err", werr)
			env.Log(slog.LevelError, fmt.Sprintf("series-phantom-repair: report NOT written to %s: %v", params.ReportPath, werr))
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
			if existing, gerr := env.Store.GetSeriesByName(name, authorID); gerr == nil && existing != nil {
				targetID = existing.ID
			} else if dryRun {
				// Would create. A placeholder id keeps the plan countable
				// without minting a row on a dry run.
				targetID = -1
				result.SeriesRecreated++
			} else {
				created, cerr := env.Writer.CreateSeries(name, authorID)
				if cerr != nil || created == nil {
					env.Logger.Warn("series-phantom-repair: CreateSeries failed; holders left as they are", "phantom_series_id", g.SeriesID, "name", name, "err", cerr)
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
		env.Log(slog.LevelInfo, fmt.Sprintf(
			"series-phantom-repair DRY RUN (mode=%s): would clear series_id on %d book(s), recreate %d series and repoint %d book(s); skipped %v. Pass {\"dry_run\": false} to apply.",
			mode, result.Nulled, result.SeriesRecreated, result.Repointed, result.Skipped))
		return result, nil
	}

	// 5. Apply. Hold the scan stand-down: a concurrent library.scan rewrites
	// the same book rows this op edits.
	if env.StandDown == nil {
		return nil, fmt.Errorf("series-phantom-repair: no scan stand-down available; refusing to write")
	}
	hold, sdErr := env.StandDown(ctx, "series-phantom-repair apply")
	if sdErr != nil {
		return nil, fmt.Errorf("series-phantom-repair: could not acquire scan stand-down; refusing to write: %w", sdErr)
	}
	defer hold.Release()

	opID := env.OpID
	if opID == "" {
		env.Logger.Warn("series-phantom-repair: no operation id; undo-ledger rows will be uncorrelated (replay by book_id still works)")
	}

	var mu sync.Mutex
	aborted := false
	runErr := env.Items(ctx, seriesPhantomRepairParallelism(), len(plan), t6g7ItemsOptions{
		// Disjoint by construction: every plannedWrite is a different book (a
		// book has one series_id, so it appears under one phantom id once).
		PureLabel: func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
	}, func(ctx context.Context, i int) error {
		w := plan[i]
		if hold.Lost() {
			mu.Lock()
			if !aborted {
				aborted = true
				result.Aborted = errSeriesPhantomStandDownLost.Error()
			}
			mu.Unlock()
			return errSeriesPhantomStandDownLost
		}
		outcome, ferr := seriesPhantomRepointOne(env.Store, env.Writer, opID, w.holder, w.target)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case ferr != nil:
			result.Failed++
			env.Logger.Warn("series-phantom-repair: book left as it was", "book_id", w.holder.BookID, "phantom_series_id", w.holder.SeriesID, "err", ferr)
		case outcome != "":
			result.Skipped[outcome]++
		case w.target == nil:
			result.Nulled++
		default:
			result.Repointed++
		}
		return nil
	})

	if result.Nulled > 0 || result.Repointed > 0 || result.SeriesRecreated > 0 {
		// Series book counts (and, for recreate, the list itself) changed.
		env.InvalidateSeriesCache()
	}
	env.Log(slog.LevelInfo, fmt.Sprintf(
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
func seriesPhantomRepointOne(store seriesPhantomRepointReads, w seriesPhantomWriter, opID string, h seriesPhantomHolder, target *int) (skip string, err error) {
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
	locks, lerr := database.LoadFieldLocks(store, full.ID)
	if lerr != nil {
		return "", fmt.Errorf("field locks unavailable, book NOT written: %w", lerr)
	}
	mutate := func(b *database.Book) { b.SeriesID = target }
	if slices.Contains(locks.Apply(full, mutate), database.FieldKeySeriesName) {
		return "locked", nil
	}
	newValue := ""
	if target != nil {
		newValue = strconv.Itoa(*target)
	}
	// Write only SeriesID, under the book's write lock (ModifyBook), so a
	// column another writer commits meanwhile is not reverted (audit A1#15).
	// The phantom check and the lock check are re-made on the fresh row.
	//
	// The undo-ledger row is written INSIDE the callback, after both checks
	// and before the book: a skip leaves no ledger row (a row for a write
	// that never happened would replay the phantom id over whatever the
	// concurrent writer committed), and a ledger failure aborts ModifyBook
	// so the book is NOT written. CreateOperationChange is one synced Pebble
	// batch (the opchange row plus its opchange_by_book: index entry) under
	// the read side of the journal lock; it takes no book stripe and writes
	// no book, so calling it while ModifyBook holds this book's stripe keeps
	// the store's lock order (book stripe, then journal lock).
	written, uerr := w.ModifyBook(full.ID, func(cur *database.Book) error {
		if cur.SeriesID == nil || *cur.SeriesID != h.SeriesID {
			skip = "changed_underneath"
			return database.ErrSkipBookWrite
		}
		if slices.Contains(locks.Apply(cur, mutate), database.FieldKeySeriesName) {
			skip = "locked"
			return database.ErrSkipBookWrite
		}
		if jerr := database.RecordOperationChange(w, &database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      h.BookID,
			ChangeType:  seriesPhantomChangeType,
			FieldName:   seriesPhantomFieldName,
			OldValue:    strconv.Itoa(h.SeriesID),
			NewValue:    newValue,
		}); jerr != nil {
			return fmt.Errorf("undo-ledger write failed, book NOT written: %w", jerr)
		}
		return nil
	})
	if uerr != nil {
		return "", fmt.Errorf("ModifyBook: %w", uerr)
	}
	if written == nil {
		return "vanished", nil
	}
	return skip, nil
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
	return library.WriteReportFile(path, []byte(b.String()), library.FileMode)
}
