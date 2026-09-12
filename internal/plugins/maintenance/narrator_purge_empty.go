// file: internal/plugins/maintenance/narrator_purge_empty.go
// version: 1.1.0
// guid: b1c1b8ef-893a-4ff8-9714-8ed1521febfa
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- purge-empty-narrators ---
//
// The narrator twin of maintenance.purge-empty-authors (author_purge_empty.go):
// deletes narrator rows that no book links to. Same defaults and the same
// fail-closed reference count, plus two guards the author op does not have,
// because the library scan in production runs for days and an apply will
// almost always overlap it:
//
//  1. A per-narrator link RE-CHECK immediately before each delete
//     (database.NarratorLinkCount, a live read of the book_narrators junction).
//     A narrator that gained a link after the bulk count is held as
//     "linked_during_run", not deleted. THIS is the guard that matters: the
//     writers that add narrator links (metadata apply, book edits,
//     optimize-database) are not the scanner, so no scan gate can stop them.
//  2. The scan stand-down on apply, with the per-item lease check, as in
//     author_duplicate_merge.go. A backup to (1), not a substitute for it.
//     It does park a scan resumed after a restart: on 2026-09-12 a scan at
//     resume_count=5 went interrupted_quiesced for the whole of a
//     purge-empty-authors apply.
//
// UNDO LEDGER: each delete is preceded by an operation_changes row
// (change_type "narrator_delete", old value "<id>:<name>"), in the same shape
// purge-empty-authors writes "author_delete". database.Narrator is {ID, Name},
// so the row is the whole narrator. A failed ledger write skips that delete
// (fail closed). The undo engine does not replay these rows; they are a
// durable record to recreate a narrator from, not a one-click restore.

// emptyNarratorSampleLimit bounds each name list in the report.
const emptyNarratorSampleLimit = 100

// Hold-back reasons, as they appear in the report and the logs.
const (
	narratorHeldNameInText      = "name_in_book_text"
	narratorHeldLinkedDuringRun = "linked_during_run"
)

// errNarratorPurgeStandDownLost aborts the remaining deletes when the scan
// stand-down lease lapses mid-run.
var errNarratorPurgeStandDownLost = errors.New("purge-empty-narrators: scan stand-down lease lost; remaining deletes abandoned")

// purgeEmptyNarratorsParams are the JSON parameters accepted by the op.
type purgeEmptyNarratorsParams struct {
	// Apply, if true, actually deletes. Default false (dry run / report only):
	// deletion is irreversible and this is a production library.
	Apply bool `json:"apply"`

	// RequireNoNameMatch, when true (the DEFAULT), holds back a narrator with
	// no junction link whose name still appears in some book's Narrator text.
	//
	// The author op's require_zero_files analogue, with a different
	// justification. Deleting such a narrator does NOT lose the name: the text
	// keeps it, and the next edit of that book re-mints a row through
	// GetNarratorByName/CreateNarrator (under a new id). It is held because
	// "credited in text, linked nowhere" looks more like a book that lost its
	// junction row than like importer junk, and that is worth a look before a
	// bulk delete. Pass false once the held sample has been reviewed.
	RequireNoNameMatch *bool `json:"require_no_name_match,omitempty"`

	// Limit caps how many narrators are deleted in one run (0 = no cap), so a
	// first apply can be run small and inspected.
	Limit int `json:"limit"`
}

// requireNoNameMatch resolves the tri-state pointer to its default of TRUE; a
// plain bool would default to the permissive setting.
func (p purgeEmptyNarratorsParams) requireNoNameMatch() bool {
	return p.RequireNoNameMatch == nil || *p.RequireNoNameMatch
}

