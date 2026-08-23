// file: internal/plugins/maintenance/author_purge_empty.go
// version: 1.1.0
// guid: 6a2f9c31-84d7-4e05-b1a3-7f92c60d8e54
// last-edited: 2026-08-23

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- purge-empty-authors ---
//
// 🔴 WHY THIS EXISTS. Measured on the live library 2026-08-17: 4,975 of 12,854
// authors (38.7%) are attached to ZERO books. They are not people — they are track
// and chapter titles that were parsed into the author field by an importer:
// "- Edgedancer", "(Long Earth 01) The Long Earth", "04 - Heir to the Jedi",
// "- The Emperor's Soul". Every one of them is a row in the Authors tab a user has
// to scroll past, and a candidate every author-dedup pass has to compare against.
//
// No existing op covers this. maintenance.author-dedup-scan finds duplicates,
// author-split-scan splits composites, author-conjunction-repair fixes "& Foo"
// fragments, resolve-production-authors handles production credits — all of them
// operate on authors that HAVE books.

// emptyAuthorSampleLimit bounds how many names are surfaced in the report, so a
// reviewer can eyeball what would be deleted without the report becoming the size
// of the deletion itself.
const emptyAuthorSampleLimit = 50

// purgeEmptyAuthorsParams are the JSON parameters accepted by the op.
type purgeEmptyAuthorsParams struct {
	// Apply, if true, actually deletes. Default false (dry-run/report only).
	//
	// 🔴 DELETION IS IRREVERSIBLE AND THIS IS A PRODUCTION LIBRARY, so the default
	// must be the harmless one. Mirrors dedup.cleanup-orphan-author-embeddings.
	Apply bool `json:"apply"`

	// RequireZeroFiles, when true (the DEFAULT), refuses to delete an author that
	// has zero books but a NON-ZERO file count.
	//
	// 🔴 THIS IS THE SAFETY THAT MATTERS. Of the 4,975 zero-book authors, 4,153 also
	// have zero files and are unambiguous junk; the other 822 have files. A zero
	// book count with files present is more likely a BROKEN LINK — a book row that
	// lost its junction entry — than an empty author, and deleting the author makes
	// that damage permanent instead of repairable. Opt out only after deciding what
	// those 822 actually are.
	RequireZeroFiles *bool `json:"require_zero_files,omitempty"`

	// Limit caps how many authors are deleted in one run (0 = no cap). Present so a
	// first apply can be run small and inspected rather than all-or-nothing.
	Limit int `json:"limit"`
}

// requireZeroFiles resolves the tri-state pointer to its default of TRUE. A plain
// bool would default to false, i.e. to the DANGEROUS setting, which is exactly the
// wrong way round for a flag whose job is to prevent deleting repairable damage.
func (p purgeEmptyAuthorsParams) requireZeroFiles() bool {
	return p.RequireZeroFiles == nil || *p.RequireZeroFiles
}

// emptyAuthorReport is the outcome of one pass.
type emptyAuthorReport struct {
	TotalAuthors int
	ZeroBooks    int
	// ZeroBooksWithFiles are the zero-book authors HELD BACK by requireZeroFiles.
	// Reported separately rather than silently folded into the skipped count: they
	// are the population that needs a decision, and a number nobody sees does not
	// get one.
	ZeroBooksWithFiles int
	// HeldByRefs are authors the DISPLAY counter calls zero-book but that are
	// still referenced by a trashed book, a non-primary version, or a
	// junction-only co-author credit. Before this guard existed they were
	// deleted; surfacing the number is how an operator sees the divergence.
	HeldByRefs int
	Eligible   int
	Deleted    int
	Failed     int
	Sample     []string
}

func (r emptyAuthorReport) summary() string {
	return fmt.Sprintf(
		"authors=%d zero-book=%d held-back(still referenced)=%d held-back(has files)=%d eligible=%d deleted=%d failed=%d",
		r.TotalAuthors, r.ZeroBooks, r.HeldByRefs, r.ZeroBooksWithFiles, r.Eligible, r.Deleted, r.Failed)
}

func (p *Plugin) purgeEmptyAuthorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.purge-empty-authors",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Purge empty authors",
		Description: "Deletes author rows attached to zero books — importer junk such as track " +
			"and chapter titles parsed into the author field ('- Edgedancer', '04 - Heir to the " +
			"Jedi'). Measured 4,975 of 12,854 authors on this library. DRY-RUN BY DEFAULT: pass " +
			"apply=true to delete. By default refuses any author with a non-zero file count, " +
			"since zero books plus files present is more likely a broken junction link than an " +
			"empty author; pass require_zero_files=false to override. Idempotent.",
		// ResumeDrop, not Requeue: this is a deletion, and a half-finished run that
		// silently resumes after a restart is harder to reason about than one that
		// stops and is re-triggered deliberately. Re-running is cheap and idempotent.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-empty-authors",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runPurgeEmptyAuthors,
	}
}

