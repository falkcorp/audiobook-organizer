// file: internal/plugins/maintenance/author_strip_merge.go
// version: 1.0.0
// guid: dbd16a1f-eada-4c33-b5c4-6a61ce342396
// last-edited: 2026-09-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-strip-merge ---
//
// 🔴 WHY THIS EXISTS. Measured on the live library 2026-09-03: 2,793 of 19,972
// author rows (14.0%) begin with a digit. They are not people — they are the
// chapter file's own numbering, lifted out of a filename or an ID3 artist tag:
// "001_Celestia", "Track 01", "000m_00s__056m_16s_43h", "001 of 301".
//
// PR #3062 closed the iTunes creation path that mints them. This op repairs the
// rows that path already created; nothing re-normalizes an author name after
// creation, so the forward fix does not touch them.
//
// Three outcomes, and the split between them is the whole design:
//
//   - JUNK -> deleted. dedup.CleanAuthorNameForCreation rejects the name
//     outright. The books keep existing and simply lose a bogus credit; a
//     future scan can recreate the author correctly.
//   - STRIPPED and the residue names an EXISTING author -> merged into it.
//     This is the 603-row "001-147 Kevin J Anderson" cluster, where a real
//     person is wrapped in numbering.
//   - STRIPPED but no existing author has that name -> LEFT ALONE.
//
// That last case is deliberate and must not be "finished" by renaming the row.
// "001_Head of the Dragon" strips to "Head of the Dragon", which is a book
// title, not a person. Renaming would launder an obviously-corrupt row into a
// plausible one and take it out of reach of every future audit — the same
// reasoning author_conjunction_repair.go gives for leaving "and Thanks for All
// the Fish" alone.

// authorStripMergeSampleLimit bounds how many per-row decisions are surfaced in
// the report, so a reviewer can eyeball the plan without the report becoming the
// size of the change.
const authorStripMergeSampleLimit = 60

type authorStripMergeParams struct {
	// Apply, if true, actually merges and deletes. Default false (report only).
	//
	// 🔴 THIS DELETES AUTHOR ROWS ON A PRODUCTION LIBRARY, so the default must
	// be the harmless one. Mirrors maintenance.purge-empty-authors.
	Apply bool `json:"apply"`

	// Limit caps how many rows are mutated in one run (0 = no cap), so a first
	// apply can be run small and inspected rather than all-or-nothing.
	Limit int `json:"limit"`

	// DeleteJunk, when true (the DEFAULT), deletes rows the name predicate
	// rejects outright. Set false to perform ONLY the merges — useful for a
	// first apply, where consolidating the unambiguous cases is lower risk than
	// deleting.
	DeleteJunk *bool `json:"delete_junk,omitempty"`
}

// deleteJunk resolves the tri-state pointer to its default of TRUE.
func (p authorStripMergeParams) deleteJunk() bool {
	return p.DeleteJunk == nil || *p.DeleteJunk
}

type authorStripMergeReport struct {
	TotalAuthors int
	Junk         int
	Mergeable    int
	// Ambiguous are rows whose stripped name matches MORE THAN ONE existing
	// author. Reported rather than merged: a name index resolves to one row and
	// silently hides the duplicates, so picking one here would be a guess.
	Ambiguous int
	// StrippedNoTarget are rows that carry numbering but whose residue names no
	// existing author. Left alone on purpose (see the file comment).
	StrippedNoTarget int
	// TargetIsJunk are rows whose stripped name matches an existing author that
	// is ITSELF junk. Merging those would consolidate junk into junk and report
	// it as a success.
	//
	// Measured 0 on the live library, and that is expected rather than lucky:
	// a row whose residue is junk is already rejected by the SOURCE check
	// above, so "00 Prologue" is deleted as junk and never reaches a merge.
	// The branch is kept as cheap insurance in case the two predicates ever
	// diverge — it is NOT what makes the merge safe, and should not be cited
	// as though it were.
	TargetIsJunk int

	// OutOfScope are rows rejected by the name predicate for a reason that is
	// NOT numbering — publisher and copyright shrapnel. Counted, never touched.
	OutOfScope   int
	Merged       int
	Deleted      int
	BooksTouched int
	Failed       int
	Sample       []string
}

