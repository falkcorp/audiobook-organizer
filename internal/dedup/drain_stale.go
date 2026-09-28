// file: internal/dedup/drain_stale.go
// version: 1.5.0
// guid: 60d982e2-6836-4327-9ddf-9b55375f39ea
// last-edited: 2026-09-27

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
// ListCandidate's Limit/Offset (never Limit:1000000), and the per-run book
// cache retains only the handful of fields the gates read — never a full
// database.Book and never a BookSigV1 string. Note that ListCandidates itself
// re-scans+re-sorts the whole candidate table per call, so this paging bounds
// the CALLER's retained set, not the store's transient per-call allocation.

package dedup

import (
	"context"
	"errors"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/logging"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// drainStaleBatchSize is the page size used when streaming pending exact
// candidates. Mirrors checkExactISBNScan's 500-row batching so the caller never
// materialises the full ~384K candidate backlog in one shot (DEDUP-5). It is a
// var (not a const) only so tests can lower it to exercise page-boundary logic.
var drainStaleBatchSize = 500

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

// DrainStaleCandidates re-evaluates pending exact-layer candidates against the
// current guard chain and reports/optionally drains the stale ones. See the
// package doc above for the full contract.
//
// opID: when non-empty AND apply is true, the run checkpoints its page offset
// via operations.SaveCheckpoint and resumes from operations.LoadCheckpoint so an
// interrupted apply resumes mid-backlog. Checkpoint/resume is deliberately NOT
// applied to dry runs: a dry run must always full-scan so its report totals are
// complete (a resumed-and-therefore-partial report would silently undercount the
// counts a human reviews before greenlighting the destructive apply).
// apply: false (default) writes nothing; true soft-reclassifies would-purge rows.
func (de *Engine) DrainStaleCandidates(ctx context.Context, opID string, apply bool) (*DrainStaleResult, error) {
	if de.embedStore == nil || de.bookStore == nil {
		return nil, fmt.Errorf("drain-stale: embedding or book store not available")
	}

	result := &DrainStaleResult{
		ReasonCounts: make(map[string]int),
		Samples:      make(map[string][]DrainStaleSample),
	}

	// Small per-run cache: book ID -> minimal stub (or missing marker). A book
	// referenced by many candidates is only fetched once. Only the tiny field
	// set the gates read is retained; the full row fetched by GetBookByID is
	// transient and discarded.
	cache := make(map[string]drainBookMeta)
	// Author names are cached by author ID: the placeholder authors this
	// gate exists for hang hundreds of books off one row.
	authorNames := make(map[int]string)
	authorName := func(authorID *int) string {
		if authorID == nil {
			return ""
		}
		if n, ok := authorNames[*authorID]; ok {
			return n
		}
		n := ""
		if a, err := de.bookStore.GetAuthorByID(*authorID); err == nil && a != nil {
			n = a.Name
		}
		authorNames[*authorID] = n
		return n
	}
	lookup := func(id string) drainBookMeta {
		if m, ok := cache[id]; ok {
			return m
		}
		var m drainBookMeta
		b, err := de.bookStore.GetBookByID(id)
		if err != nil || b == nil {
			m.missing = true
		} else {
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
			m.runtime, m.fileRows, m.filesOK = runtimeFromFiles(b, files, ferr)
			m.authorName = authorName(b.AuthorID)
			m.hashes = bookContentHashes(b, files)
		}
		cache[id] = m
		return m
	}

	// Checkpoint/resume is scoped to the APPLY path only. A dry run MUST always
	// full-scan from offset 0: its whole purpose is to produce a COMPLETE report
	// (total inspected/would-purge/kept) that a human reviews before greenlighting
	// the destructive apply, and a partial-because-resumed report would silently
	// undercount. Confining checkpoint I/O to apply also prevents a stale dry-run
	// checkpoint from ever being read by a later apply (cross-mode contamination).
	checkpoint := apply && opID != ""

	// Total pending-exact count for the checkpoint denominator (apply only). A
	// tiny Limit read returns the full filtered total without materialising it.
	totalPendingExact := 0
	if checkpoint {
		_, total, cerr := de.embedStore.ListCandidates(database.CandidateFilter{
			EntityType: "book",
			Status:     "pending",
			Layer:      "exact",
			Limit:      1,
		})
		if cerr != nil {
			return nil, fmt.Errorf("drain-stale: count pending exact candidates: %w", cerr)
		}
		totalPendingExact = total
	}

	// Resume from a saved checkpoint offset if present (apply only).
	offset := 0
	if checkpoint {
		if cp, cperr := operations.LoadCheckpoint(de.bookStore, opID); cperr != nil {
			logging.Warn(ctx, "drain-stale: checkpoint load failed (starting from 0)", "op_id", opID, "error", cperr)
		} else if cp != nil {
			offset = cp.PhaseIndex
			logging.Info(ctx, "drain-stale: resuming from checkpoint", "op_id", opID, "offset", offset)
		}
	}

	// Phase 1: bounded, read-only scan collecting counts/samples (and, when
	// applying, the IDs to reclassify). Marking is deferred to phase 2 so we
	// never mutate the candidate set while paging by offset (which would shift
	// rows and skip/double-count them).
	var toMark []int64
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		page, _, lerr := de.embedStore.ListCandidates(database.CandidateFilter{
			EntityType: "book",
			Status:     "pending",
			Layer:      "exact",
			Limit:      drainStaleBatchSize,
			Offset:     offset,
		})
		if lerr != nil {
			return result, fmt.Errorf("drain-stale: list candidates at offset %d: %w", offset, lerr)
		}
		if len(page) == 0 {
			break
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

			a := lookup(c.EntityAID)
			b := lookup(c.EntityBID)
			if _, refused := AutomatedResolutionRefusal(c, a.stub, b.stub); refused {
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

		offset += len(page)
		if checkpoint {
			if serr := operations.SaveCheckpoint(de.bookStore, opID, "dedup:drain-stale", "scanning", offset, totalPendingExact); serr != nil {
				logging.Warn(ctx, "drain-stale: checkpoint save failed", "op_id", opID, "offset", offset, "error", serr)
			}
		}

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
	// apply interrupted mid-scan marks nothing, and a resumed run (starting at the
	// checkpoint offset) marks only the rows from that offset onward. Rows before
	// the resume offset would be left unmarked while the op-wrapper still sets its
	// done-flag. Apply must therefore be run as a single uninterrupted pass; to
	// re-run cleanly after an interruption, clear the checkpoint so it restarts
	// from offset 0. Apply is owner-greenlight-gated and not run by this task.
	if apply {
		for _, id := range toMark {
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
		"skipped_at_write", result.SkippedAtWrite,
		"apply", apply,
		"reason_counts", fmt.Sprintf("%v", result.ReasonCounts))

	return result, nil
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
		if s.Confidence > 0 && !titleOnlyScoringKinds[s.Kind] {
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
