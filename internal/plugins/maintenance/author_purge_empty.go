// file: internal/plugins/maintenance/author_purge_empty.go
// version: 1.5.2
// guid: 6a2f9c31-84d7-4e05-b1a3-7f92c60d8e54
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
	// HeldLinkedDuringRun are eligible authors the apply's per-item re-check
	// found linked to a book at delete time, although the whole-library count
	// taken at the start of the run said zero. A running library.scan (or an
	// import resolving an existing author by name) linked them in between.
	// Deleting them would strand that book on a deleted author id.
	HeldLinkedDuringRun int
	// HeldLookupFailed are eligible authors whose per-item re-check could not be
	// answered (the point lookup returned an error). Held rather than deleted:
	// a missing signal is not permission. Kept apart from HeldLinkedDuringRun
	// because "cannot tell" and "found a link" are different facts.
	HeldLookupFailed int
	// JournalFailed are eligible authors NOT deleted because the undo-ledger row
	// recording them could not be written first. The author's name lives only in
	// its row, so a delete with no ledger row is unrecoverable by construction.
	JournalFailed int
	// Sample names up to emptyAuthorSampleLimit authors from the ELIGIBLE
	// (would-be-deleted) population.
	Sample []string
	// HeldBackSample names up to emptyAuthorSampleLimit authors from the
	// ZeroBooksWithFiles population — the one require_zero_files holds back and
	// that still needs a human decision before that flag is ever flipped. A
	// bare count gives a reviewer nothing to look at; this gives them who, and
	// how many files each one holds. Report-only: nothing reads it to decide a
	// deletion.
	//
	// NO SEPARATE book_authors BACK-REFERENCE CHECK IS NEEDED HERE. An author
	// only reaches this population after the refCounts guard, and
	// database.AuthorRefCounts already counts junction-only co-author credits
	// (the per-book book_authors:<bookID> arrays) in every book state. So every
	// entry is, by construction, referenced by no book_authors array anywhere.
	HeldBackSample []heldBackAuthor
	// HeldByRefsSample names up to emptyAuthorSampleLimit authors from the
	// HeldByRefs population. This is where the 822 "zero books but has files"
	// authors measured 2026-08-17 are expected to land NOW: fileCounts only
	// counts files of books that reference the author, so an author with files
	// also has a reference, and the refCounts guard (added after that
	// measurement) claims it before the file check runs. Without this sample,
	// HeldBackSample above would be empty on the real library, and the reviewer
	// would again have only a count.
	HeldByRefsSample []heldBackAuthor
}

// heldBackAuthor is one row of emptyAuthorReport.HeldBackSample or
// HeldByRefsSample.
type heldBackAuthor struct {
	// AuthorID lets a reviewer look the row up; the name alone is not unique.
	AuthorID  int
	Name      string
	FileCount int
	// RefCount is how many book references (any state, junction credits
	// included) still hold the author. Always 0 in HeldBackSample.
	RefCount int
}

