// file: internal/plugins/maintenance/metadata_cache_reap.go
// version: 1.1.0
// guid: 6b1f9d47-3c82-4e05-9a71-d84c2f60e5b3
// last-edited: 2026-08-20

// Package maintenance — REAPER for metadata_cache rows whose book is gone.
//
// 🟡 THIS OP DELETES, and that needs saying out loud next to
// missing_file_repoint.go's "🔴 THIS OP NEVER DELETES A ROW ... (owner decision:
// never delete)". That decision is not being reversed here, because it is not
// about the same thing. It removed the delete path from missing-file-repair,
// which deleted book_file rows: library records pointing at audio the owner
// owns, where a wrong delete destroys the only pointer to real bytes.
//
// A metadata_cache row is the opposite kind of object. It is DERIVED (the top-N
// results of a metadata search), REGENERABLE (re-run the fetch and it comes
// back), already treated as expendable by its own 30-day TTL, and — the part
// that matters — keyed to a book_id that no longer resolves, so nothing in the
// product can reach it. It is not a record of the library; it is a cached answer
// to a question about a book that is gone.
//
// Measured against production on 2026-08-20: 14,306 cache rows, of which 3,354
// (23%) are orphaned. There is no other cleanup path, and the count only grows.
//
// SOFT DELETES ARE NOT ORPHANS, and this was checked rather than assumed. The
// library holds 16,124 soft-deleted books, but GetBookByID applies no
// soft-delete filter — point-getting a soft-deleted book returns it. So a cache
// row pointing at a soft-deleted book RESOLVES and is never reaped. The 3,354
// are absences, not tombstones.
//
// Three properties make the delete safe to run and safe to interrupt:
//
//  1. Absence and failure are different buckets. GetBookByID returns (nil, nil)
//     for a genuinely missing key and (nil, err) for a real fault. Only the
//     first is an orphan; a lookup error is counted, reported and SKIPPED. A bad
//     day for the store must never read as 3,354 books to forget.
//  2. Every row is re-resolved at delete time, not trusted from the plan. If a
//     book is restored or re-imported between the scan and the write, its row
//     stops being an orphan and is left alone (counted as "revived").
//  3. Dry run is the default and writes nothing; apply is explicit.
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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// metadataCacheReapDefaultMax bounds how many rows one run will delete, so a
// first production run is a sample rather than a 3,354-row leap. 0 means this.
const metadataCacheReapDefaultMax = 500

// metadataCacheReapConcurrency bounds the resolve sweep. Every probe is a
// point-get against Pebble, so this is store-bound rather than disk-bound.
const metadataCacheReapConcurrency = 8

type metadataCacheReapParams struct {
	// Apply must be explicitly true to delete. Default false = report only.
	Apply bool `json:"apply"`
	// Max bounds deletes per run. <=0 uses metadataCacheReapDefaultMax.
	Max int `json:"max"`
	// ReportPath overrides where the per-row TSV lands.
	ReportPath string `json:"reportPath"`
}

// reapDecision is one cache row and what was decided about it.
type reapDecision struct {
	BookID string `json:"book_id"`
	Bucket string `json:"bucket"`
	Reason string `json:"reason"`
	// Candidates is how many cached candidates the row held. A reaped row with
	// a high count is a more expensive thing to have thrown away than an empty
	// one, and the report should let a reader see that without re-querying.
	Candidates int `json:"candidates"`
}

