// file: internal/plugins/maintenance/rewrite_path_prefix.go
// version: 2.3.0
// guid: 584360e3-4976-406c-b4d1-80bbe47ed390
// last-edited: 2026-09-26

// Package maintenance — REWRITE a stored path PREFIX across every row that
// records a location under it, after a directory was renamed or moved on disk.
//
// 🔴 THIS OP NEVER DELETES A ROW. Like maintenance.missing-file-repoint it only
// rewrites path fields, and it refuses rather than writes whenever the new path
// is not provably free and present.
//
// WHY IT EXISTS. Renaming a parent directory on disk orphans every stored path
// under it, and nothing in the codebase could repair that:
//
//   - maintenance.missing-file-repoint only derives candidates INSIDE the row's
//     own recorded directory (the track-slash flattening shape), so a renamed
//     PARENT yields "no-candidate-bytes" for every row beneath it.
//   - a library scan, the other thing that re-discovers paths, is banned in this
//     deployment (see CLAUDE.md).
//
// So a misspelled library folder is unfixable: renaming it to the correct
// spelling would strand every row under it, and leaving it misspelled keeps the
// books from matching their author. This op is the missing third route — a
// mechanical, reversible, boundary-safe prefix substitution.
//
//	old_prefix  /books/Christopher Paolin - The Inheritance Cycle
//	new_prefix  /books/Christopher Paolini - The Inheritance Cycle
//	            ↓
//	book.file_path, book.source_import_path, book_file.file_path all follow.
//
// FIELDS REWRITTEN, and why the list is these and not others. The rule is
// "where the bytes are NOW gets rewritten; where the bytes CAME FROM is
// provenance and rewriting it would falsify the record":
//
//	Book.FilePath          the book's folder (multi-file) or file (single-file).
//	Book.SourceImportPath  NOT pure provenance: CountBooksByPathPrefix reads it
//	                       as a live query input (see its declaration), so
//	                       leaving it stale after a rename breaks that count.
//	BookFile.FilePath      the per-file location.
//
// Deliberately NOT rewritten:
//
//	BookFile.DelugeOriginalPath, BookFile.OriginalFilename,
//	Book.OriginalFilename — provenance: they describe where a file came from
//	before it entered the library, which a rename inside the library does not
//	change.
//	Book.ITunesPath (deprecated), BookFile.ITunesPath — these live under the
//	iTunes root, so a correctly-scoped old_prefix never matches them. They are
//	not special-cased; the scoping already excludes them.
//
// DERIVED STATE IS NOT TOUCHED BY HAND, and that is the completeness argument.
// Every write goes through ModifyBook / ModifyBookFile, and each store-side
// commit rewrites what it owns:
//
//	book_atpath:<path>\x00<id>     updateBookLockedMode (pebble_store.go): deletes
//	                               the old key and sets the new one on a FilePath
//	                               change. Proved by TestRewritePathPrefix_Apply…
//	                               AtPathIndexFollows, which asserts
//	                               LiveBookIDsAtPath answers at the NEW path and
//	                               no longer at the old one.
//	book_path:<path>               same commit, but with a caveat worth stating
//	                               exactly: setBookPathKeyIfFree SILENTLY NO-OPS
//	                               when the target key already belongs to another
//	                               live book (it warns and leaves the key with the
//	                               incumbent, because refusing the write would
//	                               lose a book for a scan or import). So this
//	                               index follows only when the key is free. That
//	                               is not a gap for THIS op: the same condition —
//	                               another live book at the new path — is exactly
//	                               what the collision gate refuses via
//	                               LiveBookIDsAtPath before any write, so a book
//	                               this op rewrites has already been shown to have
//	                               a free path. The one way the two can disagree
//	                               is a book_atpath: index that is itself stale,
//	                               which maintenance.book-atpath-index-verify
//	                               exists to detect and the backfill to repair.
//	book_file_path:<crc32hex>      updateBookFileLocked, which drops stale
//	                               secondary indexes and writes new ones on a
//	                               path change.
//	memdb projection               UpsertBookToMemDB, in the same write path.
//	Bleve search index             indexedStore.ModifyBook re-enqueues the doc.
//	scan cache                     has NO separate keyspace: GetScanCacheMap is
//	                               built FROM book_file rows (its doc comment says
//	                               so, and forbids reading Book.FilePath), so the
//	                               cache follows the rows for free.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// rewritePathPrefixDefaultMax bounds how many BOOKS one run will rewrite, so a
// first production run is a sample rather than a whole-tree leap. 0 means this.
const rewritePathPrefixDefaultMax = 500

// rewritePathPrefixConcurrency sizes the stat sweep and the write pool. Matches
// missingFileStatConcurrency's rationale: the work is filesystem-I/O bound, not
// CPU bound, so it is sized above NumCPU.
const rewritePathPrefixConcurrency = 24

