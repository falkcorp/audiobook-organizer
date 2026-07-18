// file: internal/metabatch/upgrade.go
// version: 1.3.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-07-18
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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// MetadataUpgradeService finds books with low-quality metadata
// sources and attempts to upgrade them to richer sources.
type MetadataUpgradeService struct {
	DB      database.Store
	Fetcher *metafetch.Service
}

// NewMetadataUpgradeService creates an upgrade service. The fetcher
// provides the search + apply pipeline; the db provides the tag
// lookup for finding eligible books.
func NewMetadataUpgradeService(db database.Store, fetcher *metafetch.Service) *MetadataUpgradeService {
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
// Set conservatively high to avoid upgrading to a worse match.
const MinUpgradeConfidence = 0.90

// MinUpgradeConfidenceWithTranscription relaxes the gate when the candidate
// independently matches the book's audio-derived title/author.
const MinUpgradeConfidenceWithTranscription = 0.85

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
			if ctx.Err() != nil {
				return result, ctx.Err()
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
// The title must match exactly after normalization. The author, if present and
// longer than 3 characters, must appear as a substring of the candidate's
// author (case-insensitive). A title-only match is sufficient when no usable
// transcribed author is available.
func transcriptionConfirmsCandidate(book *database.Book, c *metafetch.MetadataCandidate) bool {
	if book.TranscribedTitle == nil || *book.TranscribedTitle == "" {
		return false
	}
	transcribedTitle := util.NormalizeTitle(*book.TranscribedTitle)
	if util.NormalizeTitle(c.Title) != transcribedTitle {
		return false
	}
	// Title matches. Now check author if one is available.
	if book.TranscribedAuthor == nil || len(*book.TranscribedAuthor) <= 3 {
		return true // no usable author — title alone confirms
	}
	transcribedAuthor := util.NormalizeAuthor(*book.TranscribedAuthor)
	return strings.Contains(util.NormalizeAuthor(c.Author), transcribedAuthor)
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

		// A candidate that independently matches the book's audio-derived
		// title/author is corroborated and gets a relaxed score gate.
		transcriptionConfirms := transcriptionConfirmsCandidate(book, c)

		// Hard gate: when the book has an audio-derived (transcribed) title but
		// this candidate does NOT match it (title+author), never auto-upgrade —
		// a score-only pass would let a same-author, wrong-title record win
		// ("matches the author but not the actual book"). Defer to manual review.
		hasTranscribedTitle := book.TranscribedTitle != nil && *book.TranscribedTitle != ""
		if hasTranscribedTitle && !transcriptionConfirms {
			slog.Debug("upgrade skip: transcribed title present but candidate not confirmed",
				"id", bookID, "candidate_title", c.Title, "score", c.Score)
			continue
		}

		gate := MinUpgradeConfidence
		if transcriptionConfirms {
			gate = MinUpgradeConfidenceWithTranscription
		}
		slog.Debug("upgrade gate", "id", bookID, "score", c.Score, "gate", gate, "transcription_confirms", transcriptionConfirms)
		if c.Score < gate {
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