func (r emptyAuthorReport) summary() string {
	return fmt.Sprintf(
		"authors=%d zero-book=%d held-back(still referenced)=%d held-back(has files)=%d eligible=%d deleted=%d failed=%d "+
			"held(linked during run)=%d held(re-check failed)=%d held(journal failed)=%d",
		r.TotalAuthors, r.ZeroBooks, r.HeldByRefs, r.ZeroBooksWithFiles, r.Eligible, r.Deleted, r.Failed,
		r.HeldLinkedDuringRun, r.HeldLookupFailed, r.JournalFailed)
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
		// 2h, not 30m. Each delete sweeps the whole book_authors junction (see
		// the loop below), measured on production on 2026-09-12 at about one
		// delete per second; a 3,958-author apply hit a 30m timeout after 1,742
		// deletes. Every delete is journaled first and the loop checks ctx
		// between items, so a timeout leaves consistent state -- but it strands
		// the rest of the run. Use `limit` to size a run below this bound.
		Timeout:      2 * time.Hour,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runPurgeEmptyAuthors,
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
	//
	// AND IT MUST BE THE UNFILTERED COUNTER. store.GetAllAuthorFileCounts is a
	// DISPLAY counter, the file-side twin of the GetAllAuthorBookCounts problem
	// argued at length below: it scans the primary-version index only, skips
	// soft-deleted books, and maps books to authors through the legacy
	// Book.AuthorID field alone. So it returns an unconditional zero for a
	// junction-only co-author, for an author whose books are all trashed, and for
	// one whose books are all non-primary — while every one of those books' files
	// is still on disk. Reading it here made "🔴 THE SAFETY THAT MATTERS" report
	// clean for exactly the rows it was written to hold back.
	//
	// database.AuthorFileRefCounts counts non-missing files over every book that
	// references the author by ANY route, in ANY state, and refuses outright
	// (ErrMemdbIncomplete) rather than answering from a memdb known to be missing
	// rows. Its nil-capability case arrives here as an error too, so there is no
	// silent fallback to the filtered count.
	var fileCounts map[int]int
	if params.requireZeroFiles() {
		fileCounts, err = database.AuthorFileRefCounts(store)
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
	// database.AuthorRefCounts is the shared guard -- the same one the entities
	// delete handlers use. It resolves the capability through the decorator
	// chain and returns an error rather than a filtered fallback when the store
	// cannot answer, so the nil-capability case arrives here as an error too.
	refCounts, err := database.AuthorRefCounts(store)
	if err != nil {
		return fmt.Errorf("purge-empty-authors: %w", err)
	}

	// Built BEFORE the dry-run branch below, so `apply=false` and `apply=true`
	// report and delete exactly the same set. A guard applied only on the apply
	// path would make the dry run a lie.
	report, eligible := classifyEmptyAuthors(authors, bookCounts, refCounts, fileCounts, params.requireZeroFiles())
	if params.Limit > 0 && len(eligible) > params.Limit {
		eligible = eligible[:params.Limit]
	}
	report.Eligible = len(eligible)

	if report.ZeroBooksWithFiles > 0 {
		// Its own line so the held-back population arrives with names and file
		// counts rather than only as a number in the summary.
		reporter.Logger().Info("purge-empty-authors held back (zero books, has files)",
			"held_back", report.ZeroBooksWithFiles,
			"sampled", len(report.HeldBackSample),
			"held_back_sample", report.HeldBackSample)
	}
	if report.HeldByRefs > 0 {
		reporter.Logger().Info("purge-empty-authors held back (zero books by the display count, still referenced)",
			"held_by_refs", report.HeldByRefs,
			"sampled", len(report.HeldByRefsSample),
			"held_by_refs_sample", report.HeldByRefsSample)
	}

	if !params.Apply {
		msg := "DRY RUN (nothing deleted) — " + report.summary()
		reporter.Logger().Info("purge-empty-authors dry run",
			"eligible", report.Eligible, "held_by_refs", report.HeldByRefs,
			"sample", report.Sample)
		_ = reporter.UpdateProgress(3, 3, msg)
		return nil
	}

	// Nothing eligible: finish WITHOUT taking the stand-down. Acquiring it blocks
	// until a running library.scan parks, so an apply that has nothing to delete
	// would pause a days-long production scan for no write at all.
	if len(eligible) == 0 {
		msg := "nothing to delete — " + report.summary()
		reporter.Logger().Info("purge-empty-authors complete (nothing eligible)",
			"held_by_refs", report.HeldByRefs, "held_back", report.ZeroBooksWithFiles)
		_ = reporter.UpdateProgress(3, 3, msg)
		return nil
	}

	// THE SCAN STAND-DOWN, acquired ONCE for the whole delete phase, exactly as
	// author_duplicate_merge.go does. refCounts above is a snapshot, and
	// library.scan runs for days on production: while it runs it can link a
	// book to one of these zero-book authors (an import resolving an existing
	// author by name), and a delete after that link strands the book on a
	// deleted author id. A dry run writes nothing and takes no gate -- parking
	// the scanner to read would be a real cost for no reason.
	//
	// 🔴 THE STAND-DOWN IS NOT THE PRIMARY GUARD. It parks library.scan only --
	// metadata apply, book edits and AI ops can still link a book to one of
	// these authors -- and it is inert when the reporter carries no op id. The
	// per-item re-check in the loop below is what actually closes the race; the
	// stand-down narrows the window in which that re-check can fire. (It does
	// park a scan resumed after a restart: on 2026-09-12 a scan at
	// resume_count=5 went interrupted_quiesced for the whole of this op's apply.)
	holderID, standDownHeld, releaseStandDown, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "purge-empty-authors apply")
	if sdErr != nil {
		return fmt.Errorf("purge-empty-authors: acquire scan stand-down: %w", sdErr)
	}
	defer releaseStandDown()

	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		// Same precedent as author_duplicate_merge.go and series_dedup.go: journal
		// anyway. A ledger row with no operation id still records the name.
		reporter.Logger().Warn("purge-empty-authors: no operation id on the reporter, undo-ledger rows will be unattributed")
	}

	// Deleted one at a time rather than in a bulk batch. DeleteAuthor removes the
	// author row, its name index and its aliases, and sweeps the author out of
	// the per-book `book_authors:<bookID>` junction -- a FULL scan of that
	// keyspace on every delete (sweepAuthorFromBookAuthors, pebble_store_authors.go;
	// there is no author -> books reverse index). That sweep is the per-item
	// cost: measured on production on 2026-09-12 at about one delete per second,
	// so a 3,958-author apply held the scan stand-down for over an hour. A bulk
	// delete that swept the junction once for the whole eligible set would
	// remove that cost; it needs a store method that does not exist yet.
	//
	// Deliberately SEQUENTIAL, against the repo's default preference for a
	// worker pool: the per-item re-check -> journal -> delete sequence must run
	// under a live stand-down lease, and the renew-then-abort contract is per
	// item. Parallel deletes would also serialize on the name-index lock, which
	// DeleteAuthor holds across its sweep and commit.
	authorNames := make(map[int]string, len(eligible))
	for _, a := range authors {
		authorNames[a.ID] = a.Name
	}
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
		name := authorNames[id]

		// The per-item half of the stand-down contract: renew the lease and treat
		// losing it as a hard abort of the REMAINING deletes. A lapsed lease means
		// the scanner has resumed. Inert when the gate is not held.
		if scanStandDownLostForApply(p.deps, holderID, standDownHeld) {
			reporter.Logger().Warn("purge-empty-authors: scan stand-down lease lost, aborting remaining deletes",
				"deleted", report.Deleted, "remaining", len(eligible)-i)
			prog.Done("aborted (stand-down lease lost) — " + report.summary())
			return fmt.Errorf("purge-empty-authors: scan stand-down lease lost after %d delete(s), "+
				"refusing to keep deleting while the scanner is running", report.Deleted)
		}

		// 🔴 THE PER-ITEM RE-CHECK -- the primary guard against the scan race.
		// refCounts was computed once, possibly hours ago; this asks again for
		// THIS author immediately before its delete, with a point lookup.
		//
		// GetBooksByAuthorIDWithRoleCore returns every version (primary and
		// non-primary) linked through the book_authors junction or the legacy
		// Book.AuthorID field, through memdb or -- when memdb is known to be
		// missing rows -- the authoritative Pebble scan. It EXCLUDES soft-deleted
		// (trashed) books on both paths. That is sufficient here, not a gap:
		// trashed references were already counted by database.AuthorRefCounts
		// above, and any author they hold was classified HeldByRefs and never
		// reached this loop. What this lookup must catch is a link created AFTER
		// that count, and a scan or import creates live book rows, not trashed
		// ones. So a lookup covering live books closes the race.
		//
		// An error is "cannot answer", and the lookup is fail-closed on both
		// paths, so it is held -- never read as "zero links".
		linked, lerr := store.GetBooksByAuthorIDWithRoleCore(id)
		if lerr != nil {
			report.HeldLookupFailed++
			reporter.Logger().Warn("purge-empty-authors: re-check lookup failed, author held (not deleted)",
				"author_id", id, "name", name, "err", lerr)
			continue
		}
		if len(linked) > 0 {
			report.HeldLinkedDuringRun++
			reporter.Logger().Warn("purge-empty-authors: author linked to a book during the run, held (not deleted)",
				"author_id", id, "name", name, "linked_books", len(linked), "first_book_id", linked[0].ID)
			continue
		}

		// THE UNDO-LEDGER ROW, written BEFORE the delete. database.Author is
		// {ID, Name}, so "id:name" is the whole row, and the name exists nowhere
		// else once the row is gone -- a post-delete journal failure would be
		// exactly the loss this op must not cause. So a failed ledger write skips
		// the delete (fail closed). Same change_type/field/format as
		// journalAuthorMerge in author_duplicate_merge.go; like that row, the undo
		// engine does not replay "author_delete" -- it is a durable record from
		// which the author can be recreated, not a one-click restore.
		//
		// The ledger row can describe a delete that then fails. That is the right
		// trade (a stray record of an author that still exists is harmless; a
		// deleted author with no record is not), and it is logged below.
		if jerr := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			ChangeType:  "author_delete",
			FieldName:   "author",
			OldValue:    fmt.Sprintf("%d:%s", id, name),
			NewValue:    "purged_empty",
		}); jerr != nil {
			report.JournalFailed++
			reporter.Logger().Warn("purge-empty-authors: undo-ledger write failed, author NOT deleted",
				"author_id", id, "name", name, "err", jerr)
			continue
		}

		if derr := store.DeleteAuthor(id); derr != nil {
			// One bad row must not abandon the other thousands; count it and move on.
			report.Failed++
			reporter.Logger().Warn("purge-empty-authors: delete failed (its undo-ledger row was already written and names an author that still exists)",
				"author_id", id, "name", name, "err", derr)
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
		"held_by_refs", report.HeldByRefs, "held_back", report.ZeroBooksWithFiles,
		"held_linked_during_run", report.HeldLinkedDuringRun,
		"held_recheck_failed", report.HeldLookupFailed,
		"held_journal_failed", report.JournalFailed)
	prog.Done(msg)
	return nil
}