func (r authorStripMergeReport) summary() string {
	return fmt.Sprintf(
		"authors=%d junk=%d mergeable=%d ambiguous=%d target-is-junk=%d stripped-no-target=%d out-of-scope=%d merged=%d deleted=%d books-touched=%d failed=%d",
		r.TotalAuthors, r.Junk, r.Mergeable, r.Ambiguous, r.TargetIsJunk,
		r.StrippedNoTarget, r.OutOfScope, r.Merged, r.Deleted, r.BooksTouched, r.Failed)
}

func (p *Plugin) authorStripMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-strip-merge",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Strip author numbering and merge",
		Description: "Repairs author rows built out of chapter-file numbering ('001_Celestia', " +
			"'Track 01', '000m_00s__056m_16s_43h'). Strips the numbering; when the residue names " +
			"an existing author the row is MERGED into it ('001-147 Kevin J Anderson'), and rows " +
			"that carry no usable name are DELETED. Rows whose residue matches nothing are left " +
			"alone rather than renamed. Measured 2,793 of 19,972 authors on this library. " +
			"REPORT-ONLY BY DEFAULT: pass apply=true to write. Idempotent.",
		// ResumeDrop, not Requeue: this deletes rows, and a half-finished run
		// that silently resumes after a restart is harder to reason about than
		// one that stops and is re-triggered. Re-running is cheap and idempotent.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-strip-merge",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorStripMerge,
	}
}

// isPositionalScope reports whether a rejected name was rejected because of
// chapter/track numbering, as opposed to the publisher and copyright shrapnel
// that IsDirtyAuthorName also covers. Only the former is this op's business.
func isPositionalScope(name string) bool {
	n := dedup.NormalizeAuthorName(name)
	return dedup.IsPositionalArtifactName(n) || dedup.StripPositionalPrefix(n) != n
}

// authorStripPlan is one row's decision, computed before anything is written so
// that apply=false and apply=true evaluate exactly the same set.
type authorStripPlan struct {
	from   database.Author
	into   *database.Author // nil => delete
	reason string
}