func (p *Plugin) runPurgeEmptyAuthors(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params purgeEmptyAuthorsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	reporter.Logger().Info("purge-empty-authors start",
		"apply", params.Apply, "require_zero_files", params.requireZeroFiles(), "limit", params.Limit)

	_ = reporter.UpdateProgress(0, 3, "Listing authors…")
	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("list authors: %w", err)
	}

	_ = reporter.UpdateProgress(1, 3, "Counting books per author…")
	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		return fmt.Errorf("author book counts: %w", err)
	}
	// File counts are only consulted for the safety check, so a failure here must
	// not be silently treated as "zero files" — that would turn a missing signal
	// into permission to delete. Fail the op instead.
	var fileCounts map[int]int
	if params.requireZeroFiles() {
		fileCounts, err = store.GetAllAuthorFileCounts()
		if err != nil {
			return fmt.Errorf("author file counts (needed for the require_zero_files guard): %w", err)
		}
	}

	// THE DELETE GUARD USES THE UNFILTERED COUNT, NOT bookCounts ABOVE.
	//
	// GetAllAuthorBookCounts is a DISPLAY counter: it skips books that are
	// trashed (MarkedForDeletion) or are non-primary versions, and it never sees
	// co-authors credited only through the book_authors junction. Every one of
	// those books still holds the author_id, so purging on a zero from that
	// counter strands them behind an author row that no longer exists — and an
	// author's name lives only in that row, so it is not recoverable afterwards.
	// This is the author-side twin of the series damage recorded in
	// internal/database/series_bookref.go (6,893 phantom series IDs held by
	// 13,322 live books + 702 trashed, measured 2026-08-14). This op deletes in
	// BULK — 4,975 authors were eligible on the live library — so it is by far
	// the highest-volume way to reproduce that incident.
	//
	// bookCounts is still used, but only to pick the CANDIDATE set and to report
	// ZeroBooks. Deletion is gated on refCounts alone.
	//
	// Deliberately fatal when the store cannot answer: falling back to the
	// filtered counter IS the bug, and it would delete thousands of rows while
	// reporting success. Computed once for the whole library rather than per
	// author — it is also the only form in which "referenced by nothing" is
	// actually answerable.
	refCounter := database.AsAuthorBookRefStore(store)
	if refCounter == nil {
		return fmt.Errorf("purge-empty-authors: store cannot count unfiltered author references (got %T); "+
			"refusing to purge from a filtered count, which silently strands books whose author "+
			"is trashed, non-primary, or a junction-only co-author", store)
	}
	refCounts, err := refCounter.GetAllAuthorBookRefCounts()
	if err != nil {
		return fmt.Errorf("author reference counts (needed for the purge guard): %w", err)
	}

	// Built BEFORE the dry-run branch below, so `apply=false` and `apply=true`
	// report and delete exactly the same set. A guard applied only on the apply
	// path would make the dry run a lie.
	report := emptyAuthorReport{TotalAuthors: len(authors)}
	var eligible []int
	for _, a := range authors {
		if bookCounts[a.ID] != 0 {
			continue
		}
		report.ZeroBooks++
		// Zero by the display counter but still referenced by something real.
		// These are precisely the rows the old guard deleted.
		if refCounts[a.ID] != 0 {
			report.HeldByRefs++
			continue
		}
		if params.requireZeroFiles() && fileCounts[a.ID] != 0 {
			report.ZeroBooksWithFiles++
			continue
		}
		eligible = append(eligible, a.ID)
		if len(report.Sample) < emptyAuthorSampleLimit {
			report.Sample = append(report.Sample, a.Name)
		}
	}
	// Deterministic order so a limited run takes the same slice every time and two
	// runs of the same report can be diffed.
	sort.Ints(eligible)
	if params.Limit > 0 && len(eligible) > params.Limit {
		eligible = eligible[:params.Limit]
	}
	report.Eligible = len(eligible)

	if !params.Apply {
		msg := "DRY RUN (nothing deleted) — " + report.summary()
		reporter.Logger().Info("purge-empty-authors dry run",
			"eligible", report.Eligible, "held_by_refs", report.HeldByRefs,
			"sample", report.Sample)
		_ = reporter.UpdateProgress(3, 3, msg)
		return nil
	}

	// Deleted one at a time rather than in a bulk batch. DeleteAuthor already
	// removes the author row, its name index and its aliases, and its junction
	// sweep iterates the `book_author:` keyspace — which is EMPTY (nothing in the
	// repo writes it; the live data is the per-book `book_authors:<id>` array), so
	// the per-author cost is a seek that finds nothing, not a table scan. A bulk
	// path would duplicate that cleanup for no measured gain.
	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Deleting %d empty authors…", len(eligible)))
	prog := sdk.NewProgress(reporter, len(eligible))
	prog.Start(fmt.Sprintf("Deleting %d empty authors…", len(eligible)))
	for i, id := range eligible {
		if err := ctx.Err(); err != nil {
			// Report what was actually done before the cancel, rather than losing it.
			reporter.Logger().Info("purge-empty-authors cancelled",
				"deleted", report.Deleted, "remaining", len(eligible)-i)
			prog.Done("cancelled — " + report.summary())
			return err
		}
		if derr := store.DeleteAuthor(id); derr != nil {
			// One bad row must not abandon the other thousands; count it and move on.
			report.Failed++
			reporter.Logger().Warn("purge-empty-authors: delete failed", "author_id", id, "err", derr)
			continue
		}
		report.Deleted++
		if (i+1)%200 == 0 {
			prog.StepN(i+1, fmt.Sprintf("Deleted %d/%d…", report.Deleted, len(eligible)))
		}
	}

	msg := report.summary()
	reporter.Logger().Info("purge-empty-authors complete",
		"deleted", report.Deleted, "failed", report.Failed,
		"held_by_refs", report.HeldByRefs, "held_back", report.ZeroBooksWithFiles)
	prog.Done(msg)
	return nil
}
