// file: internal/dedup/drain_stale.go
// version: 1.6.0
// guid: 60d982e2-6836-4327-9ddf-9b55375f39ea
// last-edited: 2026-09-28

// Package dedup — DrainStaleCandidates (DEDUP-1 / CONS-16 / CONS-17).
//
// ~383,902 exact-layer dedup candidates were emitted BEFORE the CONS-16
// (duration-stored-in-milliseconds) and CONS-17 (multi-file title-leak) iTunes
// importer bugs were fixed. Those candidates were computed against corrupt
// duration/title data and were never re-checked once the underlying book data
// self-healed (via the duration-backfill op and the title-leak forward fix).
//
// DrainStaleCandidates re-runs every PENDING, layer="exact" candidate through
// the SAME guard chain upsertExactCandidate applies TODAY — using each pair's
// current, corrected book data — and classifies any candidate that would no
// longer be emitted as "would-purge", bucketed by the first gate that rejects
// it. It is dry-run by default (apply=false only tallies); apply=true soft-
// reclassifies would-purge rows to "stale-drain" (never a hard delete, so the
// run is auditable/reversible — the M0 purge_legacy_fp precedent).
//
// Memory bound (DEDUP-5): candidates are streamed in bounded pages via
// EmbeddingStore.ListCandidatesAfterID, a keyset cursor over candidate IDs
// (never Limit:1000000), and the per-run book cache retains only the handful
// of fields the gates read — never a full database.Book and never a BookSigV1
// string.
//
// Cost (2026-09-28): this op used to page with ListCandidates' Limit/Offset.
// ListCandidates loads, filters and sorts the WHOLE pending set on every call
// and then slices out one page, so a ~384K backlog in 500-row pages meant
// ~768 full scans, O(N²) in total. That, plus serial per-pair book reads and no
// progress reports, got the dry run killed by the registry watchdog after 5
// minutes of silence. It now pages by keyset cursor (O(page) per call),
// prefetches each page's books on a bounded worker pool, and reports progress
// after every page.

package dedup

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// drainStaleBatchSize is the page size used when streaming pending exact
// candidates. Mirrors checkExactISBNScan's 500-row batching so the caller never
// materialises the full ~384K candidate backlog in one shot (DEDUP-5). It is a
// var (not a const) only so tests can lower it to exercise page-boundary logic.
var drainStaleBatchSize = 500

// drainStaleWorkers bounds the per-page book/author read pool. The reads are
// local Pebble gets plus JSON decodes (CPU-bound), so the pool is sized to the
// core count. A var only so tests can force both a serial (1) and a contended
// (> NumCPU on small CI boxes) run and compare their results.
var drainStaleWorkers = runtime.NumCPU()

// drainStaleMarkProgressEvery is how many phase-2 reclassifications pass
// between progress reports, so the watchdog stays fed on a large apply.
const drainStaleMarkProgressEvery = 500

// drainStaleCheckpointPhase names the apply checkpoint's phase. Its PhaseIndex
// is the keyset cursor (last scanned candidate ID). The phase name changed
// from "scanning" (a row offset) when paging moved to the cursor, so an old
// offset checkpoint is never misread as an ID.
const drainStaleCheckpointPhase = "scan-after-id"

// drainStaleSampleCap bounds how many example candidates are retained per reason
// bucket for the dry-run report, so Samples cannot grow with the backlog size.
const drainStaleSampleCap = 50

// staleDrainStatus is the soft-reclassification status written to would-purge
// candidates on apply=true. Rows are never hard-deleted, so the drain is
// auditable and reversible (matches the purge_legacy_fp "stale-fp" precedent).
const staleDrainStatus = "stale-drain"