func (p *Plugin) runAuthorStripMerge(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params authorStripMergeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	log := reporter.Logger()
	log.Info("author-strip-merge start",
		"apply", params.Apply, "delete_junk", params.deleteJunk(), "limit", params.Limit)

	_ = reporter.UpdateProgress(0, 2, "Listing authors…")
	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("list authors: %w", err)
	}

	// Resolve merge targets from an in-memory index rather than one
	// GetAuthorByName per candidate: 2,793 round trips to answer a question the
	// author list already contains.
	//
	// The index maps to a SLICE, not a single row. A name -> id index silently
	// resolves duplicates to one row, and this library has them (two "Unknown
	// Author" rows, ids 54845 and 54846). A merge target chosen that way is a
	// guess; ambiguous names are reported instead.
	byName := make(map[string][]database.Author, len(authors))
	for _, a := range authors {
		key := dedup.NormalizeAuthorName(a.Name)
		byName[key] = append(byName[key], a)
	}

	report := authorStripMergeReport{TotalAuthors: len(authors)}
	var plans []authorStripPlan
	tombstoneWarned := false

	_ = reporter.UpdateProgress(1, 2, "Classifying author names…")
	for _, a := range authors {
		cleaned, ok := dedup.CleanAuthorNameForCreation(a.Name)
		if !ok {
			// SCOPE GUARD. CleanAuthorNameForCreation also rejects the
			// publisher and copyright shrapnel IsDirtyAuthorName was built for
			// — 1,073 rows on this library, including "Penguin Books" and
			// "Alex A. Ryans - translator". Those are a different defect and
			// some of them name real people; deleting them here would be a
			// silent scope expansion far past the numbering this op exists for.
			if !isPositionalScope(a.Name) {
				report.OutOfScope++
				continue
			}
			report.Junk++
			if params.deleteJunk() {
				plans = append(plans, authorStripPlan{from: a, reason: "junk"})
			}
			continue
		}
		if cleaned == dedup.NormalizeAuthorName(a.Name) {
			continue // ordinary name, nothing to do
		}

		candidates := byName[dedup.NormalizeAuthorName(cleaned)]
		// Never treat the row itself as its own merge target.
		var targets []database.Author
		for _, c := range candidates {
			if c.ID != a.ID {
				targets = append(targets, c)
			}
		}
		switch {
		case len(targets) == 0:
			report.StrippedNoTarget++
		case len(targets) > 1:
			report.Ambiguous++
		default:
			// The target must pass the same judgement as the source. Otherwise
			// "00 Prologue" merges into the existing junk row "Prologue" and
			// the op reports a successful merge for work that consolidated one
			// junk row into another.
			if _, targetOK := dedup.CleanAuthorNameForCreation(targets[0].Name); !targetOK {
				report.TargetIsJunk++
				continue
			}
			report.Mergeable++
			t := targets[0]
			plans = append(plans, authorStripPlan{from: a, into: &t, reason: "merge"})
		}
	}

	// Deterministic order so a limited run is reproducible and a dry run
	// describes the same prefix the apply will take.
	sort.Slice(plans, func(i, j int) bool { return plans[i].from.ID < plans[j].from.ID })
	if params.Limit > 0 && len(plans) > params.Limit {
		plans = plans[:params.Limit]
	}

	for i, pl := range plans {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i%25 == 0 {
			_ = reporter.UpdateProgress(i, len(plans), "Applying author repairs…")
		}
		if len(report.Sample) < authorStripMergeSampleLimit {
			if pl.into != nil {
				report.Sample = append(report.Sample,
					fmt.Sprintf("merge %q -> %q", pl.from.Name, pl.into.Name))
			} else {
				report.Sample = append(report.Sample, fmt.Sprintf("delete %q", pl.from.Name))
			}
		}
		if !params.Apply {
			continue
		}

		if pl.into != nil {
			// mergeAuthorInto rewrites the book_authors junction, moves the
			// denormalized book.AuthorID when it named the row being removed,
			// and only then deletes. Reused rather than reimplemented: the
			// AuthorID step is exactly the one whose absence stranded ~212
			// authors' books in the 2026-08-24 dangling-AuthorID incident.
			n, err := p.mergeAuthorInto(ctx, pl.from, *pl.into, false, log)
			report.BooksTouched += n
			if err != nil {
				report.Failed++
				log.Warn("author-strip-merge: merge failed",
					"from_id", pl.from.ID, "from", pl.from.Name, "into", pl.into.Name, "err", err)
				continue
			}
			// Redirect any reference that still names the old id. Without this
			// a stale AuthorID resolves to nothing; with it, reads self-heal.
			// Resolved through a capability assertion rather than widened onto
			// OpsStore: the tombstone is a nice-to-have on top of a merge that
			// has already succeeded, and one extra method on a store interface
			// is a cost the whole codebase pays.
			if ts, tsOK := any(store).(interface {
				CreateAuthorTombstone(oldID, canonicalID int) error
			}); tsOK {
				if err := ts.CreateAuthorTombstone(pl.from.ID, pl.into.ID); err != nil {
					log.Warn("author-strip-merge: tombstone write failed; merge stands but stale refs will not self-heal",
						"from_id", pl.from.ID, "into_id", pl.into.ID, "err", err)
				}
			} else if !tombstoneWarned {
				// Say so once. A type assertion that never matches because a
				// decorator in the chain does not forward the method looks
				// exactly like one that had nothing to do, and the merges would
				// still be reported as complete.
				tombstoneWarned = true
				log.Warn("author-strip-merge: store does not expose CreateAuthorTombstone; merges will not leave a redirect for stale author ids")
			}
			report.Merged++
			continue
		}

		n, err := p.unlinkAndDeleteAuthor(ctx, pl.from, log)
		report.BooksTouched += n
		if err != nil {
			report.Failed++
			log.Warn("author-strip-merge: delete failed",
				"author_id", pl.from.ID, "name", pl.from.Name, "err", err)
			continue
		}
		report.Deleted++
	}

	_ = reporter.UpdateProgress(len(plans), len(plans), report.summary())
	log.Info("author-strip-merge done", "summary", report.summary())
	for _, s := range report.Sample {
		log.Info("author-strip-merge plan", "change", s)
	}
	if !params.Apply {
		log.Info("author-strip-merge: REPORT ONLY — pass apply=true to write these changes")
	}
	return nil
}