type rewritePathPrefixParams struct {
	// OldPrefix / NewPrefix are the substitution. Both required, both must be
	// absolute, and they must differ. The match is on a path-component
	// boundary (pathutil.CutPathPrefix), so "/mnt/x/ab" never matches
	// "/mnt/x/abooks/…".
	OldPrefix string `json:"old_prefix"`
	NewPrefix string `json:"new_prefix"`

	// DryRun defaults to TRUE and is a *bool for exactly that reason: a plain
	// bool zero-values to false, which would make an omitted key mean APPLY.
	// The sibling ops spell this the other way round ("apply", default false);
	// the divergence is deliberate and safe because decodeStrictParams rejects
	// unknown keys, so a habitual {"apply": true} fails loudly here instead of
	// silently not writing.
	DryRun *bool `json:"dry_run"`

	// RequireTargetExists, default TRUE, refuses to rewrite a row unless the
	// NEW path exists on disk. The intended operator sequence is: rename on
	// disk → dry run → apply, so the stat is available and is the strongest
	// available proof that the rewrite points at real bytes.
	//
	// ⚠️ A dry run executed BEFORE the rename reports every row as
	// "target-missing". That is the check working, not the op being broken.
	//
	// Set false only when the files are known-moved but not visible to this
	// process (e.g. a remote mount that is not mounted here). Doing so removes
	// the only evidence that new_prefix was typed correctly, so it is opt-in.
	RequireTargetExists *bool `json:"require_target_exists"`

	// Max bounds how many BOOKS one run rewrites. <=0 uses the default.
	Max int `json:"max"`

	// ReportPath overrides where the per-row TSV lands. Empty derives one under
	// {root_dir}/.reports/. Written on EVERY run, dry or applied.
	ReportPath string `json:"report_path,omitempty"`
}

func (p rewritePathPrefixParams) dryRun() bool {
	return p.DryRun == nil || *p.DryRun
}

func (p rewritePathPrefixParams) requireTargetExists() bool {
	return p.RequireTargetExists == nil || *p.RequireTargetExists
}

// validate rejects a substitution that cannot be meant, BEFORE any store read.
func (p rewritePathPrefixParams) validate() error {
	old, nw := p.OldPrefix, p.NewPrefix
	if strings.TrimSpace(old) == "" || strings.TrimSpace(nw) == "" {
		return fmt.Errorf("old_prefix and new_prefix are both required (got %q → %q)", old, nw)
	}
	if !filepath.IsAbs(old) {
		return fmt.Errorf("old_prefix %q is not absolute; a relative prefix would match "+
			"nothing (stored paths are absolute) or match by accident", old)
	}
	if !filepath.IsAbs(nw) {
		return fmt.Errorf("new_prefix %q is not absolute", nw)
	}
	if old == nw {
		return fmt.Errorf("old_prefix and new_prefix are identical (%q): nothing to rewrite", old)
	}
	// NESTED prefixes are refused. old=/books/A with new=/books/A/sub is not
	// idempotent — the rewritten paths still match old_prefix, so a second run
	// moves them again — and it breaks the swap-reversal this op relies on as its
	// undo, because the swapped pair would re-match its own output. The reverse
	// nesting (new inside old) has the same defect from the other side.
	co, cn := filepath.Clean(old), filepath.Clean(nw)
	if pathutil.IsWithin(cn, co) || pathutil.IsWithin(co, cn) {
		return fmt.Errorf("old_prefix %q and new_prefix %q are nested: a rewritten path would still "+
			"match the prefix it was rewritten from, so the run is neither idempotent nor reversible "+
			"by swapping the two", old, nw)
	}
	return nil
}

// rewriteFieldChange is one field of one row moving from old to new.
type rewriteFieldChange struct {
	Field string `json:"field"` // "book.file_path", "book.source_import_path", "book_file.file_path"
	RowID string `json:"row_id"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// rewriteBookPlan is the unit of work: ONE BOOK and every field under it.
//
// The partition is by book, not by row, on purpose (CLAUDE.md concurrency rule
// for order-sensitive work): a book row write and its book_file writes both take
// the book's stripe (updateBookFileLocked's post-commit aggregate recompute runs
// under lockBook), so two workers sharing a book would serialise on that stripe
// and interleave their aggregate recomputes. One book per item makes the worker
// sets disjoint, and makes the reported outcome one coherent unit: a book is
// rewritten whole or refused whole, never half.
type rewriteBookPlan struct {
	BookID  string
	Changes []rewriteFieldChange
	// Bucket is the decision. "rewritable" is the only one that writes.
	Bucket string
	Reason string
	// CauseRowID is the row whose check produced Bucket/Reason. checkRewriteBook
	// short-circuits on the FIRST failing field, so the verdict is book-level
	// while the report has one line per FIELD. Without this, recordBook stamped
	// the same reason onto every line, and a reason that names a path ("file
	// does not exist on disk: .../001.mp3") sat next to rows for 002, 003, ...
	// Seen for real on prod 2026-09-20: every refused line of the Paolini
	// rename dry run cited file 001 whatever file the line was about, which
	// would send anyone debugging a genuine per-file refusal to the wrong file.
	// Empty when the verdict came from a book-level rule that names no path
	// (the run cap, or "would rewrite").
	//
	// CauseField is part of the key, not decoration: "book.file_path" and
	// "book.source_import_path" BOTH carry the book's own ID as RowID, so a
	// RowID alone matches two different report lines of the same book.
	CauseRowID string
	CauseField string
}

type rewritePathPrefixPlan struct {
	DryRun              bool `json:"dry_run"`
	RequireTargetExists bool `json:"require_target_exists"`

	ScannedBooks     int `json:"scanned_books"`
	ScannedBookFiles int `json:"scanned_book_files"`
	MatchedBooks     int `json:"matched_books"`
	MatchedFields    int `json:"matched_fields"`

	TargetMissing   int `json:"target_missing"`
	TargetCollision int `json:"target_collision"`
	Unreadable      int `json:"unreadable"`
	Rewritable      int `json:"rewritable"`
	// SourceImportOnly counts the rewritable books whose ONLY matched field is
	// source_import_path: no file or folder changes location, and nothing about
	// them was stat- or collision-checked because nothing about them could be.
	// Reported apart so an operator approving an apply knows which population
	// they are approving.
	SourceImportOnly int `json:"source_import_only"`

	BooksRewritten  int `json:"books_rewritten"`
	FieldsRewritten int `json:"fields_rewritten"`
	SkippedChanged  int `json:"skipped_changed_underneath"`
	SkippedGone     int `json:"skipped_row_gone"`
	UpdateErrs      int `json:"update_errs"`
	CappedAt        int `json:"capped_at,omitempty"`

	ReportPath string `json:"report_path,omitempty"`

	// Samples is a STRATIFIED sample (up to samplesPerBucket per bucket) for the
	// JSON log line, for the reason repointPlan.Samples documents: a sample keyed
	// by arrival order describes the iteration, not the population.
	Samples []rewriteDecision `json:"samples,omitempty"`

	all           []rewriteDecision
	bucketSampled map[string]int
}

// rewriteDecision is one FIELD's outcome, which is the grain the report needs:
// "which stored fields changed" is the question this op exists to answer.
type rewriteDecision struct {
	Bucket string `json:"bucket"`
	BookID string `json:"book_id"`
	Field  string `json:"field"`
	RowID  string `json:"row_id"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Reason string `json:"reason"`
}