// heldBackNarrator is one sampled row a guard kept.
type heldBackNarrator struct {
	NarratorID int    `json:"narrator_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
	// Books whose Narrator text credits the name (name_in_book_text), or
	// books linking it through book_narrators at re-check time
	// (linked_during_run).
	Books int `json:"books"`
}

// emptyNarratorReport is the outcome of one pass. The dry run and the apply
// fill it from the same classification, so they report the same set.
type emptyNarratorReport struct {
	// TotalNarrators is what ListNarrators could decode. It skips undecodable
	// rows, which can only UNDER-report candidates, never add one.
	TotalNarrators int `json:"total_narrators"`
	// Referenced narrators have at least one book_narrators link in any book
	// state. They are not candidates.
	Referenced int `json:"referenced"`
	// Candidates have zero links by the unfiltered count.
	Candidates int `json:"candidates"`
	// HeldNameInText are candidates kept by require_no_name_match.
	HeldNameInText       int                `json:"held_name_in_text"`
	HeldNameInTextSample []heldBackNarrator `json:"held_name_in_text_sample,omitempty"`
	// Eligible is what this run would delete, after holds and Limit.
	Eligible int      `json:"eligible"`
	Sample   []string `json:"sample,omitempty"`
	// HeldLinkedDuringRun are eligible narrators the per-item re-check found
	// linked at delete time (apply only).
	HeldLinkedDuringRun       int                `json:"held_linked_during_run"`
	HeldLinkedDuringRunSample []heldBackNarrator `json:"held_linked_during_run_sample,omitempty"`
	// JournalFailed are eligible narrators NOT deleted because their undo-ledger
	// row could not be written (apply only).
	JournalFailed int `json:"held_journal_failed"`
	Deleted       int `json:"deleted"`
	Failed        int `json:"failed"`
	// Aborted is set when the scan stand-down lease was lost mid-apply.
	Aborted string `json:"aborted,omitempty"`
}

func (r emptyNarratorReport) summary() string {
	s := fmt.Sprintf(
		"narrators=%d referenced=%d candidates(zero links)=%d held(name in book text)=%d eligible=%d "+
			"held(linked during run)=%d held(journal failed)=%d deleted=%d failed=%d",
		r.TotalNarrators, r.Referenced, r.Candidates, r.HeldNameInText, r.Eligible,
		r.HeldLinkedDuringRun, r.JournalFailed, r.Deleted, r.Failed)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

func (p *Plugin) purgeEmptyNarratorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.purge-empty-narrators",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Purge empty narrators",
		Description: "Deletes narrator rows that no book links to. DRY-RUN BY DEFAULT: pass apply=true " +
			"to delete. Links are counted in every book state (trashed, non-primary, and junction rows " +
			"whose book is gone), and the op refuses to run if that count cannot be computed. By default " +
			"holds back a narrator whose name still appears in some book's narrator text; pass " +
			"require_no_name_match=false to include them. On apply it re-checks each narrator's links " +
			"immediately before deleting it and holds the scan stand-down. limit caps deletions per run. " +
			"Idempotent.",
		// ResumeDrop, as in purge-empty-authors: a deletion that silently
		// resumes after a restart is harder to reason about than one that is
		// re-triggered deliberately, and a re-run is cheap and idempotent.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-empty-narrators",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runPurgeEmptyNarrators,
	}
}

func (p *Plugin) runPurgeEmptyNarrators(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params purgeEmptyNarratorsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := p.purgeEmptyNarrators(ctx, params, reporter)
	return err
}

// purgeEmptyNarrators does the work and returns the report, so tests can
// assert on what was held and why, not only on which rows survived.
func (p *Plugin) purgeEmptyNarrators(ctx context.Context, params purgeEmptyNarratorsParams, reporter sdk.Reporter) (emptyNarratorReport, error) {
	store := p.deps.OpsStore()
	if store == nil {
		return emptyNarratorReport{}, fmt.Errorf("database not initialized")
	}
	if params.Limit < 0 {
		return emptyNarratorReport{}, fmt.Errorf("purge-empty-narrators: limit must be >= 0, got %d", params.Limit)
	}
	log := reporter.Logger()
	log.Info("purge-empty-narrators start",
		"apply", params.Apply, "require_no_name_match", params.requireNoNameMatch(), "limit", params.Limit)

	_ = reporter.UpdateProgress(0, 3, "Listing narrators…")
	narrators, err := store.ListNarrators()
	if err != nil {
		return emptyNarratorReport{}, fmt.Errorf("list narrators: %w", err)
	}

	// The delete guard. Unfiltered and fail-closed: a store that cannot answer
	// is an error, never an empty map, because an empty map reads as "nothing
	// references anything" and would delete every narrator.
	_ = reporter.UpdateProgress(1, 3, "Counting narrator references…")
	refs, err := database.NarratorRefCounts(store)
	if err != nil {
		return emptyNarratorReport{}, fmt.Errorf("purge-empty-narrators: %w", err)
	}

	// Classified BEFORE the dry-run branch, so apply=false and apply=true
	// report exactly the same set.
	report, eligible := classifyEmptyNarrators(narrators, refs, params.requireNoNameMatch())
	if params.Limit > 0 && len(eligible) > params.Limit {
		eligible = eligible[:params.Limit]
	}
	report.Eligible = len(eligible)
	for _, n := range eligible {
		if len(report.Sample) >= emptyNarratorSampleLimit {
			break
		}
		report.Sample = append(report.Sample, n.Name)
	}

	// Its own line, always: if zero-link narrators turn out to be mostly the
	// same population as names still credited in text, the default holds
	// nearly everything and the dry run says eligible=0. An operator must be
	// able to see at a glance that the hold is doing that, not a broken op.
	log.Info("purge-empty-narrators held back (zero links, name still in book narrator text)",
		"held_name_in_text", report.HeldNameInText,
		"candidates", report.Candidates,
		"require_no_name_match", params.requireNoNameMatch(),
		"sample", report.HeldNameInTextSample)

	if !params.Apply {
		log.Info("purge-empty-narrators dry run", "eligible", report.Eligible, "sample", report.Sample)
		_ = reporter.UpdateProgress(3, 3, "DRY RUN (nothing deleted) — "+report.summary())
		return report, nil
	}

	// Nothing to delete: return before the stand-down, so an apply that would
	// delete nothing does not park a running scan (the purge-empty-authors fix
	// in #3307, applied here too).
	if len(eligible) == 0 {
		log.Info("purge-empty-narrators: nothing eligible, no stand-down taken")
		_ = reporter.UpdateProgress(3, 3, "nothing to delete — "+report.summary())
		return report, nil
	}

	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "purge-empty-narrators apply")
	if sdErr != nil {
		return report, fmt.Errorf("purge-empty-narrators: could not acquire scan stand-down; refusing to delete: %w", sdErr)
	}
	defer release()

	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		// Journal anyway, as purge-empty-authors does: a ledger row with no
		// operation id still records the narrator's id and name.
		log.Warn("purge-empty-narrators: no operation id on the reporter, undo-ledger rows will be unattributed")
	}

	// The authors cache also holds the narrator contributor index (the entities
	// handler invalidates it after SetBookNarrators). Deferred so a cancel or
	// abort after some deletes still drops the stale list.
	defer func() {
		if report.Deleted > 0 {
			p.deps.InvalidateAuthorsCache()
		}
	}()

	// SEQUENTIAL, deliberately. Each DeleteNarrator is a pebble.Sync commit
	// plus a full sweep of the book_narrators junction, and each re-check below
	// is another junction scan that must observe the deletes before it. A pool
	// would serialize on the WAL anyway and add a window in which two workers'
	// sweeps interleave. The population is under a thousand (966 narrators in
	// total at a recent production warmup), so there is nothing to win.
	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Deleting %d empty narrators…", len(eligible)))
	prog := sdk.NewProgress(reporter, len(eligible))
	prog.Start(fmt.Sprintf("Deleting %d empty narrators…", len(eligible)))
	for i, n := range eligible {
		if err := ctx.Err(); err != nil {
			log.Info("purge-empty-narrators cancelled", "deleted", report.Deleted, "remaining", len(eligible)-i)
			prog.Done("cancelled — " + report.summary())
			return report, err
		}
		if scanStandDownLostForApply(p.deps, holderID, held) {
			report.Aborted = errNarratorPurgeStandDownLost.Error()
			log.Warn("purge-empty-narrators: scan stand-down lease lost, abandoning remaining deletes",
				"deleted", report.Deleted, "remaining", len(eligible)-i)
			prog.Done(report.summary())
			return report, errNarratorPurgeStandDownLost
		}

		// THE PER-ITEM RE-CHECK. The bulk count above may be minutes old by
		// now, and a metadata apply or a book edit can link this narrator in
		// the meantime. A re-check that cannot answer is fatal: continuing
		// would mean deleting unverified rows.
		//
		// Residual window: a writer that already resolved this narrator by name
		// can still commit a link between this read and DeleteNarrator's
		// commit. If it lands before DeleteNarrator's sweep, the sweep strips
		// that one credit, which leaves consistent state, and the book's
		// Narrator text normally still names the person, so the next edit
		// re-mints the row. If it lands after the commit, the book holds a
		// dangling narrator id. Closing that needs a lock shared with every
		// SetBookNarrators caller; nothing in the store provides one today.
		links, lerr := database.NarratorLinkCount(store, n.ID)
		if lerr != nil {
			prog.Done(report.summary())
			return report, fmt.Errorf("purge-empty-narrators: re-check narrator %d before delete (deleted %d so far): %w",
				n.ID, report.Deleted, lerr)
		}
		if links > 0 {
			report.HeldLinkedDuringRun++
			if len(report.HeldLinkedDuringRunSample) < emptyNarratorSampleLimit {
				report.HeldLinkedDuringRunSample = append(report.HeldLinkedDuringRunSample, heldBackNarrator{
					NarratorID: n.ID, Name: n.Name, Reason: narratorHeldLinkedDuringRun, Books: links,
				})
			}
			log.Info("purge-empty-narrators held (linked during run)", "narrator_id", n.ID, "name", n.Name, "books", links)
			continue
		}

		// THE UNDO-LEDGER ROW, written BEFORE the delete: once the row is gone
		// the name exists nowhere else, so a post-delete journal failure would be
		// exactly the loss this op must not cause. A failed write skips the
		// delete. The row can describe a delete that then fails; a stray record
		// of a narrator that still exists is harmless, the reverse is not.
		if jerr := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			ChangeType:  "narrator_delete",
			FieldName:   "narrator",
			OldValue:    fmt.Sprintf("%d:%s", n.ID, n.Name),
			NewValue:    "purged_empty",
		}); jerr != nil {
			report.JournalFailed++
			log.Warn("purge-empty-narrators: undo-ledger write failed, narrator NOT deleted",
				"narrator_id", n.ID, "name", n.Name, "err", jerr)
			continue
		}

		// DeleteNarrator removes the row, its ownership-checked name-index
		// entry, and any junction entry (none, per the re-check), then brings
		// memdb in line: DeleteNarratorFromMemDB plus a replay of every swept
		// book's credits.
		if derr := store.DeleteNarrator(n.ID); derr != nil {
			report.Failed++
			log.Warn("purge-empty-narrators: delete failed (its undo-ledger row was already written and names a narrator that still exists)",
				"narrator_id", n.ID, "name", n.Name, "err", derr)
			continue
		}
		report.Deleted++
		log.Info("purge-empty-narrators deleted", "narrator_id", n.ID, "name", n.Name)
		if (i+1)%50 == 0 {
			prog.StepN(i+1, fmt.Sprintf("Deleted %d/%d…", report.Deleted, len(eligible)))
		}
	}

	log.Info("purge-empty-narrators complete",
		"deleted", report.Deleted, "failed", report.Failed,
		"held_linked_during_run", report.HeldLinkedDuringRun, "held_name_in_text", report.HeldNameInText)
	prog.Done(report.summary())
	return report, nil
}

// classifyEmptyNarrators sorts every narrator into the report's buckets and
// returns the eligible rows sorted by ID (before any Limit). Pure, so the dry
// run and the apply are fed by the same code.
func classifyEmptyNarrators(narrators []database.Narrator, refs database.NarratorRefs, requireNoNameMatch bool) (emptyNarratorReport, []database.Narrator) {
	report := emptyNarratorReport{TotalNarrators: len(narrators)}
	var eligible []database.Narrator
	for _, n := range narrators {
		if refs.ByID[n.ID] != 0 {
			report.Referenced++
			continue
		}
		report.Candidates++
		if inText := refs.ByName[util.NormalizeAuthor(n.Name)]; requireNoNameMatch && inText != 0 {
			report.HeldNameInText++
			if len(report.HeldNameInTextSample) < emptyNarratorSampleLimit {
				report.HeldNameInTextSample = append(report.HeldNameInTextSample, heldBackNarrator{
					NarratorID: n.ID, Name: n.Name, Reason: narratorHeldNameInText, Books: inText,
				})
			}
			continue
		}
		eligible = append(eligible, n)
	}
	// Deterministic order, so a limited run takes the same slice every time.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return report, eligible
}
