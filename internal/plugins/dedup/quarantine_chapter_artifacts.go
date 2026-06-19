// file: internal/plugins/dedup/quarantine_chapter_artifacts.go
// version: 1.0.0
// guid: 1d7a4f92-3c60-4e85-9b21-6a5e8c0d3f47
// last-edited: 2026-06-19

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
//   - that single file's duration is positive and below MaxDurationSec
//     (a genuine short story is not also title-colliding with 5+ books).
//
// Action is SOFT-delete (MarkedForDeletion) — recoverable, not a hard delete.
// Dry-run by default: reports what it WOULD quarantine and writes nothing.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type quarantineChapterParams struct {
	Apply              bool `json:"apply"`
	MinTitleCollisions int  `json:"min_title_collisions,omitempty"` // default 5
	MaxDurationSec     int  `json:"max_duration_sec,omitempty"`     // default 1200 (20 min)
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
	params := quarantineChapterParams{MinTitleCollisions: 5, MaxDurationSec: 1200}
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
	reporter.Logger().Info("quarantine-chapter-artifacts start",
		"apply", params.Apply, "min_collisions", params.MinTitleCollisions, "max_duration_sec", params.MaxDurationSec)

	_ = reporter.UpdateProgress(0, 3, "Loading library…")
	books, err := p.store.GetAllBooks(0, 0)
	if err != nil {
		return fmt.Errorf("get all books: %w", err)
	}

	// Pass 1: count books per normalized title (skip empty titles / already soft-deleted).
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Counting title collisions over %d books…", len(books)))
	titleCount := make(map[string]int, len(books))
	for i := range books {
		b := &books[i]
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" {
			continue
		}
		titleCount[norm]++
	}

	// Pass 2: identify chapter artifacts among the colliding-title books.
	_ = reporter.UpdateProgress(2, 3, "Identifying chapter artifacts…")
	type artifact struct {
		ID    string
		Title string
	}
	var artifacts []artifact
	sampleByTitle := make(map[string]int)
	examined := 0
	for i := range books {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		b := &books[i]
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" || titleCount[norm] < params.MinTitleCollisions {
			continue
		}
		examined++
		files, ferr := p.store.GetBookFiles(b.ID)
		if ferr != nil || len(files) != 1 {
			continue // multi-file audiobook (or unreadable) — not a single-segment artifact
		}
		dur := files[0].AcoustIDFingerprintDurationSec
		if dur <= 0 {
			dur = float64(files[0].Duration)
		}
		if dur <= 0 || dur >= float64(params.MaxDurationSec) {
			continue // unscanned or long — leave it alone (conservative)
		}
		artifacts = append(artifacts, artifact{ID: b.ID, Title: b.Title})
		sampleByTitle[b.Title]++
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

	quarantined := 0
	if params.Apply {
		now := time.Now()
		for _, a := range artifacts {
			if reporter.IsCanceled() {
				return context.Canceled
			}
			full, gerr := p.store.GetBookByID(a.ID)
			if gerr != nil || full == nil {
				continue
			}
			marked := true
			full.MarkedForDeletion = &marked
			full.MarkedForDeletionAt = &now
			if _, uerr := p.store.UpdateBook(full.ID, full); uerr != nil {
				reporter.Logger().Error("quarantine soft-delete error", "book_id", a.ID, "error", uerr)
				continue
			}
			quarantined++
		}
	}

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
