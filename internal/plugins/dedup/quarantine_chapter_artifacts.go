// file: internal/plugins/dedup/quarantine_chapter_artifacts.go
// version: 1.3.0
// guid: 1d7a4f92-3c60-4e85-9b21-6a5e8c0d3f47
// last-edited: 2026-07-18

// Package dedup — op dedup.quarantine-chapter-artifacts.
//
// Drains the candidate explosion at its source: chapter/segment files that the
// scanner imported as STANDALONE books (idents, "Opening Credits", intro tracks)
// with generic titles that collide across hundreds of audiobooks. Those generic
// titles get cross-paired by the exact-title emitter into hundreds of thousands of
// bogus candidates (DEDUP-CANDIDATE-EXPLOSION-2026-06-18).
//
// A book is treated as a chapter artifact when ALL hold:
//   - its normalized title is shared by >= MinTitleCollisions OTHER books
//     (a real book title is not held by 5+ books; "Opening Credits" is held by many);
//   - it is a SINGLE-file book (not a multi-file audiobook);
//   - and either its single file is short (0 < duration < MaxDurationSec), OR it is
//     UNSCANNED (duration unknown — most idents/credits are) AND the title collides
//     with >= MinTitleCollisionsUnscanned books (a higher bar, since duration can't
//     confirm it's a segment). Long single files are never touched.
//
// Action is SOFT-delete (MarkedForDeletion) — recoverable, not a hard delete.
// Dry-run by default: reports what it WOULD quarantine and writes nothing.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type quarantineChapterParams struct {
	Apply              bool `json:"apply"`
	MinTitleCollisions int  `json:"min_title_collisions,omitempty"` // default 5 (scanned short books)
	MaxDurationSec     int  `json:"max_duration_sec,omitempty"`     // default 1200 (20 min)
	// MinTitleCollisionsUnscanned is the (higher) collision bar for UNSCANNED
	// single-file books (duration unknown). Most chapter idents/credits are
	// unscanned mp3 segments, so they MUST be reachable — but a higher bar avoids
	// quarantining a few unscanned copies of a genuine book. Default 10.
	MinTitleCollisionsUnscanned int `json:"min_title_collisions_unscanned,omitempty"`
}

func (p *Plugin) quarantineChapterArtifactsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.quarantine-chapter-artifacts",
		Plugin:      "dedup",
		DisplayName: "Quarantine chapter-file artifacts",
		Description: "Soft-deletes single short books whose generic title collides with many others " +
			"(idents/credits/intros the scanner imported as standalone books). Dry-run by default.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.quarantine-chapter-artifacts",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runQuarantineChapterArtifacts,
	}
}