// Reason buckets — which gate first rejected a would-purge candidate.
const (
	drainReasonMissingBook        = "missing_book"
	drainReasonNonPrimaryVersion  = "non_primary_version"
	drainReasonIdentifierConflict = "identifier_conflict"
	drainReasonBoilerplateTitle   = "boilerplate_title"
	drainReasonShortDuration      = "short_duration"
	drainReasonPartVsWhole        = "part_vs_whole"
	// drainReasonPlaceholderTitle (2026-09-27): the pair's only evidence is a
	// placeholder title ("Unknown Title", "read by narrator", ...) or a title
	// shared under placeholder authors on both sides — the pairs the title
	// rules no longer emit (titleEvidenceRefusal). A pair that ALSO shares
	// content (a file hash, an ISBN/ASIN, a metadata source record, or a
	// recorded content rule / scoring signal) is kept: its title was never the
	// evidence. The reclassification is to stale-drain, a machine status, not
	// a human "not a duplicate" verdict.
	drainReasonPlaceholderTitle = "placeholder_title"
)

// DrainStaleResult is the report produced by a DrainStaleCandidates run.
type DrainStaleResult struct {
	Inspected  int
	WouldPurge int
	Kept       int
	// SkippedAtWrite counts apply-mode rows phase 1 selected but phase 2 left
	// alone because, by the time of the write, a human had pinned or decided
	// the pair (see EmbeddingStore.ReclassifyCandidate). They are included in
	// WouldPurge, which describes the scan.
	SkippedAtWrite int
	// LookupErrors counts pairs kept because a book or book-file read FAILED
	// for either side (a store error, not a book that is gone). They are
	// included in Kept.
	LookupErrors int
	// ReasonCounts buckets WouldPurge by the first gate that rejected the pair.
	ReasonCounts map[string]int
	// Samples holds up to drainStaleSampleCap examples per reason for the report.
	Samples map[string][]DrainStaleSample
}

// DrainStaleSample identifies one would-purge candidate for the dry-run report.
type DrainStaleSample struct {
	CandidateID      int64
	BookAID, BookBID string
	Reason           string
}

// drainBookMeta is the minimal per-book field set the gates need. It retains a
// stub *database.Book carrying ONLY id/title/duration/identifiers/primary-flag
// (never the full row, never a BookSigV1 blob) so the run's book cache stays
// small regardless of backlog size.
type drainBookMeta struct {
	stub    *database.Book
	missing bool
	// readErr: a book or book-file read failed. The pair is kept rather than
	// classified, since a failed read says nothing about the pair.
	readErr bool
	// runtime and fileRows are the book's canonical runtime and file-row
	// count, read ONCE per book at lookup, so the min-duration and
	// part-vs-whole gates cost no store read per candidate.
	runtime  database.BookRuntime
	fileRows int
	filesOK  bool
	// authorName is the resolved author name ("" when unresolved) and
	// hashes the book's content hashes (book-level plus every file row's),
	// both for the placeholder-title gate's content-evidence check.
	authorName string
	hashes     map[string]struct{}
}

// DrainStaleProgressFunc receives progress from DrainStaleCandidates: done of
// total, plus a human-readable message. It is called on the caller's goroutine
// (never from a worker), after every scanned page and periodically while
// marking, so wiring it to a registry Reporter's UpdateProgress keeps the op
// watchdog fed. A nil func disables reporting.
type DrainStaleProgressFunc func(done, total int, message string)