// unlinkAndDeleteAuthor removes a junk author from every book that credits it
// and then deletes the row, returning the number of books rewritten.
//
// This is mergeAuthorInto's shape without a destination, and it exists because
// store.DeleteAuthor alone is NOT safe for an author that has books: it sweeps
// the book_authors junction but leaves the denormalized book.AuthorID pointing
// at the row it just deleted. That single missing step is the whole mechanism
// of the 2026-08-24 incident, where two entity handlers stranded ~212 authors'
// books behind ids that no longer resolve.
//
// A book losing its only author is the intended outcome, not a failure: the
// credit being removed was never a person, and a book with no author is honest
// where one named "Track 01" is a repair job. A future scan recreates it
// correctly, now that the creation path is gated.
func (p *Plugin) unlinkAndDeleteAuthor(ctx context.Context, from database.Author, log *slog.Logger) (int, error) {
	store := p.deps.OpsStore()

	books, err := store.GetBooksByAuthorIDWithRoleCore(from.ID)
	if err != nil {
		return 0, fmt.Errorf("get books for author %d: %w", from.ID, err)
	}

	unlinked := 0
	for _, book := range books {
		if ctx.Err() != nil {
			return unlinked, ctx.Err()
		}
		bookAuthors, err := store.GetBookAuthors(book.ID)
		if err != nil {
			// Do NOT fall through to DeleteAuthor after this: dropping the row
			// while a book still credits it is precisely the orphaning this
			// function exists to avoid.
			return unlinked, fmt.Errorf("get book authors for %s: %w", book.ID, err)
		}

		var remaining []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID == from.ID {
				continue
			}
			ba.Position = len(remaining)
			remaining = append(remaining, ba)
		}
		if err := store.SetBookAuthors(book.ID, remaining); err != nil {
			return unlinked, fmt.Errorf("set book authors for %s: %w", book.ID, err)
		}

		// Move the denormalized primary off the row being deleted: promote the
		// first surviving credit, or clear it when nothing is left. Hydrate the
		// full row rather than writing the BookCore projection, whose heavy
		// fields are nil and whose guard-preserved Author would still name the
		// deleted row (STOREFID W5d-1).
		if book.AuthorID != nil && *book.AuthorID == from.ID {
			if full, err := store.GetBookByID(book.ID); err == nil && full != nil {
				full.AuthorID = nil
				full.Author = nil
				if len(remaining) > 0 {
					if promoted, err := store.GetAuthorByID(remaining[0].AuthorID); err == nil && promoted != nil {
						id := promoted.ID
						full.AuthorID = &id
						full.Author = promoted
					}
				}
				if _, err := store.UpdateBook(book.ID, full); err != nil {
					log.Warn("author-strip-merge: primary author rewrite failed",
						"book_id", book.ID, "err", err)
				}
			} else {
				log.Warn("author-strip-merge: hydrate failed, primary author left stale",
					"book_id", book.ID, "err", err)
			}
		}
		unlinked++
	}

	if err := store.DeleteAuthor(from.ID); err != nil {
		return unlinked, fmt.Errorf("delete author %d: %w", from.ID, err)
	}
	return unlinked, nil
}