func (p *rewritePathPrefixPlan) record(d rewriteDecision) {
	p.all = append(p.all, d)
	const samplesPerBucket = 8
	if p.bucketSampled == nil {
		p.bucketSampled = map[string]int{}
	}
	if p.bucketSampled[d.Bucket] < samplesPerBucket {
		p.bucketSampled[d.Bucket]++
		p.Samples = append(p.Samples, d)
	}
}

func (p rewritePathPrefixPlan) summary() string {
	mode := "DRY RUN"
	if !p.DryRun {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s scanned=%d books/%d files | matched books=%d fields=%d | rewritable=%d (source-import-only=%d) "+
			"rewrote books=%d fields=%d | refused: target-missing=%d collision=%d unreadable=%d | "+
			"skipped: changed-underneath=%d row-gone=%d | update_errs=%d",
		mode, p.ScannedBooks, p.ScannedBookFiles, p.MatchedBooks, p.MatchedFields,
		p.Rewritable, p.SourceImportOnly, p.BooksRewritten, p.FieldsRewritten,
		p.TargetMissing, p.TargetCollision, p.Unreadable,
		p.SkippedChanged, p.SkippedGone, p.UpdateErrs)
}

// rewritePathPrefixStore is the narrow store this op needs. Every method is on
// database.Store, so no AsCapability unwrap is needed and nothing silently
// misses against production's indexedStore decorator.
type rewritePathPrefixStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	RecordPathChange(change *database.BookPathChange) error
}

func (p *Plugin) rewritePathPrefixDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.rewrite-path-prefix",
		Plugin:      "maintenance",
		DisplayName: "Rewrite a stored path prefix after a folder rename",
		Description: "Substitutes old_prefix → new_prefix in book.file_path, book.source_import_path " +
			"and book_file.file_path for every row filed under old_prefix, matching on a path-component " +
			"boundary. NEVER deletes a row. Refuses any book whose new path is already claimed by another " +
			"live book (or another book_file row) or does not exist on disk. DEFAULT DRY RUN: pass " +
			"{\"dry_run\": false} to write.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.rewrite-path-prefix",
		// Declared write-set (dispatcher Gate 3b). BOTH resources: unlike
		// missing-file-repoint, which touches only book_file rows, this op also
		// rewrites Book.FilePath, so declaring only ResBookFiles would let it run
		// beside a book-writing op.
		Writes: []sdk.Resource{sdk.ResBooks, sdk.ResBookFiles},
		// ResumeDrop, matching missing-file-repoint: an apply interrupted midway
		// must not silently pick itself back up. Re-running is cheap and safe — a
		// rewritten row no longer matches old_prefix, so it is simply not selected
		// again, which is also what makes "re-run after a partial apply completes
		// the rest" true.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		Cancellable:  true,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runRewritePathPrefix(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runRewritePathPrefix(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params rewritePathPrefixParams
	if err := decodeStrictParams(rawParams, &params); err != nil {
		return fmt.Errorf("rewrite-path-prefix: decode params: %w", err)
	}
	if err := params.validate(); err != nil {
		return fmt.Errorf("rewrite-path-prefix: %w", err)
	}
	// The frozen iTunes tree is off limits to every path-writing op (same guard
	// and same fail-closed stance as repoint-unrecorded-renames). Checked on the
	// PREFIXES, which is enough: every row this op touches is under old_prefix
	// and every path it writes is under new_prefix, so neither side can reach
	// into an iTunes root once both prefixes are outside one.
	roots, itErr := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if itErr != nil {
		return fmt.Errorf("rewrite-path-prefix: %w", itErr)
	}
	if prefixTouchesITunes(params.OldPrefix, roots) || prefixTouchesITunes(params.NewPrefix, roots) {
		return fmt.Errorf("rewrite-path-prefix: refusing — %q or %q is inside an iTunes library root; "+
			"the iTunes tree is frozen and its rows are never rewritten by a maintenance op",
			params.OldPrefix, params.NewPrefix)
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	// Resolve the report path before any work: a run that could not record its
	// decisions must fail with nothing done, not apply and then lose its record.
	reportPath, rpErr := p.resolveReportPath(params.ReportPath, opReportFileName(reporter, "rewrite-path-prefix"))
	if rpErr != nil {
		return fmt.Errorf("rewrite-path-prefix: %w", rpErr)
	}

	plan, err := planRewritePathPrefix(ctx, store, p.deps, params, reporter)
	log := reporter.Logger()

	// Write the report BEFORE returning any error, so a run that aborts mid-apply
	// still leaves the artifact behind — the run where an operator most needs it.
	if err == nil || len(plan.all) > 0 {
		if wErr := writeRewritePathPrefixReport(reportPath, plan.all); wErr != nil {
			log.Error("rewrite-path-prefix: FAILED to write the per-row report",
				"path", reportPath, "err", wErr, "rows", len(plan.all))
		} else {
			plan.ReportPath = reportPath
			log.Info("rewrite-path-prefix: per-row report written", "path", reportPath, "rows", len(plan.all))
		}
	}
	if err != nil {
		return err
	}
	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("rewrite-path-prefix report (JSON)", "report", string(b))
	}
	if plan.TargetCollision > 0 {
		log.Warn("rewrite-path-prefix: books REFUSED because the new path is already claimed by a "+
			"different row. Group the report's collision rows by new path to see which rows collide.",
			"books", plan.TargetCollision, "report", reportPath)
	}
	if plan.TargetMissing > 0 && params.requireTargetExists() {
		log.Warn("rewrite-path-prefix: books REFUSED because the new path does not exist on disk. If the "+
			"rename has not happened yet, this is the expected result of a dry run — rename first, then re-run.",
			"books", plan.TargetMissing, "report", reportPath)
	}
	if plan.CappedAt > 0 {
		log.Warn("rewrite-path-prefix: more rewritable books than the cap — run again to continue",
			"cap", plan.CappedAt, "rewritable", plan.Rewritable)
	}
	log.Info("rewrite-path-prefix complete", "summary", plan.summary())
	return nil
}