// DrainStaleCandidates re-evaluates pending exact-layer candidates against the
// current guard chain and reports/optionally drains the stale ones. See the
// package doc above for the full contract.
//
// opID: when non-empty AND apply is true, the run checkpoints its keyset cursor
// (the last scanned candidate ID) via operations.SaveCheckpoint and resumes
// after it via operations.LoadCheckpoint so an interrupted apply resumes
// mid-backlog. Checkpoint/resume is deliberately NOT applied to dry runs: a dry
// run must always full-scan so its report totals are complete (a
// resumed-and-therefore-partial report would silently undercount the counts a
// human reviews before greenlighting the destructive apply).
// apply: false (default) writes nothing; true soft-reclassifies would-purge rows.
// progress: see DrainStaleProgressFunc; may be nil.
func (de *Engine) DrainStaleCandidates(ctx context.Context, opID string, apply bool, progress DrainStaleProgressFunc) (*DrainStaleResult, error) {
	if de.embedStore == nil || de.bookStore == nil {
		return nil, fmt.Errorf("drain-stale: embedding or book store not available")
	}
	report := func(done, total int, msg string) {
		if progress != nil {
			progress(done, total, msg)
		}
	}

	result := &DrainStaleResult{
		ReasonCounts: make(map[string]int),
		Samples:      make(map[string][]DrainStaleSample),
	}

	// Small per-run cache: book ID -> minimal stub (or missing / read-error
	// marker). A book referenced by many candidates is only fetched once.
	// Only the tiny field set the gates read is retained; the full row
	// fetched by GetBookByID is transient and discarded. Both maps are
	// written ONLY on this goroutine (prefetchDrainBooks fills them from its
	// workers' results after the pool has drained), so they need no lock.
	cache := make(map[string]drainBookMeta)
	// Author names are cached by author ID: the placeholder authors this
	// gate exists for hang hundreds of books off one row.
	authorNames := make(map[int]string)

	filter := database.CandidateFilter{EntityType: "book", Status: "pending", Layer: "exact"}

	// Total pending-exact count: the progress denominator (and the
	// checkpoint's PhaseTotal on apply). A Limit:1 read returns the full
	// filtered total without handing the caller the rows; it costs one scan
	// of the pending set, once per run.
	countFilter := filter
	countFilter.Limit = 1
	_, totalPendingExact, cerr := de.embedStore.ListCandidates(countFilter)
	if cerr != nil {
		return nil, fmt.Errorf("drain-stale: count pending exact candidates: %w", cerr)
	}

	// Checkpoint/resume is scoped to the APPLY path only. A dry run MUST always
	// full-scan from the start: its whole purpose is to produce a COMPLETE
	// report (total inspected/would-purge/kept) that a human reviews before
	// greenlighting the destructive apply, and a partial-because-resumed report
	// would silently undercount. Confining checkpoint I/O to apply also prevents
	// a stale dry-run checkpoint from ever being read by a later apply
	// (cross-mode contamination).
	checkpoint := apply && opID != ""

	// Resume after a saved cursor if present (apply only). Only a checkpoint
	// written in the keyset phase is honoured: a legacy "scanning" checkpoint
	// holds a row OFFSET, and reading an offset as a candidate ID would skip
	// an arbitrary slice of the backlog. Phase 1 is read-only, so restarting
	// from the beginning is always safe.
	var cursor int64
	if checkpoint {
		if cp, cperr := operations.LoadCheckpoint(de.bookStore, opID); cperr != nil {
			logging.Warn(ctx, "drain-stale: checkpoint load failed (starting from the beginning)", "op_id", opID, "error", cperr)
		} else if cp != nil && cp.Phase == drainStaleCheckpointPhase {
			cursor = int64(cp.PhaseIndex)
			logging.Info(ctx, "drain-stale: resuming from checkpoint", "op_id", opID, "after_candidate_id", cursor)
		} else if cp != nil {
			logging.Warn(ctx, "drain-stale: ignoring checkpoint from another phase (starting from the beginning)",
				"op_id", opID, "phase", cp.Phase, "phase_index", cp.PhaseIndex)
		}
	}

	scanMsg := func() string {
		return fmt.Sprintf("Scanned %d of %d pending exact candidates (%d would purge, %d kept)",
			result.Inspected, max(totalPendingExact, result.Inspected), result.WouldPurge, result.Kept)
	}
	report(0, totalPendingExact, scanMsg())

	// Phase 1: bounded, read-only scan collecting counts/samples (and, when
	// applying, the IDs to reclassify), keyset-paged in ascending candidate ID
	// order (ListCandidatesAfterID — see its doc for why offset paging was
	// quadratic). Marking is deferred to phase 2 so phase 1 stays read-only:
	// an apply interrupted mid-scan writes nothing.
	var toMark []int64
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		page, lerr := de.embedStore.ListCandidatesAfterID(filter, cursor, drainStaleBatchSize)
		if lerr != nil {
			return result, fmt.Errorf("drain-stale: list candidates after id %d: %w", cursor, lerr)
		}
		if len(page) == 0 {
			break
		}

		// Book/file/author reads for the whole page run on a bounded pool;
		// classification below stays serial, in candidate order, so counts,
		// samples and toMark are deterministic and need no lock.
		if perr := de.prefetchDrainBooks(ctx, page, cache, authorNames); perr != nil {
			return result, perr
		}

		for i := range page {
			c := page[i]
			result.Inspected++
			// A pinned manual candidate keeps its scanner layer, so an exact row
			// a human asked to review can land here, and a same-path pair is
			// review-queue-only too. Both stay in the queue (automated_guard.go).
			if database.IsManualCandidate(c) {
				result.Kept++
				continue
			}

			a, b := cache[c.EntityAID], cache[c.EntityBID]
			if _, refused := AutomatedResolutionRefusal(c, a.stub, b.stub); refused {
				result.Kept++
				continue
			}
			// A book, file or author read that FAILED (as opposed to a book
			// that is genuinely gone) says nothing about the pair. Keep it —
			// the drain errs toward keeping — rather than let a transient
			// store error land it in missing_book or strip its content
			// evidence. A later run re-evaluates it.
			if a.readErr || b.readErr {
				result.LookupErrors++
				result.Kept++
				continue
			}

			reason, purge := de.classifyStaleCandidate(c, a, b)
			if !purge {
				result.Kept++
				continue
			}
			result.WouldPurge++
			result.ReasonCounts[reason]++
			if len(result.Samples[reason]) < drainStaleSampleCap {
				result.Samples[reason] = append(result.Samples[reason], DrainStaleSample{
					CandidateID: c.ID,
					BookAID:     c.EntityAID,
					BookBID:     c.EntityBID,
					Reason:      reason,
				})
			}
			if apply {
				toMark = append(toMark, c.ID)
			}
		}

		cursor = page[len(page)-1].ID
		if checkpoint {
			// PhaseIndex carries the keyset cursor (last scanned candidate
			// ID), not a row count; drainStaleCheckpointPhase marks that.
			if serr := operations.SaveCheckpoint(de.bookStore, opID, "dedup:drain-stale", drainStaleCheckpointPhase, int(cursor), totalPendingExact); serr != nil {
				logging.Warn(ctx, "drain-stale: checkpoint save failed", "op_id", opID, "after_candidate_id", cursor, "error", serr)
			}
		}
		// The denominator is the count taken at the start; a scanner can add
		// rows mid-run (they sort last by ID and ARE scanned), so never let
		// done exceed total.
		report(result.Inspected, max(totalPendingExact, result.Inspected), scanMsg())

		if len(page) < drainStaleBatchSize {
			break
		}
	}

	// Phase 2 (apply only): soft-reclassify the collected would-purge rows by ID.
	// ReclassifyCandidate re-checks each row under the lock a manual pin takes:
	// phase 1 can run for a long time, and a pair a human pinned (or dismissed,
	// or merged) since it was read must not be moved to stale-drain. Rows it
	// refuses are counted in SkippedAtWrite. A re-run over the same IDs is safe:
	// an already-drained row is refused as no longer pending.
	//
	// KNOWN LIMITATION (apply-resume): marking is deferred to this phase, so an
	// apply interrupted mid-scan marks nothing, and a resumed run (starting after
	// the checkpoint cursor) marks only the rows after it. Rows before the resume
	// cursor would be left unmarked while the op-wrapper still sets its
	// done-flag. Apply must therefore be run as a single uninterrupted pass; to
	// re-run cleanly after an interruption, clear the checkpoint so it restarts
	// from the beginning. Apply is owner-greenlight-gated and not run by this task.
	if apply {
		markMsg := func(done int) string {
			return fmt.Sprintf("Marking stale-drain: %d of %d (%d left unchanged since the scan)",
				done, len(toMark), result.SkippedAtWrite)
		}
		report(0, len(toMark), markMsg(0))
		for i, id := range toMark {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			default:
			}
			uerr := de.embedStore.ReclassifyCandidate(id, "pending", staleDrainStatus)
			switch {
			case uerr == nil:
			case errors.Is(uerr, database.ErrManualCandidateProtected), errors.Is(uerr, database.ErrCandidateStatusChanged):
				result.SkippedAtWrite++
				logging.Info(ctx, "drain-stale: left a row changed since the scan", "candidate_id", id, "reason", uerr)
			default:
				logging.Error(ctx, "drain-stale: reclassify failed", "candidate_id", id, "error", uerr)
			}
			if done := i + 1; done%drainStaleMarkProgressEvery == 0 || done == len(toMark) {
				report(done, len(toMark), markMsg(done))
			}
		}
	}

	// Clean completion — drop the checkpoint so the next run starts fresh.
	if checkpoint {
		if cerr := operations.ClearState(de.bookStore, opID); cerr != nil {
			logging.Warn(ctx, "drain-stale: clear checkpoint failed", "op_id", opID, "error", cerr)
		}
	}

	logging.Info(ctx, "drain-stale: complete",
		"inspected", result.Inspected,
		"would_purge", result.WouldPurge,
		"kept", result.Kept,
		"lookup_errors", result.LookupErrors,
		"skipped_at_write", result.SkippedAtWrite,
		"apply", apply,
		"reason_counts", fmt.Sprintf("%v", result.ReasonCounts))

	return result, nil
}