func (p *Plugin) runQuarantineChapterArtifacts(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}
	params := quarantineChapterParams{MinTitleCollisions: 5, MaxDurationSec: 1200, MinTitleCollisionsUnscanned: 10}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	if params.MinTitleCollisions < 2 {
		params.MinTitleCollisions = 5
	}
	if params.MaxDurationSec <= 0 {
		params.MaxDurationSec = 1200
	}
	if params.MinTitleCollisionsUnscanned < params.MinTitleCollisions {
		params.MinTitleCollisionsUnscanned = params.MinTitleCollisions
		if params.MinTitleCollisionsUnscanned < 10 {
			params.MinTitleCollisionsUnscanned = 10
		}
	}
	reporter.Logger().Info("quarantine-chapter-artifacts start",
		"apply", params.Apply, "min_collisions", params.MinTitleCollisions, "max_duration_sec", params.MaxDurationSec)

	_ = reporter.UpdateProgress(0, 3, "Loading library…")
	books, err := p.store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("get all books: %w", err)
	}

	// Pass 1: count books per normalized title (skip empty titles / already soft-deleted).
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Counting title collisions over %d books…", len(books)))
	titleCount := make(map[string]int, len(books))
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" {
			continue
		}
		titleCount[norm]++
	}

	// Pass 2: identify chapter artifacts among the colliding-title books.
	//
	// Cheap in-memory pre-filter stays sequential (no DB I/O); only books that
	// clear the title-collision bar go on to the per-book GetBookFiles read,
	// which IS a per-item DB call and therefore runs through registry.RunItems
	// with a bounded worker pool (mandatory concurrency rule — see CLAUDE.md).
	_ = reporter.UpdateProgress(2, 3, "Identifying chapter artifacts…")
	type artifact struct {
		ID    string
		Title string
	}
	type candidate struct {
		ID    string
		Title string
		Norm  string
	}
	var candidates []candidate
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" || titleCount[norm] < params.MinTitleCollisions {
			continue
		}
		candidates = append(candidates, candidate{ID: b.ID, Title: b.Title, Norm: norm})
	}

	// Shared mutable state written from worker goroutines below — all guarded
	// by mu. titleCount is read-only in this phase (safe for concurrent reads,
	// no writers), so it needs no lock.
	var (
		mu            sync.Mutex
		artifacts     []artifact
		examined      int
		sampleByTitle = make(map[string]int)
	)
	if err := registry.RunItems(ctx, reporter, candidates, func(ctx context.Context, c candidate) error {
		mu.Lock()
		examined++
		mu.Unlock()

		files, ferr := p.store.GetBookFiles(c.ID)
		if ferr != nil || len(files) != 1 {
			return nil // multi-file audiobook (or unreadable) — not a single-segment artifact
		}
		dur := files[0].AcoustIDFingerprintDurationSec
		if dur <= 0 {
			dur = float64(files[0].Duration)
		}
		switch {
		case dur >= float64(params.MaxDurationSec):
			return nil // long — a real audiobook, never an artifact
		case dur > 0:
			// Scanned + short: the ≥ MinTitleCollisions gate above is enough.
		default:
			// Unscanned (duration unknown — most idents/credits are unscanned mp3
			// segments). Require the higher collision bar so we don't quarantine a
			// few unscanned copies of a genuine book.
			if titleCount[c.Norm] < params.MinTitleCollisionsUnscanned {
				return nil
			}
		}
		mu.Lock()
		artifacts = append(artifacts, artifact{ID: c.ID, Title: c.Title})
		sampleByTitle[c.Title]++
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, total int) string {
			return fmt.Sprintf("Identifying chapter artifacts %d/%d…", i+1, total)
		},
	}); err != nil {
		return err
	}

	// Build a short, human-readable sample of the offending titles.
	type tc struct {
		Title string
		N     int
	}
	samples := make([]tc, 0, len(sampleByTitle))
	for t, n := range sampleByTitle {
		samples = append(samples, tc{t, n})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].N > samples[j].N })
	sampleStr := ""
	for i := 0; i < len(samples) && i < 10; i++ {
		sampleStr += fmt.Sprintf("%q×%d ", samples[i].Title, samples[i].N)
	}

	var quarantinedCount int64
	if params.Apply {
		now := time.Now()
		// Each artifact is a distinct book ID (built once per book above), so
		// workers never touch the same row — safe to run concurrently without
		// any additional partitioning. The soft-delete itself keeps the
		// correct fetch-full-mutate pattern (GetBookByID then UpdateBook with
		// the full record), unchanged from the sequential version.
		if err := registry.RunItems(ctx, reporter, artifacts, func(ctx context.Context, a artifact) error {
			full, gerr := p.store.GetBookByID(a.ID)
			if gerr != nil || full == nil {
				return nil
			}
			marked := true
			full.MarkedForDeletion = &marked
			full.MarkedForDeletionAt = &now
			if _, uerr := p.store.UpdateBook(full.ID, full); uerr != nil {
				reporter.Logger().Error("quarantine soft-delete error", "book_id", a.ID, "error", uerr)
				return nil // one book's write error doesn't abort the whole op
			}
			atomic.AddInt64(&quarantinedCount, 1)
			return nil
		}, registry.RunItemsOptions{
			Concurrency: runtime.NumCPU(),
			Label: func(i, total int) string {
				return fmt.Sprintf("Quarantining %d/%d chapter artifacts…", i+1, total)
			},
		}); err != nil {
			return err
		}
	}
	quarantined := int(quarantinedCount)

	summary := fmt.Sprintf("examined=%d artifacts=%d quarantined=%d (apply=%v) top: %s",
		examined, len(artifacts), quarantined, params.Apply, sampleStr)
	reporter.Logger().Info("quarantine-chapter-artifacts complete", "summary", summary)
	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
			"Dry-run — %d chapter-artifact book(s) would be soft-deleted. Pass apply=true to quarantine. Top: %s",
			len(artifacts), sampleStr))
	} else {
		_ = reporter.UpdateProgress(3, 3, fmt.Sprintf("Soft-deleted %d chapter-artifact book(s). %s", quarantined, summary))
	}
	return nil
}