// prefixTouchesITunes reports whether a SWEEP PREFIX can reach an iTunes root.
//
// underITunes alone answers only "is p inside a root", which is the wrong
// question for a prefix: `old_prefix = /mnt/bigdata/books` with an iTunes root
// at /mnt/bigdata/books/itunes is NOT inside the root, yet it sweeps every row
// under it — the standing "never mutate books/itunes/**" invariant, violated by
// a prefix that passed the guard. So containment is tested BOTH ways: the
// prefix inside a root, and a root inside the prefix.
//
// Paths are compared cleaned on both sides, since a root from config and a
// prefix from an operator will not agree on trailing separators.
func prefixTouchesITunes(p string, roots []string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	if underITunes(p, roots) {
		return true
	}
	clean := filepath.Clean(p)
	if pathutil.UnderFrozenITunesTree(clean) {
		return true
	}
	for _, root := range roots {
		// The other direction: does this prefix CONTAIN an iTunes root?
		if pathutil.IsWithin(filepath.Clean(root), clean) {
			return true
		}
	}
	return false
}

// rewriteTarget is old→new for one field, with the boundary already applied.
func rewriteTarget(old, oldPrefix, newPrefix string) (string, bool) {
	rest, ok := pathutil.CutPathPrefix(old, oldPrefix)
	if !ok {
		return "", false
	}
	return newPrefix + rest, true
}

