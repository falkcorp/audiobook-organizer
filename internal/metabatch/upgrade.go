// file: internal/metabatch/upgrade.go
// version: 1.7.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-09-13
//
// Background job that upgrades metadata from lower-quality sources
// (primarily Google Books) to richer ones (Hardcover, Audible/Audnexus)
// when a high-confidence match is available. Backlog 7.4.
//
// The upgrade targets books tagged with `metadata:source:google_books`
// (or any other source considered "lower quality"). For each candidate,
// the job re-runs the full metadata search pipeline against ALL
// configured sources. If the best result comes from a source OTHER
// than the current one and its confidence score exceeds a threshold,
// the upgrade is applied automatically.
//
// The job leverages the metadata fetch cache (PR #250) so re-fetches
// for already-queried sources are free. Only sources that returned
// empty on the initial fetch will actually hit the API.

package metabatch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// MetadataUpgradeService finds books with low-quality metadata
// sources and attempts to upgrade them to richer sources.
type MetadataUpgradeService struct {
	DB      Store
	Fetcher *metafetch.Service
}

// NewMetadataUpgradeService creates an upgrade service. The fetcher
// provides the search + apply pipeline; the db provides the tag
// lookup for finding eligible books.
func NewMetadataUpgradeService(db Store, fetcher *metafetch.Service) *MetadataUpgradeService {
	return &MetadataUpgradeService{DB: db, Fetcher: fetcher}
}

// LowQualitySources lists the metadata sources that are considered
// "lower quality" — books whose metadata came from these sources
// are candidates for upgrade. The tag namespace is
// metadata:source:<slug> (all lowercase, spaces → underscores).
var LowQualitySources = []string{
	"google_books",
	"wikipedia",
}

// UpgradeResult summarizes what the upgrade job did.
type UpgradeResult struct {
	Checked  int `json:"checked"`
	Upgraded int `json:"upgraded"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
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

	result := &UpgradeResult{}

	for _, sourceSlug := range LowQualitySources {
		tag := "metadata:source:" + sourceSlug
		bookIDs, err := s.DB.GetBooksByTag(tag)
		if err != nil {
			slog.Warn("metadata-upgrade GetBooksByTag", "tag", tag, "err", err)
			continue
		}
		slog.Info("metadata-upgrade found books tagged", "count", len(bookIDs), "tag", tag)

		for _, bookID := range bookIDs {
			// Per-book stand-down beat: tryUpgradeBook is a network call, so the
			// scan hold must be renewed per book, not per 25-book progress stamp.
			if err := opsregistry.ScanStandDownCheckpoint(ctx); err != nil {
				return result, err
			}
			if result.Checked >= limit {
				break
			}
			result.Checked++

			upgraded, upgradeErr := s.tryUpgradeBook(ctx, bookID, sourceSlug)
			if upgradeErr != nil {
				slog.Warn("metadata-upgrade book", "id", bookID, "err", upgradeErr)
				result.Errors++
				continue
			}
			if upgraded {
				result.Upgraded++
			} else {
				result.Skipped++
			}

			if progress != nil && (result.Checked%25 == 0 || result.Checked >= limit) {
				_ = progress.UpdateProgress(result.Checked, limit, fmt.Sprintf(
					"metadata upgrade: %d/%d books checked (%d upgraded, %d skipped, %d errors)",
					result.Checked, limit, result.Upgraded, result.Skipped, result.Errors))
			}
		}
	}

	return result, nil
}

// transcriptionConfirmsCandidate returns true when the candidate's title/author
// independently matches the book's audio-derived (transcribed) title/author.
// The rule lives in internal/applygate so every bulk-apply path shares it.
func transcriptionConfirmsCandidate(book *database.Book, c *metafetch.MetadataCandidate) bool {
	return applygate.TranscriptionConfirms(book, c)
}

// tryUpgradeBook re-searches metadata for a single book and
// applies the best non-current-source result if it's confident
// enough. Returns true if an upgrade was applied.
func (s *MetadataUpgradeService) tryUpgradeBook(ctx context.Context, bookID, currentSourceSlug string) (bool, error) {
	book, err := s.DB.GetBookByID(bookID)
	if err != nil || book == nil {
		return false, fmt.Errorf("book not found: %s", bookID)
	}

	// Run the full search pipeline — this goes through the
	// metadata fetch cache, so sources that were already queried
	// (and returned non-empty) won't hit the API again. Sources
	// that returned empty last time WILL be retried because the
	// cache only stores non-empty results.
	resp, err := s.Fetcher.SearchMetadataForBook(bookID, book.Title)
	if err != nil {
		return false, fmt.Errorf("search failed: %w", err)
	}
	if resp == nil || len(resp.Results) == 0 {
		return false, nil // no results at all
	}

	// Find the best candidate from a source OTHER than the current one.
	var bestCandidate *metafetch.MetadataCandidate
	for i := range resp.Results {
		c := &resp.Results[i]
		candidateSlug := strings.ToLower(strings.ReplaceAll(c.Source, " ", "_"))
		if strings.HasPrefix(candidateSlug, "audnexus") {
			candidateSlug = "audnexus"
		}
		// Skip candidates from the same source we're trying to upgrade FROM.
		if candidateSlug == currentSourceSlug {
			continue
		}

		// The shared bulk-apply gate (internal/applygate): the transcription
		// hard gate (a book with a transcribed title never takes a candidate
		// that does not match it), the 0.90 / 0.85-with-audio score floor, and
		// the sequence-number guard, so "Big Cats 3" can never upgrade
		// "Big Cats 1" however well it scores. There is no cache-identity leg
		// here: the candidates were searched a moment ago from the book's
		// current fields, so nothing can have drifted.
		v := applygate.Evaluate(book, c, nil)
		slog.Debug("upgrade gate", "id", bookID, "score", c.Score, "gate", v.ScoreFloor,
			"transcription_confirms", v.AudioConfirmed, "allowed", v.Allowed, "reason", v.Reason, "detail", v.Detail)
		if !v.Allowed {
			continue
		}
		if bestCandidate == nil || c.Score > bestCandidate.Score {
			bestCandidate = c
		}
	}

	if bestCandidate == nil {
		return false, nil // no better source found above threshold
	}

	// Apply the upgrade. ApplyMetadataCandidate handles:
	// - change history recording
	// - metadata field application
	// - provenance tagging (metadata:source:*, metadata:language:*)
	// - cache invalidation
	// - ISBN enrichment queueing
	// - file I/O queueing (cover embed, tag write, rename)
	_, applyErr := s.Fetcher.ApplyMetadataCandidate(bookID, *bestCandidate, nil)
	if applyErr != nil {
		return false, fmt.Errorf("apply failed: %w", applyErr)
	}

	slog.Info("metadata-upgrade upgraded", "id", bookID, "from", currentSourceSlug, "to", bestCandidate.Source, "score", bestCandidate.Score, "title", bestCandidate.Title)
	return true, nil
}
