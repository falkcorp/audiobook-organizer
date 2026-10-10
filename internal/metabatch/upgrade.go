// file: internal/metabatch/upgrade.go
// version: 2.4.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-10
//
// Background job that upgrades metadata from lower-quality sources
// (Open Library, Google Books, Wikipedia) to richer ones (Hardcover,
// Audible/Audnexus) when a high-confidence match is available. Backlog 7.4.
//
// The upgrade targets books tagged `metadata:source:<slug>` for every slug
// ranked at or below metafetch.LowQualitySourceMaxRank; the rank table in
// internal/metafetch/source_rank.go is the only list. For each book, the job
// re-runs the full metadata search pipeline against ALL configured sources.
// A candidate may replace the book's metadata only when its source ranks
// strictly higher than the book's current source (metafetch.SourceOutranks)
// and it passes the shared bulk-apply gate (internal/applygate). Owner
// decision 2026-09-27: such an upgrade REPLACES filled fields (user-locked
// fields are still kept); see tryUpgradeBook.
//
// The job leverages the metadata fetch cache (PR #250) so re-fetches
// for already-queried sources are free. Only sources that returned
// empty on the initial fetch will actually hit the API.

package metabatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// UpgradeFetcher is the part of *metafetch.Service the upgrade uses: the
// search and the apply. Tests substitute the search while keeping the real
// apply.
type UpgradeFetcher interface {
	SearchMetadataForBook(id string, query string, authorHint ...string) (*metafetch.SearchMetadataResponse, error)
	ApplyMetadataCandidateWithOptions(id string, candidate metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) (*metafetch.FetchMetadataResponse, error)
}

// MetadataUpgradeService finds books with low-quality metadata
// sources and attempts to upgrade them to richer sources.
type MetadataUpgradeService struct {
	DB      Store
	Fetcher UpgradeFetcher
}

// NewMetadataUpgradeService creates an upgrade service. The fetcher
// provides the search + apply pipeline; the db provides the tag
// lookup for finding eligible books. A nil fetcher leaves Fetcher nil (not a
// typed nil inside the interface), so RunUpgrade reports it unconfigured.
func NewMetadataUpgradeService(db Store, fetcher *metafetch.Service) *MetadataUpgradeService {
	svc := &MetadataUpgradeService{DB: db}
	if fetcher != nil {
		svc.Fetcher = fetcher
	}
	return svc
}

// LowQualitySources lists the metadata source slugs whose books are
// candidates for upgrade (visited in book-ID order from the sweep cursor,
// upgradeCursorKey, not source by source). It is derived from
// the rank table (metafetch.LowQualitySourceSlugs: open_library,
// google_books, wikipedia); there is no second list. The tag namespace is
// metadata:source:<slug> (metafetch.MetadataSourceSlug).
var LowQualitySources = metafetch.LowQualitySourceSlugs()

// upgradeWorkers bounds how many books are searched and applied at once. The
// work is network-bound (a metadata search per book), so a small fixed pool:
// enough to overlap provider latency, few enough to stay inside the
// providers' own rate limits. The book IDs are de-duplicated before dispatch
// (and a book carries one metadata:source tag), so no two workers ever apply
// to the same book.
//
// An apply also touches rows other than its own book: it resolves or creates
// author/series rows, and when two books end up sharing a source hash it runs
// the MATCH-4 primary election (checkMetadataSourceHashDuplicates) over every
// book carrying that hash. Concurrent applies through that same path already
// run in production at the same width: batch-apply-cached drives
// ApplyMetadataCandidateWithOptions from registry.RunItems with
// writeBackWorkers() (default 4). The election re-reads every book sharing
// the hash and picks the survivor from their stored signals, so two books
// upgraded to one record at once converge on the same primary whichever
// commits last.
const upgradeWorkers = 4