func planRewritePathPrefix(
	ctx context.Context,
	store rewritePathPrefixStore,
	scan ScanController,
	params rewritePathPrefixParams,
	reporter sdk.Reporter,
) (rewritePathPrefixPlan, error) {
	log := reporter.Logger()
	maxBooks := params.Max
	if maxBooks <= 0 {
		maxBooks = rewritePathPrefixDefaultMax
	}
	log.Info("rewrite-path-prefix start",
		"old_prefix", params.OldPrefix, "new_prefix", params.NewPrefix,
		"dry_run", params.dryRun(), "require_target_exists", params.requireTargetExists(),
		"max_books", maxBooks)

	plan := rewritePathPrefixPlan{
		DryRun:              params.dryRun(),
		RequireTargetExists: params.requireTargetExists(),
	}

	// --- read every book, in bounded pages ---
	byBook := map[string]*rewriteBookPlan{}
	for offset := 0; ; offset += bookPageSize {
		page, perr := store.GetAllBooksCore(bookPageSize, offset)
		if perr != nil {
			return plan, fmt.Errorf("load books: %w", perr)
		}
		plan.ScannedBooks += len(page)
		for i := range page {
			b := page[i]
			var changes []rewriteFieldChange
			if np, ok := rewriteTarget(b.FilePath, params.OldPrefix, params.NewPrefix); ok {
				changes = append(changes, rewriteFieldChange{
					Field: "book.file_path", RowID: b.ID, Old: b.FilePath, New: np})
			}
			if b.SourceImportPath != nil {
				if np, ok := rewriteTarget(*b.SourceImportPath, params.OldPrefix, params.NewPrefix); ok {
					changes = append(changes, rewriteFieldChange{
						Field: "book.source_import_path", RowID: b.ID, Old: *b.SourceImportPath, New: np})
				}
			}
			if len(changes) > 0 {
				byBook[b.ID] = &rewriteBookPlan{BookID: b.ID, Changes: changes}
			}
		}
		if len(page) < bookPageSize {
			break
		}
	}

	// --- read every book_file row ---
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return plan, fmt.Errorf("load book files: %w", err)
	}
	plan.ScannedBookFiles = len(files)

	// claimed holds EVERY path any book_file row currently points at, matched or
	// not: a rewrite target already in here would leave two rows on one file.
	claimed := make(map[string]string, len(files))
	for i := range files {
		if fp := strings.TrimSpace(files[i].FilePath); fp != "" {
			claimed[fp] = files[i].ID
		}
	}
	for i := range files {
		f := files[i]
		np, ok := rewriteTarget(f.FilePath, params.OldPrefix, params.NewPrefix)
		if !ok {
			continue
		}
		bp := byBook[f.BookID]
		if bp == nil {
			bp = &rewriteBookPlan{BookID: f.BookID}
			byBook[f.BookID] = bp
		}
		bp.Changes = append(bp.Changes, rewriteFieldChange{
			Field: "book_file.file_path", RowID: f.ID, Old: f.FilePath, New: np})
	}

	// Deterministic order, so a capped run takes a stable prefix across re-runs.
	plans := make([]*rewriteBookPlan, 0, len(byBook))
	for _, bp := range byBook {
		sort.Slice(bp.Changes, func(a, b int) bool {
			if bp.Changes[a].Field != bp.Changes[b].Field {
				return bp.Changes[a].Field < bp.Changes[b].Field
			}
			return bp.Changes[a].RowID < bp.Changes[b].RowID
		})
		plans = append(plans, bp)
		plan.MatchedFields += len(bp.Changes)
	}
	sort.Slice(plans, func(a, b int) bool { return plans[a].BookID < plans[b].BookID })
	plan.MatchedBooks = len(plans)

	if len(plans) == 0 {
		log.Info("rewrite-path-prefix: no row is filed under old_prefix — nothing to do",
			"old_prefix", params.OldPrefix)
		return plan, nil
	}

	// --- Phase 1: per-book safety checks (stat + collision), on a bounded pool ---
	// Filesystem-I/O bound over the matched set, so it runs on a worker pool
	// rather than a serial loop (CLAUDE.md concurrency rule). Each worker writes
	// only its OWN plan's Bucket/Reason, so no lock is needed; the counters the
	// Label closure reads are atomics, because run_items.go invokes Label INSIDE
	// each worker goroutine.
	var refusedCount, okCount atomic.Int64
	prog := sdk.NewProgress(reporter, len(plans))
	prog.Start(fmt.Sprintf("Checking %d book(s) under %s…", len(plans), params.OldPrefix))

	err = registry.RunItems(ctx, reporter, plans, func(_ context.Context, bp *rewriteBookPlan) error {
		bucket, reason, causeRow, causeField := checkRewriteBook(store, bp, claimed, params)
		bp.Bucket, bp.Reason, bp.CauseRowID, bp.CauseField = bucket, reason, causeRow, causeField
		if bucket == "rewritable" {
			okCount.Add(1)
		} else {
			refusedCount.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: rewritePathPrefixConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d books (ok=%d refused=%d)", i+1, t, okCount.Load(), refusedCount.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("safety sweep: %w", err)
	}

	var rewritable []*rewriteBookPlan
	for _, bp := range plans {
		switch bp.Bucket {
		case "rewritable":
			rewritable = append(rewritable, bp)
		case "rewritable-source-import-only":
			plan.SourceImportOnly++
			rewritable = append(rewritable, bp)
		case "target-missing":
			plan.TargetMissing++
			plan.recordBook(bp)
		case "collision":
			plan.TargetCollision++
			plan.recordBook(bp)
		default: // "unreadable"
			plan.Unreadable++
			plan.recordBook(bp)
		}
	}
	plan.Rewritable = len(rewritable)

	if len(rewritable) > maxBooks {
		plan.CappedAt = maxBooks
		log.Warn("rewrite-path-prefix: more rewritable books than the cap — taking the first N by book ID",
			"rewritable", len(rewritable), "cap", maxBooks)
		// The books above the cap get their own bucket rather than vanishing from
		// the report: "every matched field lands in exactly one bucket" has to
		// hold for the truncated tail too, or the report silently understates the
		// population an operator is sizing the next run against.
		for _, bp := range rewritable[maxBooks:] {
			bp.Bucket = "capped"
			bp.Reason = fmt.Sprintf("above this run's cap of %d books — rewritable, not yet attempted", maxBooks)
			plan.recordBook(bp)
		}
		rewritable = rewritable[:maxBooks]
	}

	if params.dryRun() {
		for _, bp := range rewritable {
			bp.Reason = "would rewrite"
			plan.recordBook(bp)
		}
		log.Info("rewrite-path-prefix: DRY RUN — no rows written",
			"would_rewrite_books", len(rewritable))
		return plan, nil
	}

	// --- Phase 2: write ---
	// Acquire the scan stand-down so our path rewrites cannot race a running
	// library.scan's own writes. Released on return.
	holderID, standDownHeld, releaseStandDown, sdErr := acquireScanStandDownForApply(
		ctx, scan, reporter, "rewrite-path-prefix apply")
	if sdErr != nil {
		return plan, fmt.Errorf("rewrite-path-prefix: acquire scan stand-down: %w", sdErr)
	}
	defer releaseStandDown()

	var booksWritten, fieldsWritten, skippedChanged, skippedGone, updateErrs atomic.Int64
	var standDownLost atomic.Bool
	var mu sync.Mutex // guards plan.record from the worker pool

	err = registry.RunItems(ctx, reporter, rewritable, func(_ context.Context, bp *rewriteBookPlan) error {
		if standDownLost.Load() {
			return nil
		}
		if scanStandDownLostForApply(scan, holderID, standDownHeld) {
			standDownLost.Store(true)
			log.Warn("rewrite-path-prefix: scan stand-down lease lost — aborting remaining writes")
			return nil
		}
		decisions, wrote := applyRewriteBook(store, bp, params, log)
		mu.Lock()
		for _, d := range decisions {
			plan.record(d)
		}
		mu.Unlock()
		for _, d := range decisions {
			switch d.Bucket {
			case "rewritten":
				fieldsWritten.Add(1)
			case "skipped-changed-underneath":
				skippedChanged.Add(1)
			case "skipped-row-gone":
				skippedGone.Add(1)
			case "update-error":
				updateErrs.Add(1)
			}
		}
		if wrote {
			booksWritten.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: rewritePathPrefixConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Rewrote %d/%d books (fields=%d errs=%d)",
				i+1, t, fieldsWritten.Load(), updateErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("rewrite writes: %w", err)
	}
	plan.BooksRewritten = int(booksWritten.Load())
	plan.FieldsRewritten = int(fieldsWritten.Load())
	plan.SkippedChanged = int(skippedChanged.Load())
	plan.SkippedGone = int(skippedGone.Load())
	plan.UpdateErrs = int(updateErrs.Load())
	// A run whose writes ALL failed must not return success. The per-item
	// callback deliberately returns nil (one bad row must not abort the sweep),
	// so ErrModeCollect never fires and UpdateErrs was previously visible only in
	// the summary string — on the one op where "did the writes land?" is the
	// entire question, a total failure looked green. The report and the counts
	// are already written by the caller before this error surfaces.
	// Stand-down loss is checked first: it explains WHY the remaining writes did
	// not happen, which subsumes a write-failure count taken from the same run.
	if standDownLost.Load() {
		return plan, fmt.Errorf("rewrite-path-prefix: scan stand-down lease lapsed mid-apply after %d "+
			"book(s) — aborted (re-run after the scan is idle; already-rewritten rows no longer match "+
			"old_prefix, so the re-run finishes the rest)", plan.BooksRewritten)
	}
	if plan.UpdateErrs > 0 {
		return plan, fmt.Errorf("rewrite-path-prefix: %d field write(s) FAILED across %d rewritten "+
			"book(s) — see the per-row report's update-error rows; a book whose own row write failed "+
			"had its file rows left untouched, so re-running is safe", plan.UpdateErrs, plan.BooksRewritten)
	}
	return plan, nil
}

// checkRewriteBook decides one book's fate. A book is rewritten WHOLE or refused
// WHOLE: if any one of its fields fails a check, the book is refused, because a
// book whose folder moved but whose files did not (or vice versa) is worse than
// one left entirely alone.
func checkRewriteBook(
	store rewritePathPrefixStore,
	bp *rewriteBookPlan,
	claimed map[string]string,
	params rewritePathPrefixParams,
) (bucket, reason, causeRowID, causeField string) {
	for _, ch := range bp.Changes {
		switch ch.Field {
		case "book.file_path":
			// Collision is answered by the book_atpath: index, not by a
			// hand-built map: LiveBookIDsAtPath is the same source the organizer
			// consults, and it fails closed when the index is not built.
			ids, err := store.LiveBookIDsAtPath(ch.New)
			if err != nil {
				return "unreadable", fmt.Sprintf("LiveBookIDsAtPath(%s): %v", ch.New, err), ch.RowID, ch.Field
			}
			for _, id := range ids {
				if id != bp.BookID {
					return "collision", fmt.Sprintf("book %s already occupies %s", id, ch.New), ch.RowID, ch.Field
				}
			}
			if params.requireTargetExists() {
				// No IsDir guard here, unlike the book_file case below:
				// Book.FilePath is normalised to the containing DIRECTORY for a
				// multi-file book and is a FILE for a single-file book, so both
				// kinds are legitimate targets.
				if _, serr := os.Stat(ch.New); serr != nil {
					if os.IsNotExist(serr) {
						return "target-missing", "book path does not exist on disk: " + ch.New, ch.RowID, ch.Field
					}
					return "unreadable", fmt.Sprintf("stat %s: %v", ch.New, serr), ch.RowID, ch.Field
				}
			}
		case "book_file.file_path":
			if owner, taken := claimed[ch.New]; taken && owner != ch.RowID {
				return "collision", fmt.Sprintf("book_file %s already claims %s", owner, ch.New), ch.RowID, ch.Field
			}
			if params.requireTargetExists() {
				st, serr := os.Stat(ch.New)
				if serr != nil {
					if os.IsNotExist(serr) {
						return "target-missing", "file does not exist on disk: " + ch.New, ch.RowID, ch.Field
					}
					return "unreadable", fmt.Sprintf("stat %s: %v", ch.New, serr), ch.RowID, ch.Field
				}
				if st.IsDir() {
					return "collision", "a DIRECTORY sits at the new file path: " + ch.New, ch.RowID, ch.Field
				}
			}
		case "book.source_import_path":
			// NO stat gate and NO collision check, deliberately, and this is the
			// one field where that is the right answer rather than an omission:
			//
			//   - it is not a filesystem claim. It records the import folder the
			//     book was FIRST discovered in, and that folder legitimately may
			//     no longer exist (the book was moved into RootDir long ago). A
			//     stat gate would refuse the very rows it is meant to repair.
			//   - many books share one import folder by design, so "another row
			//     holds this path" is the normal case, not a collision.
			//
			// What it gets instead is VISIBILITY: a book matched ONLY through
			// this field lands in its own bucket below, so an operator reading
			// the dry run can tell a real folder move from a provenance-only
			// touch before applying.
		}
	}
	// A book whose ONLY matched field is source_import_path moves no file and
	// changes no location — nothing about it was stat- or collision-checked,
	// because nothing about it could be. It is still rewritten (the field is a
	// live input to CountBooksByPathPrefix), but it is reported apart so the
	// operator is never left guessing which population they are approving.
	sourceImportOnly := true
	for _, ch := range bp.Changes {
		if ch.Field != "book.source_import_path" {
			sourceImportOnly = false
			break
		}
	}
	if sourceImportOnly {
		// No CauseRowID: this verdict names no path, so every row may carry it.
		return "rewritable-source-import-only",
			"would rewrite source_import_path only — no file or folder location changes, " +
				"so no stat or collision check applies", "", ""
	}
	return "rewritable", "would rewrite", "", ""
}

// applyRewriteBook performs one book's writes and returns a decision per field.
//
// Every write re-derives the substitution from the row AS READ UNDER THE STORE'S
// OWN LOCK. That is the concurrency check: ModifyBook / ModifyBookFile read and
// write as one step under the row's stripe, so a precondition tested inside the
// callback cannot be invalidated before the commit. If the stored path no longer
// starts with old_prefix, another writer moved it since the scan, and the row is
// SKIPPED (ErrSkipBookWrite / ErrSkipBookFileWrite) rather than clobbered with
// this run's stale idea of where it was.
func applyRewriteBook(
	store rewritePathPrefixStore,
	bp *rewriteBookPlan,
	params rewritePathPrefixParams,
	log interface {
		Warn(msg string, args ...any)
	},
) (decisions []rewriteDecision, wroteAnything bool) {
	dec := func(bucket, field, rowID, old, nw, reason string) {
		decisions = append(decisions, rewriteDecision{
			Bucket: bucket, BookID: bp.BookID, Field: field, RowID: rowID,
			Old: old, New: nw, Reason: reason})
	}

	// --- the book row: FilePath and SourceImportPath in ONE ModifyBook ---
	//
	// bookOld/bookNew are written INSIDE the ModifyBook callback, i.e. before the
	// commit, so they are a statement of intent, not of fact. The three
	// *Committed flags below are set only on the branch where the store returned
	// a committed row, and they — never the intent variables — decide what the
	// ledger records.
	var bookOld, bookNew string
	var bookPathCommitted, sourceImportCommitted, filesCommitted bool
	bookChanges := map[string]rewriteFieldChange{}
	for _, ch := range bp.Changes {
		if strings.HasPrefix(ch.Field, "book.") {
			bookChanges[ch.Field] = ch
		}
	}
	if len(bookChanges) > 0 {
		var applied []rewriteFieldChange
		var skipReasons []rewriteFieldChange
		// bookPathStale means the book's FilePath no longer starts with
		// old_prefix: another writer moved the book since the scan. When that
		// happens the WHOLE book is abandoned — book row and every file row
		// under it — not just the one field. The stat and collision checks were
		// taken against the path the book held at scan time; once it holds a
		// different one, none of them describe the row in front of us, and
		// rewriting its file rows (or its source_import_path) on the strength of
		// a superseded plan is exactly the half-written book this op refuses to
		// create. Re-running picks up whatever genuinely still needs the move.
		bookPathStale := false
		row, err := store.ModifyBook(bp.BookID, func(b *database.Book) error {
			applied, skipReasons, bookPathStale = nil, nil, false
			if _, want := bookChanges["book.file_path"]; want {
				if np, ok := rewriteTarget(b.FilePath, params.OldPrefix, params.NewPrefix); ok {
					bookOld, bookNew = b.FilePath, np
					applied = append(applied, rewriteFieldChange{
						Field: "book.file_path", RowID: bp.BookID, Old: b.FilePath, New: np})
					b.FilePath = np
				} else {
					bookPathStale = true
					return database.ErrSkipBookWrite
				}
			}
			if _, want := bookChanges["book.source_import_path"]; want {
				if b.SourceImportPath != nil {
					if np, ok := rewriteTarget(*b.SourceImportPath, params.OldPrefix, params.NewPrefix); ok {
						applied = append(applied, rewriteFieldChange{
							Field: "book.source_import_path", RowID: bp.BookID,
							Old: *b.SourceImportPath, New: np})
						b.SourceImportPath = &np
					} else {
						skipReasons = append(skipReasons, rewriteFieldChange{
							Field: "book.source_import_path", RowID: bp.BookID, Old: *b.SourceImportPath})
					}
				} else {
					skipReasons = append(skipReasons, rewriteFieldChange{
						Field: "book.source_import_path", RowID: bp.BookID})
				}
			}
			if len(applied) == 0 {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		// refuseFileRows records every planned book_file row of this book as
		// refused. It exists because the book row's write is the FIRST write of
		// the book: if it did not land, continuing into the file loop produces
		// exactly the half-written book — folder unmoved, files moved — that this
		// op's whole-or-nothing rule exists to prevent. Recording rather than
		// silently returning keeps the "every matched field lands in exactly one
		// bucket" property true.
		refuseFileRows := func(bucket, reason string) {
			for _, ch := range bp.Changes {
				if ch.Field == "book_file.file_path" {
					dec(bucket, ch.Field, ch.RowID, ch.Old, ch.New, reason)
				}
			}
		}
		switch {
		case err != nil:
			for _, ch := range bookChanges {
				dec("update-error", ch.Field, ch.RowID, ch.Old, ch.New, err.Error())
			}
			refuseFileRows("update-error",
				"not attempted: the book row's own write failed, and moving its files without it "+
					"would leave the book half-written")
			log.Warn("rewrite-path-prefix: book update failed — this book's file rows were NOT attempted",
				"book", bp.BookID, "err", err)
			return decisions, false
		case bookPathStale:
			// Abandon the whole book: every planned field, file rows included.
			for _, ch := range bp.Changes {
				dec("skipped-changed-underneath", ch.Field, ch.RowID, ch.Old, "",
					"the book's stored path no longer starts with old_prefix — another writer moved "+
						"it since the scan, so this run's checks no longer describe the row")
			}
			return decisions, false
		case row == nil:
			for _, ch := range bookChanges {
				dec("skipped-row-gone", ch.Field, ch.RowID, ch.Old, ch.New, "book row vanished before write")
			}
			refuseFileRows("skipped-row-gone",
				"not attempted: the owning book row vanished before its write")
			return decisions, false
		default:
			// COMMITTED. Only here are bookOld/bookNew trustworthy: they are
			// assigned inside the callback, which runs BEFORE the commit, so a
			// failed write leaves them set describing a move that never happened.
			// Promoting them to the committed pair only on success is what keeps
			// the ledger below from asserting a move the store rejected.
			bookPathCommitted = bookNew != ""
			for _, ch := range applied {
				dec("rewritten", ch.Field, ch.RowID, ch.Old, ch.New, "rewritten")
				wroteAnything = true
				if ch.Field == "book.source_import_path" {
					sourceImportCommitted = true
				}
			}
			for _, ch := range skipReasons {
				dec("skipped-changed-underneath", ch.Field, ch.RowID, ch.Old, "",
					"stored path no longer starts with old_prefix — another writer moved it since the scan")
			}
		}
	}

	// --- the book_file rows ---
	for _, ch := range bp.Changes {
		if ch.Field != "book_file.file_path" {
			continue
		}
		var newPath string
		var moved bool
		row, err := store.ModifyBookFile(bp.BookID, ch.RowID, func(f *database.BookFile) error {
			np, ok := rewriteTarget(f.FilePath, params.OldPrefix, params.NewPrefix)
			if !ok {
				moved = true
				return database.ErrSkipBookFileWrite
			}
			newPath = np
			f.FilePath = np
			return nil
		})
		switch {
		case err != nil:
			dec("update-error", ch.Field, ch.RowID, ch.Old, ch.New, err.Error())
			log.Warn("rewrite-path-prefix: book_file update failed",
				"book", bp.BookID, "file", ch.RowID, "err", err)
		case row == nil:
			dec("skipped-row-gone", ch.Field, ch.RowID, ch.Old, ch.New, "book_file row vanished before write")
		case moved:
			dec("skipped-changed-underneath", ch.Field, ch.RowID, ch.Old, "",
				"stored path no longer starts with old_prefix — another writer moved it since the scan")
		default:
			dec("rewritten", ch.Field, ch.RowID, ch.Old, newPath, "rewritten")
			wroteAnything = true
			filesCommitted = true
		}
	}

	// --- the ledger, AFTER the write it describes ---
	// Ordering and provenance both matter (the ledger-before-write bug class):
	// the row is recorded only once its write has succeeded, and OldPath is the
	// value read INSIDE the ModifyBook callback, not this run's pre-scan
	// snapshot. path_history is book-keyed, so the book row's move is what is
	// recorded; the file rows beneath it move with it and are enumerated in the
	// run's TSV report.
	//
	// The type names WHICH of the three committed, so a reader of path_history
	// can tell a folder move from a files-only or an import-provenance-only one:
	//
	//	prefix-rewrite               the book row's own FilePath moved; carries
	//	                             the book's real old and new path.
	//	prefix-rewrite-files         only book_file rows moved; carries the
	//	                             PREFIXES, because inventing a book path from
	//	                             a file's directory would record something the
	//	                             row never held.
	//	prefix-rewrite-source-import only source_import_path moved — no file
	//	                             changed location at all, so calling it a
	//	                             "files" rewrite would be a lie.
	//
	// Every branch reads a *Committed flag, never bookOld/bookNew directly: those
	// are set inside the callback, so a book row whose write FAILED would
	// otherwise journal a move that never happened. A row's state heals on a
	// re-run; a false ledger entry is permanent.
	var change *database.BookPathChange
	switch {
	case bookPathCommitted:
		change = &database.BookPathChange{BookID: bp.BookID, OldPath: bookOld, NewPath: bookNew,
			ChangeType: "prefix-rewrite"}
	case filesCommitted:
		change = &database.BookPathChange{BookID: bp.BookID,
			OldPath: params.OldPrefix, NewPath: params.NewPrefix, ChangeType: "prefix-rewrite-files"}
	case sourceImportCommitted:
		change = &database.BookPathChange{BookID: bp.BookID,
			OldPath: params.OldPrefix, NewPath: params.NewPrefix,
			ChangeType: "prefix-rewrite-source-import"}
	}
	if change != nil {
		if rerr := store.RecordPathChange(change); rerr != nil {
			// Not fatal and NOT counted as an update error: the row write already
			// succeeded and undoing it here would be worse than a missing ledger
			// line. Said loudly so it is not silent.
			log.Warn("rewrite-path-prefix: RecordPathChange failed (the row write DID land)",
				"book", bp.BookID, "kind", change.ChangeType,
				"old", change.OldPath, "new", change.NewPath, "err", rerr)
		}
	}
	return decisions, wroteAnything
}

// recordBook files one decision per field for a book that was not written.
//
// The verdict is book-level (checkRewriteBook stops at the first failing field)
// but the report has one line per field, so only the row that actually produced
// the verdict carries its reason verbatim. Every other row of the same book says
// so and points at that row, instead of repeating a reason that names a path the
// line is not about.
func (p *rewritePathPrefixPlan) recordBook(bp *rewriteBookPlan) {
	for _, ch := range bp.Changes {
		reason := bp.Reason
		if bp.CauseRowID != "" && (ch.RowID != bp.CauseRowID || ch.Field != bp.CauseField) {
			reason = fmt.Sprintf("refused with its book (%s); see %s row %s", bp.Bucket, bp.CauseField, bp.CauseRowID)
		}
		p.record(rewriteDecision{
			Bucket: bp.Bucket, BookID: bp.BookID, Field: ch.Field, RowID: ch.RowID,
			Old: ch.Old, New: ch.New, Reason: reason})
	}
}

// writeRewritePathPrefixReport dumps EVERY matched field and what was decided
// about it, TSV — same convention and same reasoning as writeRepointReport: the
// file exists to be read by a person deciding whether to run the apply, and
// grepped by bucket while they do.
func writeRewritePathPrefixReport(path string, decisions []rewriteDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	// A path with a tab or newline in it would shift every later column silently.
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("bucket\tbook_id\tfield\trow_id\told\tnew\treason\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.Bucket, d.BookID, d.Field, d.RowID, clean(d.Old), clean(d.New), clean(d.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
