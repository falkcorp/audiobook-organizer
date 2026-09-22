// file: internal/dedup/collectors_chapters.go
// version: 1.0.0
// guid: 6b4e1f78-25ac-4d93-9e07-3c8f1ab60d24
// last-edited: 2026-09-22

package dedup

import (
	"context"
	"fmt"
	"math"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// ChapterStore is the slice of the store this collector needs: the per-book
// chapter table, keyed "chapters:<bookID>".
type ChapterStore interface {
	GetChaptersForBook(bookID string) ([]database.Chapter, error)
}

// ChapterCollectorConfig tunes the comparison.
type ChapterCollectorConfig struct {
	// BoundaryToleranceSec is how far two corresponding chapter boundaries may
	// differ and still count as the same table. 1.0s by default: the same
	// recording encoded twice lands well inside a second, while genuinely
	// different recordings of the same text diverge by far more.
	BoundaryToleranceSec float64
	// MinChapters is the fewest chapters a book must have for its table to be
	// evidence at all. Below this the structure is not distinctive -- a great
	// many books are a single file with two or three sections, and matching on
	// that would fire constantly.
	MinChapters int
}

// DefaultChapterCollectorConfig is the tuning used when a caller passes none.
func DefaultChapterCollectorConfig() ChapterCollectorConfig {
	return ChapterCollectorConfig{BoundaryToleranceSec: 1.0, MinChapters: 3}
}

// chapterConfidence maps the worst boundary deviation onto the configured
// 0.85–0.93 range: an exact table scores 0.93, and confidence falls linearly to
// 0.85 as the worst boundary approaches the tolerance.
func chapterConfidence(worstDevSec, toleranceSec float64) float64 {
	const lo, hi = 0.85, 0.93
	if toleranceSec <= 0 {
		return hi
	}
	frac := worstDevSec / toleranceSec
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return hi - frac*(hi-lo)
}

// CollectChapterStructure emits a SigChapterStructure signal for each candidate
// whose chapter table matches bookID's.
//
// Chapter tables have been persisted per book for a long time and read by
// nobody in dedup: GetChaptersForBook had three callers before this one -- the
// chapters backfill, the ABS mapper, and scan-time persistence -- and there was
// no SignalKind for them at all. Per-chapter boundaries are close to a
// fingerprint of a book's EDITION, because they come from the recording rather
// than the text, so two different recordings of the same work do not share
// them.
//
// A match requires the SAME chapter count and EVERY boundary inside the
// tolerance. Partial overlap earns nothing: a table that agrees on some
// boundaries and not others is a different edition, which is exactly the case
// this signal must not vote for.
//
// Absence is never an error. A book with no chapters, or too few to be
// distinctive, simply produces no signal -- the same contract the other
// collectors follow.
func CollectChapterStructure(
	ctx context.Context,
	store ChapterStore,
	bookID string,
	candidateIDs []string,
	cfg ChapterCollectorConfig,
) ([]unified.Signal, error) {
	if cfg.BoundaryToleranceSec <= 0 {
		cfg.BoundaryToleranceSec = DefaultChapterCollectorConfig().BoundaryToleranceSec
	}
	if cfg.MinChapters <= 0 {
		cfg.MinChapters = DefaultChapterCollectorConfig().MinChapters
	}

	base, err := store.GetChaptersForBook(bookID)
	if err != nil {
		return nil, fmt.Errorf("CollectChapterStructure %s: %w", bookID, err)
	}
	if len(base) < cfg.MinChapters {
		// Not enough structure to be evidence. Not an error.
		return nil, nil
	}

	var out []unified.Signal
	for _, candID := range candidateIDs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if candID == "" || candID == bookID {
			continue
		}
		cand, cErr := store.GetChaptersForBook(candID)
		if cErr != nil {
			// One unreadable candidate must not sink the whole comparison;
			// the others are still valid evidence.
			continue
		}
		worst, ok := compareChapterTables(base, cand, cfg.BoundaryToleranceSec)
		if !ok {
			continue
		}
		out = append(out, unified.Signal{
			Kind:       unified.SigChapterStructure,
			Raw:        worst,
			Confidence: chapterConfidence(worst, cfg.BoundaryToleranceSec),
			Evidence: fmt.Sprintf(
				"chapter table matches: %d chapters, worst boundary delta %.3fs: book %s ↔ %s",
				len(base), worst, bookID, candID,
			),
		})
	}
	return out, nil
}

// compareChapterTables reports the worst boundary deviation between two tables,
// and whether they match at all. Both the count and every boundary must agree
// within tolerance; the first boundary outside it ends the comparison.
func compareChapterTables(a, b []database.Chapter, toleranceSec float64) (worstDevSec float64, matched bool) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, false
	}
	for i := range a {
		// Both ends are checked. Comparing only starts would let two tables
		// with an identical run of starts but a different final end read as a
		// match, and the last end is the book's own duration.
		for _, d := range [2]float64{
			math.Abs(a[i].StartSec - b[i].StartSec),
			math.Abs(a[i].EndSec - b[i].EndSec),
		} {
			if d > toleranceSec {
				return 0, false
			}
			if d > worstDevSec {
				worstDevSec = d
			}
		}
	}
	return worstDevSec, true
}