// prefetchDrainBooks loads, on a bounded worker pool, the drainBookMeta of
// every book the page's non-manual candidates reference that is not already
// cached, then its uncached authors, and stores the results in cache and
// authorNames.
//
// Concurrency contract: workers only READ the store and write their own slot
// of a pre-sized result slice; cache and authorNames are written here, on the
// caller's goroutine, after each pool has drained. The pool is sized to
// drainStaleWorkers (runtime.NumCPU by default). registry.RunItems is not used
// because this runs inside the engine without a registry Reporter, and its
// per-item progress labels would fight the per-page progress the caller
// reports.
func (de *Engine) prefetchDrainBooks(ctx context.Context, page []database.DedupCandidate, cache map[string]drainBookMeta, authorNames map[int]string) error {
	// Unique uncached book IDs, in first-seen candidate order. Manual
	// candidates are kept without a lookup, so they are not fetched.
	var ids []string
	seen := make(map[string]struct{})
	for i := range page {
		if database.IsManualCandidate(page[i]) {
			continue
		}
		for _, id := range [2]string{page[i].EntityAID, page[i].EntityBID} {
			if _, ok := cache[id]; ok {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	metas := make([]drainBookMeta, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(drainStaleWorkers, 1))
	for i, id := range ids {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			metas[i] = de.fetchDrainBook(gctx, id)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("drain-stale: prefetch books: %w", err)
	}

	// Authors: unique, uncached, in first-seen order.
	var authorIDs []int
	seenA := make(map[int]struct{})
	for i := range metas {
		if metas[i].stub == nil || metas[i].stub.AuthorID == nil {
			continue
		}
		aid := *metas[i].stub.AuthorID
		if _, ok := authorNames[aid]; ok {
			continue
		}
		if _, ok := seenA[aid]; ok {
			continue
		}
		seenA[aid] = struct{}{}
		authorIDs = append(authorIDs, aid)
	}
	names := make([]string, len(authorIDs))
	nameErr := make([]bool, len(authorIDs))
	if len(authorIDs) > 0 {
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(max(drainStaleWorkers, 1))
		for i, aid := range authorIDs {
			g.Go(func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				a, err := de.bookStore.GetAuthorByID(aid)
				switch {
				case err != nil:
					nameErr[i] = true
					logging.Warn(gctx, "drain-stale: author read failed; author treated as unresolved", "author_id", aid, "error", err)
				case a != nil:
					names[i] = a.Name
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return fmt.Errorf("drain-stale: prefetch authors: %w", err)
		}
	}
	for i, aid := range authorIDs {
		if nameErr[i] {
			// Not cached, so a later page's book by this author retries the
			// read. This page's books by it get "", which cannot make a pair
			// drain: titleEvidenceRefusal's placeholder-author rule needs a
			// non-empty placeholder name on BOTH sides, so a failed author
			// read only ever keeps a pair it might otherwise have drained.
			continue
		}
		authorNames[aid] = names[i]
	}

	for i, id := range ids {
		m := metas[i]
		if m.stub != nil && m.stub.AuthorID != nil {
			m.authorName = authorNames[*m.stub.AuthorID]
		}
		// Every fetched ID is cached, read errors included: the classify
		// loop reads the cache for both sides of each pair, and a readErr
		// entry is what makes it keep the pair. The failure therefore
		// sticks for the rest of this run (the pair is re-evaluated by the
		// next run), and the Warn is logged once per book, not per pair.
		cache[id] = m
	}
	return nil
}

// fetchDrainBook reads one book's gate inputs: the stub, the canonical runtime
// and file-row count, and the content hashes. GetBookByID's (nil, nil) is a
// genuinely missing book; an error from any read marks readErr instead, so a
// transient failure is never classified as missing_book (see the caller).
func (de *Engine) fetchDrainBook(ctx context.Context, id string) drainBookMeta {
	var m drainBookMeta
	b, err := de.bookStore.GetBookByID(id)
	if err != nil {
		logging.Warn(ctx, "drain-stale: book read failed; pairs with this book are kept", "book_id", id, "error", err)
		m.readErr = true
		return m
	}
	if b == nil {
		m.missing = true
		return m
	}
	m.stub = &database.Book{
		ID:                 b.ID,
		Title:              b.Title,
		FilePath:           b.FilePath, // for SamePathPair
		Duration:           b.Duration,
		ISBN10:             b.ISBN10,
		ISBN13:             b.ISBN13,
		ASIN:               b.ASIN,
		IsPrimaryVersion:   b.IsPrimaryVersion,
		AuthorID:           b.AuthorID,
		MetadataSourceHash: b.MetadataSourceHash,
	}
	// One file-row read serves both the runtime gates and the
	// content-evidence check.
	files, ferr := de.bookStore.GetBookFiles(b.ID)
	if ferr != nil {
		// Without the file rows the content-evidence check cannot see the
		// file hashes, so a placeholder-title pair that shares a file could
		// be drained on a read error. Keep instead.
		logging.Warn(ctx, "drain-stale: book files read failed; pairs with this book are kept", "book_id", id, "error", ferr)
		m.readErr = true
	}
	m.runtime, m.fileRows, m.filesOK = runtimeFromFiles(b, files, ferr)
	m.hashes = bookContentHashes(b, files)
	return m
}

// classifyStaleCandidate re-runs upsertExactCandidate's full guard chain —
// isNonPrimaryVersion, identifiersConflict, isBoilerplateTitle,
// hasKnownShortDuration, isPartVsWholeMismatch, in the SAME order the
// chokepoint applies them — against a pair's CURRENT data. It returns the
// first rejecting reason and true if the candidate would no longer be
// emitted today, or ("", false) if it still passes every gate and must be
// kept.
//
// This reuses the real predicate functions verbatim rather than
// reimplementing them, so the re-evaluation can never drift from what the
// live emitter actually does.
//
// non_primary_version (INIT-2 T3, drain-gate parity): originally omitted
// here on the theory that PurgeStaleCandidates already handles non-primary
// pairs — but PurgeStaleCandidates is a DIFFERENT op (hard-delete, all
// layers, run at startup/rescan, not scoped to this drain's pending
// exact-layer backlog). A pending exact candidate can still involve a
// non-primary book between PurgeStaleCandidates runs, so the chokepoint's
// first gate needs its own twin here for the drain's report/apply to be a
// true preview of what upsertExactCandidate would (not) emit today. The
// soft-reclassify (never delete) semantics keep this idempotent alongside
// PurgeStaleCandidates' separate hard-delete sweep — a row either path
// already removed simply never appears in this scan again.
//
// placeholder_title runs LAST, after every chokepoint twin, so a pair an
// existing gate already rejects keeps its existing reason in the report.
func (de *Engine) classifyStaleCandidate(c database.DedupCandidate, a, b drainBookMeta) (string, bool) {
	// A missing book on either side means the candidate can't be actioned — the
	// same conservative treatment PurgeStaleCandidates gives missing books.
	// This check has no chokepoint twin (upsertExactCandidate always receives
	// live, already-loaded *database.Book pointers, never a dangling ID) — it
	// exists only because the drain re-resolves books by ID.
	if a.missing || b.missing {
		return drainReasonMissingBook, true
	}
	if isNonPrimaryVersion(a.stub) || isNonPrimaryVersion(b.stub) {
		return drainReasonNonPrimaryVersion, true
	}
	if identifiersConflict(a.stub, b.stub) {
		return drainReasonIdentifierConflict, true
	}
	if isBoilerplateTitle(a.stub.Title) || isBoilerplateTitle(b.stub.Title) {
		return drainReasonBoilerplateTitle, true
	}
	// Same gates as upsertExactCandidate (hasKnownShortDuration,
	// isPartVsWholeMismatch), on the runtimes cached at lookup.
	if shortRuntime(a.runtime) || shortRuntime(b.runtime) {
		return drainReasonShortDuration, true
	}
	if a.filesOK && b.filesOK && a.fileRows > 0 && b.fileRows > 0 &&
		partVsWholeRuntime(a.runtime, a.fileRows, b.runtime, b.fileRows) {
		return drainReasonPartVsWhole, true
	}
	if placeholderOnlyEvidence(c, a, b) {
		return drainReasonPlaceholderTitle, true
	}
	return "", false
}

// placeholderOnlyEvidence reports whether a pending exact pair rests on
// nothing but a placeholder title (titleEvidenceRefusal) with no content
// evidence beside it. Most such rows predate provenance and carry no
// breakdown, so the check re-derives the content evidence from the books'
// CURRENT data; a row whose breakdown records a content rule or a scoring
// non-title signal is kept even if that evidence has since changed, because
// the drain must err toward keeping a pair a human can still look at.
func placeholderOnlyEvidence(c database.DedupCandidate, a, b drainBookMeta) bool {
	if titleEvidenceRefusal(a.stub.Title, b.stub.Title, a.authorName, b.authorName) == "" {
		return false
	}
	if recordsContentEvidence(c) {
		return false
	}
	return !sharesContentEvidence(a, b)
}

// titleOnlyScoringKinds are the scoring signals whose evidence is the title
// (text similarity). Every other scoring kind is content evidence.
var titleOnlyScoringKinds = map[unified.SignalKind]bool{
	unified.SigMetaFuzzy:   true,
	unified.SigEmbedHigh:   true,
	unified.SigEmbedMedium: true,
}

// recordsContentEvidence reports whether the candidate's stored breakdown
// names a content rule, or a scoring signal whose evidence is not the title.
func recordsContentEvidence(c database.DedupCandidate) bool {
	if c.ScoreBreakdown == nil {
		return false
	}
	for _, s := range c.ScoreBreakdown.Signals {
		if s.Kind == unified.SigExactRule {
			switch s.Rule {
			case ExactRuleFileHash, ExactRuleISBNASIN, ExactRuleMetadataHash, ExactRuleOrganizeCollision:
				return true
			}
			continue
		}
		// Supporting kinds (duration, folder_path, same_path, cover_text) can
		// never be the reason a pair exists (unified.IsSupportingKind), so
		// they are not content evidence either. An unknown kind counts as
		// content — the direction that keeps a pair.
		if s.Confidence > 0 && !titleOnlyScoringKinds[s.Kind] && !unified.IsSupportingKind(s.Kind) {
			return true
		}
	}
	return false
}

// sharesContentEvidence reports whether two books share any content-level
// identity the exact content rules pair on: a file hash, an ISBN/ASIN, or a
// metadata source record.
func sharesContentEvidence(a, b drainBookMeta) bool {
	for h := range a.hashes {
		if _, ok := b.hashes[h]; ok {
			return true
		}
	}
	if len(isbnASINEvidenceShared(a.stub, b.stub)) > 0 {
		return true
	}
	ma, mb := derefStr(a.stub.MetadataSourceHash), derefStr(b.stub.MetadataSourceHash)
	return ma != "" && ma == mb
}

// bookContentHashes is every content hash a book carries: the book-level
// file_hash / original / organized hashes and each file row's hash.
func bookContentHashes(b *database.Book, files []database.BookFile) map[string]struct{} {
	out := make(map[string]struct{}, len(files)+3)
	for _, h := range []*string{b.FileHash, b.OriginalFileHash, b.OrganizedFileHash} {
		if h != nil && *h != "" {
			out[*h] = struct{}{}
		}
	}
	for i := range files {
		if files[i].FileHash != "" {
			out[files[i].FileHash] = struct{}{}
		}
		if files[i].OriginalFileHash != "" {
			out[files[i].OriginalFileHash] = struct{}{}
		}
	}
	return out
}