// reapPlan is the whole-run accounting. The bucket counters partition the
// scanned set: Resolves + Orphaned + OrphanedCapped + LookupErrs == ScannedRows.
// That identity is asserted in the tests and printed in the summary, because a
// delete op whose buckets do not close cannot prove it did not drop a row
// silently.
type reapPlan struct {
	Apply       bool `json:"apply"`
	ScannedRows int  `json:"scanned_rows"`
	// Resolves is rows whose book is still present — the keep set.
	Resolves int `json:"resolves"`
	// Orphaned is rows selected for deletion this run (already capped).
	Orphaned int `json:"orphaned"`
	// OrphanedCapped is orphans left for a later run because of Max.
	OrphanedCapped int `json:"orphaned_capped"`
	// LookupErrs is rows whose book could not be resolved EITHER way. Never
	// deleted.
	LookupErrs int `json:"lookup_errors"`
	// Reaped / Revived / DeleteErrs are apply-phase outcomes and are 0 on a dry
	// run. Revived counts rows that resolved on the delete-time re-check after
	// looking orphaned during the scan.
	Reaped     int    `json:"reaped"`
	Revived    int    `json:"revived"`
	DeleteErrs int    `json:"delete_errors"`
	CappedAt   int    `json:"capped_at,omitempty"`
	ReportPath string `json:"report_path,omitempty"`

	Samples []reapDecision `json:"samples,omitempty"`

	all           []reapDecision
	bucketSampled map[string]int
}

func (p *reapPlan) summary() string {
	mode := "DRY RUN"
	if p.Apply {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s scanned=%d resolves=%d orphaned=%d orphaned_capped=%d lookup_errs=%d | reaped=%d revived=%d delete_errs=%d",
		mode, p.ScannedRows, p.Resolves, p.Orphaned, p.OrphanedCapped, p.LookupErrs,
		p.Reaped, p.Revived, p.DeleteErrs)
}

// bucketsClose reports whether the four scan buckets account for every scanned
// row. Exported behaviour depends on it only through the log line, but the op
// says so explicitly rather than leaving a reader to add the numbers up.
func (p *reapPlan) bucketsClose() bool {
	return p.Resolves+p.Orphaned+p.OrphanedCapped+p.LookupErrs == p.ScannedRows
}

// record files one row's outcome: every decision into the full list for the TSV,
// and a per-bucket-capped subset into Samples for the log line. Sampling is per
// bucket, not arrival-ordered, so the log shows one of each KIND of decision
// rather than the first N rows the iterator happened to hand over.
func (p *reapPlan) record(d reapDecision) {
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

func (p *Plugin) metadataCacheReapDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.metadata-cache-reap",
		DisplayName: "Reap metadata cache rows whose book is gone",
		Description: "Deletes metadata_cache rows whose book_id no longer resolves. Cache rows are " +
			"derived and regenerable; no library record is touched. Soft-deleted books still " +
			"resolve and are never reaped. Default dry-run; pass {\"apply\": true} to delete.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.metadata-cache-reap",
		// ResumeDrop: this op WRITES, and a delete pass interrupted midway must
		// not silently pick itself back up. Re-running is safe and idempotent —
		// a reaped row is gone, so it is simply not selected again — so dropping
		// costs progress, not correctness.
		ResumePolicy: sdk.ResumeDrop,
		// LivenessRunItems: both phases go through registry.RunItems. The
		// pre-RunItems prologue is a single ListMetadataCacheKeys over ~14k rows,
		// an order of magnitude smaller than the 532k-row sweep that
		// missing-file-repoint does before its first tick, so the silent window
		// fits well inside the 5m default and no explicit ProgressTimeout is set.
		Liveness: sdk.LivenessRunItems,
		// CapLibraryWrite even though no library row is written: the capability
		// set has no cache-specific grant, and a delete op must not be the one
		// that claims read-only. CapLibraryRead covers resolving each book.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runMetadataCacheReap(ctx, raw, reporter)
		},
	}
}

// reapBookResolver is the one thing this op needs from the library store: the
// ability to ask whether a book_id still exists. Named separately from the cache
// store so the test can supply a resolver that fails on demand.
type reapBookResolver interface {
	GetBookByID(id string) (*database.Book, error)
}