// upgradeLog is the upgrade's subsystem logger (internal/logger, printf-style).
// Every book ID, title, source slug and error string it logs goes through
// logger.SanitizeLogValue.
var upgradeLog = logger.New("metadata-upgrade")

// UpgradeResult summarizes what the upgrade job did.
type UpgradeResult struct {
	Checked  int `json:"checked"`
	Upgraded int `json:"upgraded"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
	// OwnerManualOnly counts books left alone because they are Doctor Who /
	// Big Finish / Torchwood (applygate.BulkManualOnlyGuard). They are counted
	// in Checked, not in Skipped.
	OwnerManualOnly int `json:"owner_manual_only"`
	// CursorStart and CursorEnd are the sweep positions this run started
	// after and stopped at ("" is the start of the list); Wrapped says the
	// run reached the end of the list and continued from its start.
	CursorStart string `json:"cursor_start"`
	CursorEnd   string `json:"cursor_end"`
	Wrapped     bool   `json:"wrapped"`
}

// upgradeOutcome is what tryUpgradeBook did with one book, for the op log.
// Reason is set whenever Upgraded is false and no error was returned: it is
// the "why not" that used to be a silent `false`.
type upgradeOutcome struct {
	Upgraded bool
	Reason   string
	// BookTitle is the book's title when it was read ("" before that).
	BookTitle string
	// From and To are the source slugs of an upgrade; CandidateTitle and
	// Score describe the candidate that replaced the metadata.
	From, To       string
	CandidateTitle string
	Score          float64
}

// Skip reasons reported on upgradeOutcome.Reason.
const (
	upgradeSkipUnranked         = "current source is unranked; it is never replaced"
	upgradeSkipMarkedNoMatch    = "owner marked the book no match"
	upgradeSkipNoResults        = "search returned no candidates"
	upgradeSkipNoneOutrank      = "no candidate from a higher-ranked source"
	upgradeSkipGateRefused      = "every higher-ranked candidate was refused by the gate"
	upgradeSkipMarkedDuringDone = "owner marked the book no match while it was being searched"
)

// errOwnerManualOnly is tryUpgradeBook's skip for an owner-manual-only book.
var errOwnerManualOnly = errors.New("owner-manual-only book (Doctor Who / Big Finish / Torchwood)")

// upgradeCursorKey is the STABLE operation-state key of the upgrade's sweep
// position, carried from one run to the next (the pattern the retired
// isbn-enrichment sweep used; its cursor went with it on 2026-10-02). Without it every run walked the eligible
// books from the top and stopped at the cap, so books that never upgrade
// (no higher-ranked candidate, or a gate refusal) were re-searched every
// night and the rest of the list was never reached.
const upgradeCursorKey = "metabatch:metadata-upgrade-cursor"

// upgradeCursor is the persisted sweep position: the ID of the last book a
// run dispatched. The next run resumes strictly after it in book-ID order
// and wraps to the start once the end is reached.
type upgradeCursor struct {
	AfterID   string    `json:"after_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// loadUpgradeCursor reads the sweep position. Fail-open: an error or a
// malformed blob starts from the top.
func (s *MetadataUpgradeService) loadUpgradeCursor() string {
	data, err := s.DB.GetOperationState(upgradeCursorKey)
	if err != nil {
		upgradeLog.Warn("reading sweep cursor failed; starting from the top: err=%s", logger.SanitizeLogValue(err.Error()))
		return ""
	}
	if len(data) == 0 {
		return ""
	}
	var cur upgradeCursor
	if err := json.Unmarshal(data, &cur); err != nil {
		upgradeLog.Warn("sweep cursor is malformed; starting from the top: err=%s", logger.SanitizeLogValue(err.Error()))
		return ""
	}
	return cur.AfterID
}

// saveUpgradeCursor persists the sweep position. A write error is logged,
// never returned: failing to checkpoint must not fail the run.
func (s *MetadataUpgradeService) saveUpgradeCursor(afterID string) {
	data, err := json.Marshal(upgradeCursor{AfterID: afterID, UpdatedAt: time.Now()})
	if err != nil {
		upgradeLog.Warn("marshalling sweep cursor failed: err=%s", logger.SanitizeLogValue(err.Error()))
		return
	}
	if err := s.DB.SaveOperationState(upgradeCursorKey, data); err != nil {
		upgradeLog.Warn("persisting sweep cursor failed: err=%s", logger.SanitizeLogValue(err.Error()))
	}
}

// MinUpgradeConfidence is the minimum score a non-current-source
// candidate must achieve to trigger an automatic metadata apply.
// Set conservatively high to avoid upgrading to a worse match. It is the
// shared bulk-apply floor (internal/applygate), not a number of its own.
const MinUpgradeConfidence = applygate.MinScore

// MinUpgradeConfidenceWithTranscription relaxes the gate when the candidate
// independently matches the book's audio-derived title/author.
const MinUpgradeConfidenceWithTranscription = applygate.MinScoreAudioConfirmed

// RunUpgrade scans for books tagged with low-quality metadata
// sources and attempts to find a better match from other sources.
// Respects context cancellation so it can be run as a long-running
// operation with a kill switch.
//
// progress may be nil (M7, 2026-07 error-correction sweep): before this, the
// op reported nothing between "starting" and the final result while checking
// up to `limit` books, each involving a network metadata search — a 30+
// minute silent stretch indistinguishable from a hang. When non-nil,
// progress is reported every 25 books checked (and once more at the end).
func (s *MetadataUpgradeService) RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*UpgradeResult, error) {
	if s.Fetcher == nil {
		return nil, fmt.Errorf("metadata fetch service not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	// Fail-safe cap (internal/applycap): `limit` bounds how many books this run
	// may re-fetch AND apply. Callers pass 200 today; a caller asking for more
	// than the cap is refused up front rather than applying the first cap-many.
	if err := applycap.Check("metadata.upgrade", limit, config.AppConfig.BulkApplyMaxItems); err != nil {
		return nil, err
	}

	// Every eligible book (tagged with a low-quality source), de-duplicated
	// and sorted by book ID so the persisted sweep cursor has a stable order.
	// A book carries one metadata:source tag; should one ever carry two, the
	// first source in LowQualitySources order is kept.
	type item struct{ bookID, sourceSlug string }
	var all []item
	seen := map[string]bool{}
	for _, sourceSlug := range LowQualitySources {
		tag := "metadata:source:" + sourceSlug
		bookIDs, err := s.DB.GetBooksByTag(tag)
		if err != nil {
			upgradeLog.Warn("GetBooksByTag failed: tag=%s err=%s", tag, logger.SanitizeLogValue(err.Error()))
			continue
		}
		upgradeLog.Info("found books tagged: count=%d tag=%s", len(bookIDs), tag)
		for _, bookID := range bookIDs {
			if seen[bookID] {
				continue
			}
			seen[bookID] = true
			all = append(all, item{bookID, sourceSlug})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].bookID < all[j].bookID })

	// Take up to limit books starting strictly after the cursor, wrapping to
	// the start of the list, never taking a book twice in one run.
	result := &UpgradeResult{}
	afterID := s.loadUpgradeCursor()
	result.CursorStart = afterID
	start := sort.Search(len(all), func(i int) bool { return all[i].bookID > afterID })
	var items []item
	for k := 0; k < len(all) && len(items) < limit; k++ {
		idx := start + k
		if idx >= len(all) {
			idx -= len(all)
			if afterID != "" {
				result.Wrapped = true
			}
		}
		items = append(items, all[idx])
	}
	upgradeLog.Info("sweep: eligible=%d this_run=%d after=%s wrapped=%v",
		len(all), len(items), logger.SanitizeLogValue(afterID), result.Wrapped)

	total := len(items)
	var mu sync.Mutex // guards result and the progress stamp
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(upgradeWorkers)
	var stopErr error
	lastDispatched := ""
	for _, it := range items {
		// Stop dispatching once the hold is lost or the op is canceled; the
		// workers already running see the same through gctx.
		if err := opsregistry.ScanStandDownCheckpoint(gctx); err != nil {
			stopErr = err
			break
		}
		lastDispatched = it.bookID
		g.Go(func() error {
			// Per-book stand-down beat: tryUpgradeBook is a network call, so
			// the scan hold must be renewed per book, right before its write,
			// not per 25-book progress stamp. Safe for concurrent use.
			if err := opsregistry.ScanStandDownCheckpoint(gctx); err != nil {
				return err
			}
			outcome, upgradeErr := s.tryUpgradeBook(gctx, it.bookID, it.sourceSlug)

			mu.Lock()
			defer mu.Unlock()
			result.Checked++
			switch {
			case errors.Is(upgradeErr, errOwnerManualOnly):
				upgradeLog.Info("skipped owner-manual-only book (Doctor Who / Big Finish / Torchwood): id=%s", logger.SanitizeLogValue(it.bookID))
				result.OwnerManualOnly++
			case upgradeErr != nil:
				upgradeLog.Warn("book failed: id=%s err=%s", logger.SanitizeLogValue(it.bookID), logger.SanitizeLogValue(upgradeErr.Error()))
				result.Errors++
			case outcome.Upgraded:
				result.Upgraded++
			default:
				result.Skipped++
			}
			// One op-log line per book: upgraded (from -> to, candidate,
			// score), skipped (why) or error. Before 2026-09-28 only the
			// process log saw upgrades and a skip was a silent `false`.
			if progress != nil {
				level, line := upgradeOutcomeLine(it.bookID, it.sourceSlug, outcome, upgradeErr)
				_ = progress.Log(level, line, nil)
			}
			if progress != nil && (result.Checked%25 == 0 || result.Checked >= total) {
				_ = progress.UpdateProgress(result.Checked, total, fmt.Sprintf(
					"metadata upgrade: %d/%d books checked (%d upgraded, %d skipped, %d errors)",
					result.Checked, total, result.Upgraded, result.Skipped, result.Errors))
			}
			return nil
		})
	}
	waitErr := g.Wait()
	// Advance the cursor past every book this run dispatched, whatever its
	// outcome: a book that did not upgrade leaves the front of the queue
	// until the sweep comes round again, so it cannot starve the rest.
	if lastDispatched != "" {
		s.saveUpgradeCursor(lastDispatched)
		result.CursorEnd = lastDispatched
	} else {
		result.CursorEnd = afterID
	}
	if waitErr != nil {
		return result, waitErr
	}
	if stopErr != nil {
		return result, stopErr
	}
	return result, nil
}

// transcriptionConfirmsCandidate returns true when the candidate's title/author
// independently matches the book's audio-derived (transcribed) title/author.
// The rule lives in internal/applygate so every bulk-apply path shares it.
func transcriptionConfirmsCandidate(book *database.Book, c *metafetch.MetadataCandidate) bool {
	return applygate.TranscriptionConfirms(book, c)
}

// isOwnerManualOnly reports whether the book belongs to a library the owner
// curates by hand (Doctor Who / Big Finish / Torchwood), through the same
// guard every bulk apply uses (applygate.BulkManualOnlyGuard with all four
// readers, then ManualOnlyDetail): the book's path, title, narrator,
// publisher, transcribed fields, series name, author credits, franchise tags
// and every file row's path and transcribed fields. A read failure is
// returned rather than read as "not manual-only", which would loosen the
// rule.
func (s *MetadataUpgradeService) isOwnerManualOnly(book *database.Book) (bool, error) {
	g := applygate.BulkManualOnlyGuard(applygate.ManualOnlyReaders{Files: s.DB, Series: s.DB, Authors: s.DB, Tags: s.DB}, book, "")
	if g.ReadErr != "" {
		return false, errors.New(g.ReadErr)
	}
	reason, _ := applygate.ManualOnlyDetail(book, nil, applygate.TranscribedSearch{}, g)
	return reason == applygate.ReasonOwnerManualOnly, nil
}

// tryUpgradeBook re-searches metadata for a single book and applies the best
// candidate from a strictly higher-ranked source if the gate passes it. The
// outcome says whether an upgrade was applied and, when not, why.
func (s *MetadataUpgradeService) tryUpgradeBook(ctx context.Context, bookID, currentSourceSlug string) (upgradeOutcome, error) {
	var out upgradeOutcome
	skip := func(reason string) (upgradeOutcome, error) {
		out.Reason = reason
		return out, nil
	}
	// An unranked current source is never replaced (metafetch.SourceOutranks
	// refuses every candidate for it); skip before spending a search on it.
	currentRank, ranked := metafetch.SourceRank(currentSourceSlug)
	if !ranked {
		return skip(upgradeSkipUnranked)
	}
	book, err := s.DB.GetBookByID(bookID)
	if err != nil || book == nil {
		return out, fmt.Errorf("book not found: %s", bookID)
	}
	out.BookTitle = book.Title
	// The owner marked this book "no match": skip before the search so no
	// provider quota is spent on a match that the apply would refuse anyway
	// (ApplyMetadataCandidateWithOptions returns ErrMarkedNoMatch for an
	// automatic apply, which this is: UnseenCandidate below).
	if metafetch.IsMarkedNoMatch(book.MetadataReviewStatus) {
		return skip(upgradeSkipMarkedNoMatch)
	}
	// Standing owner rule: Doctor Who / Big Finish / Torchwood are never
	// touched by a bulk or automatic apply; the owner applies them by hand.
	// Checked before the search so no provider quota is spent on them.
	manual, merr := s.isOwnerManualOnly(book)
	if merr != nil {
		return out, merr
	}
	if manual {
		return out, errOwnerManualOnly
	}

	// The owner's candidate rejections: a rejected candidate is never the
	// upgrade, whatever it scores (the gate's owner_rejected leg). Read
	// before the search so an unreadable list costs no provider quota, and
	// refused rather than read as "nothing rejected".
	rejections := applygate.LoadRejections(s.DB, bookID)
	if rejections.ReadErr != nil {
		return skip(fmt.Sprintf("%s (%v)", applygate.ReasonOwnerRejectionCheckFailed, rejections.ReadErr))
	}

	// Run the full search pipeline — this goes through the
	// metadata fetch cache, so sources that were already queried
	// (and returned non-empty) won't hit the API again. Sources
	// that returned empty last time WILL be retried because the
	// cache only stores non-empty results.
	resp, err := s.Fetcher.SearchMetadataForBook(bookID, book.Title)
	if err != nil {
		return out, fmt.Errorf("search failed: %w", err)
	}
	if resp == nil || len(resp.Results) == 0 {
		return skip(upgradeSkipNoResults)
	}

	// The gate's runtime check compares the canonical runtime (sum over the
	// book's files), never Book.Duration, which is a partial sum for a
	// multi-file book whose chapters were not all probed. A read failure makes
	// the runtime unknown — missing evidence — rather than a false mismatch.
	rt, rtErr := database.LoadBookRuntime(s.DB, book)
	if rtErr != nil {
		logging.Warn(ctx, "upgrade: book files unreadable; runtime treated as unknown", "id", bookID, "err", rtErr)
	}

	// The gate judges each candidate's author against the book's LIVE
	// authors, never the Book.Author snapshot (applygate.Authors). A failed
	// read fails the book rather than judging it authorless, which would
	// loosen the gate.
	authors, aerr := database.LiveBookAuthorNames(s.DB, book)
	if aerr != nil {
		return out, fmt.Errorf("read authors: %w", aerr)
	}

	// Find the best candidate from a source that strictly OUTRANKS the
	// current one (metafetch.SourceOutranks): the same or a lower rank is
	// never an upgrade, so an Audible book is never "upgraded" to Open
	// Library, and Audible and Audnexus never replace each other. Among the
	// ones the gate passes, the highest rank wins and score breaks ties, so
	// an Open Library book lands on Audible rather than on a better-scoring
	// Hardcover candidate that a later run would never revisit (hardcover is
	// not a low-quality source).
	var bestCandidate *metafetch.MetadataCandidate
	bestRank := currentRank
	bestSlug := ""
	// outranking counts candidates from a higher-ranked source, and
	// refusals keeps the gate's reason for each one it refused, so a skip
	// can say whether nothing outranked the book or the gate said no.
	outranking := 0
	var refusals []string
	for i := range resp.Results {
		c := &resp.Results[i]
		candidateSlug := metafetch.MetadataSourceSlug(c.Source)
		if !metafetch.SourceOutranks(candidateSlug, currentSourceSlug) {
			continue
		}
		candidateRank, _ := metafetch.SourceRank(candidateSlug)
		outranking++

		// The shared bulk-apply gate (internal/applygate), unchanged by the
		// replace mode: the transcription hard gate (a book with a transcribed
		// title never takes a candidate that does not match it), the 0.90 /
		// 0.85-with-audio score floor, and the sequence-number guard, so
		// "Big Cats 3" can never upgrade "Big Cats 1" however well it scores.
		// There is no owner override here: this is an automatic apply. There
		// is no cache-identity leg either: the candidates were searched a
		// moment ago from the book's current fields, so nothing can have
		// drifted. A candidate the owner rejected is refused (owner_rejected).
		v := applygate.EvaluateInBatch(book, authors, rt, c, nil, nil, rejections)
		upgradeLog.Debug("gate: id=%s source=%s score=%.3f floor=%.3f transcription_confirms=%v allowed=%v reason=%s detail=%s",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(candidateSlug), c.Score, v.ScoreFloor,
			v.AudioConfirmed, v.Allowed, v.Reason, logger.SanitizeLogValue(v.Detail))
		if !v.Allowed {
			refusals = append(refusals, fmt.Sprintf("%s %.2f: %s", candidateSlug, c.Score, v.Reason))
			continue
		}
		if bestCandidate == nil || candidateRank > bestRank || (candidateRank == bestRank && c.Score > bestCandidate.Score) {
			bestCandidate, bestRank, bestSlug = c, candidateRank, candidateSlug
		}
	}

	if bestCandidate == nil {
		if outranking == 0 {
			return skip(upgradeSkipNoneOutrank)
		}
		return skip(fmt.Sprintf("%s (%s)", upgradeSkipGateRefused, strings.Join(refusals, "; ")))
	}

	// Apply the upgrade. ApplyMetadataCandidateWithOptions handles:
	// - change history recording
	// - metadata field application
	// - provenance tagging (metadata:source:*, metadata:language:*), so the
	//   book's source tag becomes the candidate's and the next run does not
	//   visit it again
	// - ISBN enrichment queueing
	//
	// It does NOT queue any file I/O. This comment said it queued "cover embed,
	// tag write, rename" until 2026-09-13; it never has. The file sequel
	// (FinishApplyFileWork: cover download, rename, tags) is queued by each
	// caller that wants one, and this op queues none, so no rename follows
	// this write. That is why there is no RenamePreflight here, unlike the
	// apply paths that do queue a rename (batch_apply_one.go, the single-book
	// apply, batch-apply-candidates): the preflight predicts whether the
	// post-apply rename fails, and a refusal here would guard a rename that
	// never runs. The files keep their names until a later write-back or
	// organize, which runs its own rename. If this op ever queues file work,
	// add the preflight, gated the same way, before this call.
	//
	// Replace, not fill. Until 2026-09-27 this apply was fill-only (owner
	// decision A3#3: every automatic apply only fills empty fields), which
	// made the job a no-op for its own purpose: a book whose description,
	// publisher and so on came from Open Library kept them, and Audible's
	// better values were dropped. The owner changed that on 2026-09-27 for
	// rank upgrades only: a candidate from a strictly higher-ranked source
	// that passes the gate REPLACES filled fields. This is the same apply path
	// as the review page's bulk "Replace existing" (#3580: FillOnly false),
	// with these differences:
	//   - UnseenCandidate: nobody picked this candidate, so the apply keeps
	//     the automatic guards: a book marked "no match" is refused (before
	//     the apply and again under the write lock), and the match (review
	//     status, MetadataSource, source hash) is recorded only when the book
	//     ends up holding the candidate's title.
	//   - RankUpgrade instead of OwnerReplace: history is labeled with the
	//     rank upgrade, not an owner action, and a failed history write is
	//     still an error because it is the only way to revert the overwrite.
	// User-locked fields are kept on every apply, fill or replace:
	// guardedApply strips them before anything is written.
	opts := metafetch.ApplyOptions{
		FillOnly:        false,
		UnseenCandidate: true,
		RankUpgrade:     fmt.Sprintf("rank upgrade: %s -> %s", currentSourceSlug, bestSlug),
	}
	_, applyErr := s.Fetcher.ApplyMetadataCandidateWithOptions(bookID, *bestCandidate, nil, opts)
	if applyErr != nil {
		// The owner marked the book "no match" while this run searched it:
		// a skip, not a failure.
		if errors.Is(applyErr, metafetch.ErrMarkedNoMatch) {
			return skip(upgradeSkipMarkedDuringDone)
		}
		// The write committed but its change history did not: the overwrite
		// stands and cannot be undone. Counted as an error, and said so.
		if errors.Is(applyErr, metafetch.ErrApplyHistoryIncomplete) {
			return out, fmt.Errorf("applied %s -> %s but change history was not recorded (not undoable): %w",
				currentSourceSlug, bestSlug, applyErr)
		}
		return out, fmt.Errorf("apply failed: %w", applyErr)
	}

	upgradeLog.Info("upgraded: id=%s from=%s to=%s score=%.3f title=%s",
		logger.SanitizeLogValue(bookID), currentSourceSlug, logger.SanitizeLogValue(bestSlug),
		bestCandidate.Score, logger.SanitizeLogValue(bestCandidate.Title))
	out.Upgraded = true
	out.From, out.To = currentSourceSlug, bestSlug
	out.CandidateTitle, out.Score = bestCandidate.Title, bestCandidate.Score
	return out, nil
}

// upgradeOutcomeLine renders one book's upgrade outcome for the op log and
// returns its operations.ProgressReporter level.
func upgradeOutcomeLine(bookID, currentSourceSlug string, out upgradeOutcome, err error) (level, msg string) {
	book := fmt.Sprintf("%q", logger.SanitizeLogValue(bookID))
	if out.BookTitle != "" {
		book = fmt.Sprintf("%q", logger.SanitizeLogValue(out.BookTitle))
	}
	switch {
	case errors.Is(err, errOwnerManualOnly):
		return "info", fmt.Sprintf("skipped: %s — %s", book, errOwnerManualOnly.Error())
	case err != nil:
		return "warn", fmt.Sprintf("error: %s — %s", book, logger.SanitizeLogValue(err.Error()))
	case out.Upgraded:
		return "info", fmt.Sprintf("upgraded: %s — %s → %s, now %q (score %.2f)",
			book, logger.SanitizeLogValue(out.From), logger.SanitizeLogValue(out.To),
			logger.SanitizeLogValue(out.CandidateTitle), out.Score)
	default:
		return "info", fmt.Sprintf("skipped: %s (source %s) — %s",
			book, logger.SanitizeLogValue(currentSourceSlug), logger.SanitizeLogValue(out.Reason))
	}
}