// classifyEmptyAuthors sorts every author into the report's buckets and returns
// the SORTED eligible IDs (before any Limit is applied). It is pure — maps in,
// report out — so the classification can be tested without a store or reporter,
// and so the dry-run and apply paths are fed by the same code.
//
// bookCounts only picks the candidate set; refCounts is the delete guard; a nil
// fileCounts is only valid when requireZeroFiles is false (the caller skips the
// file pass in that case).
func classifyEmptyAuthors(authors []database.Author, bookCounts, refCounts, fileCounts map[int]int, requireZeroFiles bool) (emptyAuthorReport, []int) {
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
			if len(report.HeldByRefsSample) < emptyAuthorSampleLimit {
				report.HeldByRefsSample = append(report.HeldByRefsSample, heldBackAuthor{
					AuthorID:  a.ID,
					Name:      a.Name,
					FileCount: fileCounts[a.ID], // nil map (require_zero_files=false) reads 0
					RefCount:  refCounts[a.ID],
				})
			}
			continue
		}
		if requireZeroFiles && fileCounts[a.ID] != 0 {
			report.ZeroBooksWithFiles++
			// The counter above stays uncapped; only the sample is bounded.
			if len(report.HeldBackSample) < emptyAuthorSampleLimit {
				report.HeldBackSample = append(report.HeldBackSample, heldBackAuthor{
					AuthorID:  a.ID,
					Name:      a.Name,
					FileCount: fileCounts[a.ID],
				})
			}
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
	return report, eligible
}