func (p *Plugin) runMetadataCacheReap(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params metadataCacheReapParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("metadata-cache-reap: decode params: %w", err)
		}
	}
	cache := p.deps.MetadataCacheStore()
	if cache == nil {
		return fmt.Errorf("database not initialized")
	}
	books := p.deps.OpsStore()
	if books == nil {
		return fmt.Errorf("database not initialized")
	}

	plan, err := planMetadataCacheReap(ctx, cache, books, params, reporter)
	if err != nil {
		return err
	}
	log := reporter.Logger()

	// Write the per-row report BEFORE the summary lines, so a run killed while
	// emitting its summary still leaves the artifact behind. For a delete op the
	// report is not a convenience: it is the only record of what was destroyed,
	// and the only way to regenerate the reaped rows is to know which books they
	// belonged to.
	reportPath := params.ReportPath
	if reportPath == "" {
		name := registry.ReporterOpID(reporter)
		if name == "" {
			name = "unknown-op"
		}
		reportPath = filepath.Join("reports", "metadata-cache-reap-"+name+".tsv")
	}
	if wErr := writeReapReport(reportPath, plan.all); wErr != nil {
		log.Error("metadata-cache-reap: FAILED to write the per-row report",
			"path", reportPath, "err", wErr, "rows", len(plan.all))
	} else {
		plan.ReportPath = reportPath
		log.Info("metadata-cache-reap: per-row report written",
			"path", reportPath, "rows", len(plan.all))
	}

	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("metadata-cache-reap report (JSON)", "report", string(b))
	}
	if !plan.bucketsClose() {
		// Loud, because the whole safety argument for deleting rests on every
		// scanned row having been classified into exactly one bucket.
		log.Error("metadata-cache-reap: BUCKETS DO NOT CLOSE — a scanned row was not classified",
			"scanned", plan.ScannedRows, "resolves", plan.Resolves, "orphaned", plan.Orphaned,
			"orphaned_capped", plan.OrphanedCapped, "lookup_errors", plan.LookupErrs)
	}
	if plan.LookupErrs > 0 {
		log.Warn("metadata-cache-reap: rows SKIPPED because the book could not be resolved either way "+
			"(a lookup error is not an absence). Re-run once the store is healthy.",
			"rows", plan.LookupErrs, "report", reportPath)
	}
	if plan.Revived > 0 {
		log.Warn("metadata-cache-reap: rows spared at write time — the book resolved on the re-check "+
			"after looking orphaned during the scan",
			"rows", plan.Revived)
	}
	if plan.OrphanedCapped > 0 {
		log.Warn("metadata-cache-reap: more orphans than the cap — run again to continue",
			"cap", plan.CappedAt, "remaining", plan.OrphanedCapped)
	}
	log.Info("metadata-cache-reap complete", "summary", plan.summary())
	return nil
}

func planMetadataCacheReap(
	ctx context.Context,
	cache database.MetadataCacheStore,
	books reapBookResolver,
	params metadataCacheReapParams,
	reporter sdk.Reporter,
) (reapPlan, error) {
	log := reporter.Logger()
	maxDeletes := params.Max
	if maxDeletes <= 0 {
		maxDeletes = metadataCacheReapDefaultMax
	}
	log.Info("metadata-cache-reap start", "apply", params.Apply, "max", maxDeletes)

	summaries, err := cache.ListMetadataCacheKeys()
	if err != nil {
		return reapPlan{}, fmt.Errorf("list metadata cache: %w", err)
	}
	plan := reapPlan{Apply: params.Apply, ScannedRows: len(summaries)}

	type cacheItem struct {
		idx int
		sum database.MetadataCacheSummary
	}
	items := make([]cacheItem, 0, len(summaries))
	for _, s := range summaries {
		// A cache row with an empty book_id cannot be resolved and cannot be
		// deleted by key either — DeleteMetadataCache would target the bare
		// prefix. Count it as a lookup error rather than inventing a bucket.
		items = append(items, cacheItem{idx: len(items), sum: s})
	}

	// Phase 1 — resolve every row's book. Point-gets over ~14k rows, so it runs
	// on the bounded worker pool rather than a serial loop.
	type rowOutcome struct {
		orphan bool
		errMsg string
	}
	outcomes := make([]rowOutcome, len(items))
	var orphanCount atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Resolving %d cached book(s)…", len(items)))

	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it cacheItem) error {
		if strings.TrimSpace(it.sum.BookID) == "" {
			outcomes[it.idx] = rowOutcome{errMsg: "empty book_id"}
			return nil
		}
		book, gerr := books.GetBookByID(it.sum.BookID)
		if gerr != nil {
			// NOT an orphan. A store fault is unknowable, not absent.
			outcomes[it.idx] = rowOutcome{errMsg: gerr.Error()}
			return nil
		}
		if book == nil {
			orphanCount.Add(1)
			outcomes[it.idx] = rowOutcome{orphan: true}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: metadataCacheReapConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Resolved %d/%d cached books (orphans=%d)", i+1, t, orphanCount.Load())
		},
	})
	if err != nil {
		return reapPlan{}, fmt.Errorf("resolve sweep: %w", err)
	}

	// Phase 2 — classify. Every scanned row lands in exactly one bucket.
	var orphans []cacheItem
	for i, it := range items {
		o := outcomes[i]
		switch {
		case o.errMsg != "":
			plan.LookupErrs++
			plan.record(reapDecision{BookID: it.sum.BookID, Bucket: "lookup-error",
				Candidates: it.sum.CandidateCount,
				Reason:     "book could not be resolved either way: " + o.errMsg})
		case o.orphan:
			orphans = append(orphans, it)
		default:
			plan.Resolves++
			plan.record(reapDecision{BookID: it.sum.BookID, Bucket: "resolves",
				Candidates: it.sum.CandidateCount, Reason: "book still present"})
		}
	}

	// Deterministic order so a capped run takes a stable prefix across re-runs
	// instead of a different arbitrary slice each time.
	sort.Slice(orphans, func(a, b int) bool { return orphans[a].sum.BookID < orphans[b].sum.BookID })
	reapable := orphans
	if len(reapable) > maxDeletes {
		plan.CappedAt = maxDeletes
		log.Warn("metadata-cache-reap: more orphans than the cap — taking the first N by book ID",
			"orphans", len(reapable), "cap", maxDeletes)
		reapable = reapable[:maxDeletes]
		for _, it := range orphans[maxDeletes:] {
			plan.OrphanedCapped++
			plan.record(reapDecision{BookID: it.sum.BookID, Bucket: "orphaned-capped",
				Candidates: it.sum.CandidateCount, Reason: "orphaned but beyond this run's cap"})
		}
	}
	plan.Orphaned = len(reapable)
	for _, it := range reapable {
		plan.record(reapDecision{BookID: it.sum.BookID, Bucket: "orphaned",
			Candidates: it.sum.CandidateCount, Reason: "book does not resolve — would reap"})
	}

	if !params.Apply {
		log.Info("metadata-cache-reap: DRY RUN — no rows deleted", "would_reap", len(reapable))
		return plan, nil
	}

	// Phase 3 — delete, re-resolving each book IMMEDIATELY BEFORE removing its
	// row. The plan is a snapshot and the store is live: a book restored or
	// re-imported since the scan is no longer an orphan, and its cached
	// candidates are worth keeping. Trusting the plan here would turn a race
	// into data loss, so the check that authorises each delete is the one taken
	// at the moment of the delete.
	// outcomes is what actually happened per row, collected so the report can
	// state it. Without this every orphaned row reads "would reap" even on a run
	// that reaped it -- accurate for a dry run, misleading for an apply -- and a
	// spared or failed row is indistinguishable from a deleted one anywhere but
	// the summary counters. For an op whose report IS the record of what it
	// destroyed, "which of these 3,354 actually went" has to be answerable from
	// the file.
	var outcomeMu sync.Mutex
	applyOutcome := make(map[string]reapDecision, len(reapable))
	note := func(bookID, bucket, reason string) {
		outcomeMu.Lock()
		defer outcomeMu.Unlock()
		applyOutcome[bookID] = reapDecision{Bucket: bucket, Reason: reason}
	}

	var reaped, revived, deleteErrs atomic.Int64
	err = registry.RunItems(ctx, reporter, reapable, func(_ context.Context, it cacheItem) error {
		book, gerr := books.GetBookByID(it.sum.BookID)
		if gerr != nil {
			deleteErrs.Add(1)
			note(it.sum.BookID, "recheck-error", "spared: re-check failed: "+gerr.Error())
			log.Warn("metadata-cache-reap: re-check failed, row spared",
				"book", it.sum.BookID, "err", gerr)
			return nil
		}
		if book != nil {
			revived.Add(1)
			note(it.sum.BookID, "spared-revived", "spared: book resolved on the re-check")
			log.Info("metadata-cache-reap: book resolved on re-check, row spared",
				"book", it.sum.BookID)
			return nil
		}
		if derr := cache.DeleteMetadataCache(it.sum.BookID); derr != nil {
			deleteErrs.Add(1)
			note(it.sum.BookID, "delete-error", "NOT deleted: "+derr.Error())
			log.Warn("metadata-cache-reap: delete failed", "book", it.sum.BookID, "err", derr)
			return nil
		}
		reaped.Add(1)
		note(it.sum.BookID, "reaped", "DELETED")
		return nil
	}, registry.RunItemsOptions{
		Concurrency: metadataCacheReapConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Reaped %d/%d rows (revived=%d errs=%d)",
				i+1, t, revived.Load(), deleteErrs.Load())
		},
	})
	if err != nil {
		return plan, fmt.Errorf("reap deletes: %w", err)
	}
	plan.Reaped = int(reaped.Load())
	plan.Revived = int(revived.Load())
	plan.DeleteErrs = int(deleteErrs.Load())

	// Restamp the planned decisions with what actually happened. Only rows the
	// apply phase touched are rewritten; `resolves` and `orphaned-capped` were
	// never candidates and keep the plan's wording. Samples carry copies of the
	// same decisions, so they are restamped too or the log line would contradict
	// the file it points at.
	plan.restampApplyOutcomes(applyOutcome)
	return plan, nil
}

// restampApplyOutcomes rewrites each attempted row's bucket and reason with the
// result of the attempt, so the report distinguishes a row that was deleted from
// one that was spared or failed. A no-op for rows the apply never reached.
func (p *reapPlan) restampApplyOutcomes(outcomes map[string]reapDecision) {
	// No empty-map guard: a dry run returns before the apply phase and never
	// reaches this, and with an empty map the loops below are already a no-op.
	// A guard here would be untestable code standing in for nothing -- verified
	// by mutation, removing it changes no test's result.
	stamp := func(list []reapDecision) {
		for i := range list {
			o, ok := outcomes[list[i].BookID]
			if !ok || list[i].Bucket != "orphaned" {
				continue
			}
			list[i].Bucket = o.Bucket
			list[i].Reason = o.Reason
		}
	}
	stamp(p.all)
	stamp(p.Samples)
}

// writeReapReport dumps EVERY scanned row and what was decided about it, TSV.
//
// Every row, not only the reaped ones, and for a sharper reason than the repoint
// report's: this op destroys data, and the report is the record. A reader
// deciding whether to run the apply needs to see the keep set to believe the
// reap set, and a reader auditing afterwards needs the book_ids to know what to
// re-fetch.
func writeReapReport(path string, decisions []reapDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	// A reason with a tab or newline in it would shift every later column
	// silently — store errors are interpolated into reason, so this is real.
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("bucket\tbook_id\tcandidates\treason\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\t%s\t%d\t%s\n", d.Bucket, clean(d.BookID), d.Candidates, clean(d.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
