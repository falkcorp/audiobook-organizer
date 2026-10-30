// file: internal/server/entities_ops.go
// version: 1.17.0
// guid: 3f7e2a91-b4c6-4d85-9e13-7a2f10c84d32
// last-edited: 2026-10-30

// entities_ops registers the UOS-02 OperationDefs for author entity
// operations: author-merge and resolve-production-author. Each def is
// registered via addOpRegistrar in init(), so no edits to server.go are
// required.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
)

// authorMergeOpParams holds the parameters for the entities.author-merge op. It is
// the one definition of the wire shape: package handlers/entities builds it
// (entities.AuthorMergeOpParams) and Run decodes it here, so the two cannot drift.
type authorMergeOpParams = entities.AuthorMergeOpParams

// resolveProductionAuthorOpParams holds the parameters for the
// entities.resolve-production-author op.
type resolveProductionAuthorOpParams struct {
	AuthorID       int    `json:"author_id"`
	ProdAuthorName string `json:"prod_author_name"`
}

// RegisterAuthorMergeOp registers the "entities.author-merge" v2 OperationDef.
func (s *Server) RegisterAuthorMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "entities.author-merge",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "entities",
		DisplayName:     "Author Merge",
		Description:     "Merge one or more author records into a single canonical author, relinking all associated books.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		// Interactive: a person triggered this merge and is waiting on it, so it does
		// not wait out the post-restart startup warmup (see NoWarmupWait).
		NoWarmupWait: true,
		Timeout:      2 * time.Hour,
		// RESUME AUDIT 2026-09-11 (b): ResumeDrop, was ResumeRestart with no
		// checkpoint, which meant a restart re-ran every merge id from zero. A
		// re-issued merge for an id already deleted is not proven to no-op:
		// GetBooksByAuthorIDWithRoleCore on a gone author and a second
		// DeleteAuthor/CreateAuthorTombstone are unverified paths, and the op
		// change ledger would get a second set of rows under the same op id.
		// This is the "auto-merge must not double-merge" shape CLAUDE.md warns
		// about, and the loop is seconds long, so an interrupted merge is
		// surfaced as interrupted_dropped for the user to re-issue deliberately.
		ResumePolicy:   opsregistry.ResumeDrop,
		ConcurrencyKey: "entities.author-merge",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p authorMergeOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("author-merge: decode params: %w", err)
				}
			}

			store := s.Ops()
			// The op change ledger (opchange:<opID>:<changeID>) keys by this id.
			// It is an independent prefix keyspace with no parent row, and undo
			// reads it per-book, so moving both sides to the v2 id is safe.
			opID := opsregistry.ReporterOpID(reporter)
			keepID := p.KeepID
			mergeIDs := p.MergeIDs

			progress := registryProgressAdapter{r: reporter}

			// Stand the library scanner down for the whole merge, before the first
			// write: a concurrent library.scan rewrites book_authors on the rows
			// this loop repoints. Nothing is written when this refuses, so there is
			// nothing to invalidate.
			release, herr := s.holdAuthorMergeStandDown(ctx, opID, "author-merge apply")
			if herr != nil {
				return fmt.Errorf("author-merge: %w", herr)
			}
			defer release()

			_ = progress.Log("info", fmt.Sprintf("Merging %d author(s) into %q", len(mergeIDs), p.KeepName), nil)
			_ = progress.UpdateProgress(0, len(mergeIDs), "Starting author merge...")

			out, cancelled := mergeAuthorsInto(ctx, store, opID, authorMergeSpec{
				KeepID:   keepID,
				MergeIDs: mergeIDs,
				// Locks are honoured unless the caller says otherwise: the Authors
				// view's request carries no such field, and a merge of many groups
				// from one click must not move a book whose author the owner pinned.
				HonorLocks: p.HonorLocks == nil || *p.HonorLocks,
				Scan:       s.scanGate(),
			}, progress.IsCanceled, func(done, total int) {
				_ = progress.UpdateProgress(done, total, fmt.Sprintf("Merged %d/%d authors", done, total))
			})
			if cancelled {
				// The ids that DID merge before the cancel are gone.
				s.dedupCache.InvalidateAll()
				s.authorsCache.InvalidateAll()
				return fmt.Errorf("cancelled")
			}

			resultMsg := fmt.Sprintf("Author merge complete: merged %d, %d errors", out.Merged, len(out.Errors))
			_ = progress.Log("info", resultMsg, nil)
			if len(out.Errors) > 0 {
				errDetail := strings.Join(out.Errors[:min(len(out.Errors), 10)], "; ")
				_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
			}
			// Invalidate even on a partial failure: the ids that DID merge are gone.
			s.dedupCache.InvalidateAll()
			s.authorsCache.InvalidateAll()
			// A lost stand-down lease stops the loop part way, so it fails the op even
			// when the ids it never reached were all already merged: the run did not
			// finish under the gate.
			if failed := unmergedAuthorIDs(mergeIDs, keepID, out.MergedIDs); len(failed) > 0 || out.Aborted != "" {
				// Report failure rather than completing: the op used to return nil
				// here, so the UI treated a merge that left authors behind as done
				// and dropped the whole group. The id list is in a fixed form
				// (AuthorMergeFailedIDsPrefix) so the client can keep exactly the
				// variants that remain.
				detail := ""
				switch {
				case out.Aborted != "":
					detail = ": " + out.Aborted
				case len(out.Errors) > 0:
					detail = ": " + strings.Join(out.Errors[:min(len(out.Errors), 3)], "; ")
				}
				return fmt.Errorf("%s%s] of %d not merged, %d merged%s",
					AuthorMergeFailedIDsPrefix, joinInts(failed), len(mergeIDs), out.Merged, detail)
			}
			return nil
		},
	})
}

// authorMergeStore is what folding one author into another reads and writes.
type authorMergeStore interface {
	authorAssignStore
	database.AuthorRelinkLister
	// The field-lock reader (database.LoadFieldLocks).
	database.MetadataFieldStateReader
	authorMergeJournalStore
	GetAuthorByID(id int) (*database.Author, error)
	// GetAuthorByName: whether the merged author holds its own name entry, read
	// for the delete row journaled before the merge-delete.
	GetAuthorByName(name string) (*database.Author, error)
	// The merge-delete itself (database.AuthorMergeDeleter) is a capability, not
	// part of this interface: mergeAuthorsInto resolves it from the store and
	// refuses the whole merge when the store does not offer it.
}

// authorMergeJournalStore: the undo ledger rows, and the aliases the merge moves
// (read to be journaled before the delete).
type authorMergeJournalStore interface {
	CreateOperationChange(change *database.OperationChange) error
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
}

// authorMergeSpec is one merge: fold MergeIDs into KeepID.
type authorMergeSpec struct {
	KeepID   int
	MergeIDs []int
	// HonorLocks leaves a book whose author_name is user-locked on the author it is
	// credited to. That author is then not deleted (a book still credits it) and the
	// lock is named in Errors. The zero value ignores locks; callers pass true
	// unless the user chose otherwise.
	HonorLocks bool
	// DryRun reads and decides everything a merge would, writes nothing, and
	// reports what it would have done in the outcome (Merged counts the authors
	// it would delete; Errors names the locks that would hold an author back).
	DryRun bool
	// Scan is the scan stand-down the CALLER already holds under the merge's op id;
	// the loop only renews its lease, once per author, and stops writing when the
	// lease is lost (the DedupSeries / MergeSeriesOptions.Scan contract). The
	// caller acquires, not the loop, because ai.author-scan-apply runs this loop
	// once per result and a hold keyed by the op id cannot be taken again after the
	// first loop releases it: one hold has to span all of them. Nil means no gate
	// is held (a preview, which writes nothing).
	Scan scanLeaseRenewer
}

// scanLeaseRenewer is the half of the scan stand-down a merge loop needs once its
// caller holds it. *Server and opsregistry.ScanStandDownGate satisfy it.
type scanLeaseRenewer interface {
	// RenewScanStandDown extends the holder's lease; false means the lease is gone
	// and the scanner has been (or is about to be) resumed.
	RenewScanStandDown(holderOpID string) bool
}

// authorMergeOutcome is what a merge did.
type authorMergeOutcome struct {
	// Aborted is set when the stand-down lease was lost and the loop stopped before
	// writing the next author. What was merged before stays merged.
	Aborted string
	// Merged counts authors this run folded in and deleted.
	Merged int
	// MergedIDs is every merge id that no longer exists as a distinct author: the
	// ones this run merged, and the ones that were already merged into the keep
	// author when it got there. The failed set is every other id, and it is what a
	// caller needs: a per-id log line is not something a client can act on.
	MergedIDs map[int]bool
	Errors    []string
}

// journalStep is authormerge.Step: one undo-ledger row written BEFORE the write
// it describes, with the function that voids it if that write is refused.
func journalStep(store authorMergeJournalStore, opID string, row *database.OperationChange) (void func() error, err error) {
	return authormerge.Step(store, opID, row)
}

// mergeAuthorsInto folds spec.MergeIDs into spec.KeepID, one author at a time,
// relinking every book (trash included), journaling each write for undo (see
// undo/author_merge.go), deleting the merged author once nothing credits it and
// leaving a tombstone. It is the body of entities.author-merge and of
// ai.author-scan-apply, which must behave the same.
//
// Every write is journaled first. A book whose row cannot be journaled is not
// moved, and the author is then not deleted (a book still credits it); an author
// whose delete row cannot be journaled is not deleted.
//
// canceled is polled between authors; a true return stops the merge and
// cancelled is reported true (what was merged stays merged). step, when not nil,
// is told how many of the ids have been handled.
//
// A merge id whose author is already gone counts as merged only when it was merged
// into THIS keep author: an earlier merge (this run's, or another screen's) did
// it. One merged into a different author fails with that author named, and an id
// with no author and no tombstone is not found. A keep author that is gone fails
// every id: there is nothing to fold them into.
func mergeAuthorsInto(
	ctx context.Context,
	store authorMergeStore,
	opID string,
	spec authorMergeSpec,
	canceled func() bool,
	step func(done, total int),
) (out authorMergeOutcome, cancelled bool) {
	keepID := spec.KeepID
	out.MergedIDs = make(map[int]bool, len(spec.MergeIDs))

	// The merge-delete is a store capability (database.AuthorMergeDeleter). A
	// store without it cannot remove a merged author without deleting its
	// aliases, so the merge is refused here, before any book is moved; there is
	// no DeleteAuthor fallback.
	if _, ok := database.AsCapability[database.AuthorMergeDeleter](store); !ok {
		out.Errors = append(out.Errors, fmt.Sprintf("nothing merged into author %d: %v", keepID, database.ErrAuthorMergeDeleteUnsupported))
		return out, false
	}

	// GetAuthorByID FOLLOWS the author tombstone: asked for a merged-away id it
	// answers with the author it was merged into. An answer whose ID is not the one
	// asked for therefore means the author is gone.
	keepAuthor, kerr := store.GetAuthorByID(keepID)
	if kerr != nil || keepAuthor == nil || keepAuthor.ID != keepID {
		why := "it no longer exists"
		if kerr != nil {
			why = kerr.Error()
		}
		out.Errors = append(out.Errors, fmt.Sprintf("keep author %d cannot be read, nothing merged: %s", keepID, why))
		return out, false
	}
	for i, mergeID := range spec.MergeIDs {
		if canceled != nil && canceled() {
			return out, true
		}
		// Heartbeat the lease before each author's writes and treat a lost lease as
		// a HARD ABORT: a lapsed lease resumes the scanner, so writing past it means
		// two writers on the same book_authors rows with no gate.
		if spec.Scan != nil && !spec.Scan.RenewScanStandDown(opID) {
			out.Aborted = scanStandDownLostMessage(i, len(spec.MergeIDs))
			out.Errors = append(out.Errors, out.Aborted)
			return out, false
		}
		if mergeID == keepID {
			continue
		}
		mergeAuthor, merr := store.GetAuthorByID(mergeID)
		if merr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("failed to read author %d: %v", mergeID, merr))
			continue
		}
		if mergeAuthor == nil {
			// Deleted with no tombstone: not found, not merged. Counting it merged would
			// let a suggestion whose author never existed (or was purged) read as done.
			out.Errors = append(out.Errors, fmt.Sprintf("author %d was not found", mergeID))
			continue
		}
		if mergeAuthor.ID != mergeID {
			// Merged away already. Done only if it went into THIS keep author: one
			// merged into somebody else is still not part of this group, and
			// calling it merged would drop a suggestion whose authors are not
			// together (result R1 folds B into A; R2 asks to fold B and D into C).
			if mergeAuthor.ID == keepID {
				out.MergedIDs[mergeID] = true
			} else {
				out.Errors = append(out.Errors, fmt.Sprintf(
					"author %d was already merged into author %d, not author %d", mergeID, mergeAuthor.ID, keepID))
			}
			continue
		}
		mergeAuthorName := mergeAuthor.Name
		// Unlike the series merge there is no "same name, nothing moves" exemption:
		// author names are unique, so a merge always moves a book to a differently
		// named author.
		checkLocks := spec.HonorLocks

		// ForRelink includes the trash: DeleteAuthor below sweeps the
		// merged author out of trashed books' junction rows too, so a
		// trashed book skipped here lost its credit for good.
		books, err := store.GetBooksByAuthorIDForRelinkCore(mergeID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("failed to get books for author %d: %v", mergeID, err))
			continue
		}

		// held: books this merge will not move (a locked author name, or a lock it
		// could not read, which is not an answer of "unlocked"). It is the reason
		// the author is not deleted, and in a preview the reason it is not counted merged.
		lockedBooks, unreadable := 0, 0
		moveFailures := 0
		for _, book := range books {
			if checkLocks {
				locks, lerr := database.LoadFieldLocks(store, book.ID)
				if lerr != nil {
					unreadable++
					out.Errors = append(out.Errors, fmt.Sprintf("book %s: not moved off author %d, %v", book.ID, mergeID, lerr))
					continue
				}
				if locks.Locked(database.FieldKeyAuthorName) {
					lockedBooks++
					out.Errors = append(out.Errors,
						fmt.Sprintf("book %s: author name is user-locked as %q; not moved to %q (author %d kept)",
							book.ID, mergeAuthorName, keepAuthor.Name, mergeID))
					continue
				}
			}
			if spec.DryRun {
				continue
			}
			bookAuthors, err := store.GetBookAuthors(book.ID)
			if err != nil {
				moveFailures++
				out.Errors = append(out.Errors, fmt.Sprintf("book %s: credits unreadable, not moved off author %d: %v", book.ID, mergeID, err))
				continue
			}
			hasKeep := false
			for _, ba := range bookAuthors {
				if ba.AuthorID == keepID {
					hasKeep = true
					break
				}
			}
			var newAuthors []database.BookAuthor
			for _, ba := range bookAuthors {
				if ba.AuthorID == mergeID {
					if !hasKeep {
						ba.AuthorID = keepID
						newAuthors = append(newAuthors, ba)
						hasKeep = true
					}
				} else {
					newAuthors = append(newAuthors, ba)
				}
			}
			// Journaled first (authormerge.MoveCredits): a book whose row cannot be
			// recorded is not moved, and the primary is repointed only when it
			// names the merged author. Into is the keep row read above, written as
			// the repointed book's denormalized Author so it does not keep naming
			// the merged-away author.
			moved, merr := authormerge.MoveCredits(store, opID, authormerge.CreditMove{
				BookID: book.ID, FromID: mergeID, IntoID: keepID, Into: keepAuthor,
				Before: bookAuthors, After: newAuthors, PrimaryBefore: book.AuthorID,
			})
			if merr != nil {
				moveFailures++
				logging.Warn(ctx, "author merge: book not moved", "book", book.ID, "from_author", mergeID, "err", merr)
				out.Errors = append(out.Errors, fmt.Sprintf("book %s: not moved off author %d: %v", book.ID, mergeID, merr))
				continue
			}
			if moved.PrimaryErr != nil {
				// The credits moved but the primary write failed: the book still
				// names the merged author, so it is not deleted.
				moveFailures++
				logging.Warn(ctx, "author merge failed to sync denormalized AuthorID on book", "book", book.ID, "err", moved.PrimaryErr)
			}
			for _, p := range moved.Problems {
				out.Errors = append(out.Errors, fmt.Sprintf("book %s: %s", book.ID, p))
			}
		}

		// The loop above skips a book on any read or write failure, so
		// re-check before the delete erases whatever it could not move.
		switch {
		case lockedBooks > 0 || unreadable > 0:
			out.Errors = append(out.Errors, fmt.Sprintf(
				"author %d NOT deleted: %d book(s) have a user-locked author name and %d have a lock that could not be read; they keep it",
				mergeID, lockedBooks, unreadable))
		case spec.DryRun:
			// Nothing was moved, so the unlinked re-check below would always refuse;
			// the lock check above was the only thing that could hold this author.
			out.Merged++
			out.MergedIDs[mergeID] = true
		default:
			if moveFailures > 0 {
				out.Errors = append(out.Errors, fmt.Sprintf("author %d NOT deleted: %d book(s) could not be moved", mergeID, moveFailures))
			} else if err := database.VerifyAuthorUnlinked(store, mergeID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("author %d NOT deleted: %v", mergeID, err))
			} else if mergeFinish(ctx, store, opID, mergeAuthor, keepAuthor, &out) {
				out.Merged++
				out.MergedIDs[mergeID] = true
			}
		}

		if step != nil {
			step(i+1, len(spec.MergeIDs))
		}
	}
	return out, false
}

// scanStandDownLostMessage is what a merge says when its stand-down lease lapsed
// after done of total authors.
func scanStandDownLostMessage(done, total int) string {
	return fmt.Sprintf(
		"scan stand-down lease lost after %d/%d authors; aborting the remaining merges before the library scanner resumes",
		done, total)
}

// holdAuthorMergeStandDown stands the library scanner down for an author-merge
// apply and returns the release to defer. It is the series merge's gate
// (dedup.MergeSeriesOptions.Scan) for the author merge loop, and fails CLOSED the
// same way: without an op id the undo-ledger rows would be unattributable and the
// gate has no holder key, and a scan that will not park refuses the run, so the
// caller writes nothing in either case. The release is always safe to call.
//
// The ops' ConcurrencyKey stays "entities.author-merge", and library.scan's key is
// not touched: the stand-down is the registry's separate interlock against it.
func (s *Server) holdAuthorMergeStandDown(ctx context.Context, opID, reason string) (release func(), err error) {
	if opID == "" {
		return nil, fmt.Errorf(
			"refusing to apply without an operation id: its undo-ledger rows would be unattributable and the scan stand-down is keyed by it")
	}
	release, aerr := s.scanGate().AcquireScanStandDown(ctx, opID, reason)
	if aerr != nil {
		return nil, fmt.Errorf("refusing to apply while a library.scan is running or queued: %w", aerr)
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

// mergeFinish journals and merge-deletes one author whose books are all moved,
// and reports whether the author is gone. Its failures are appended to out.
//
// database.MergeDeleteAuthor deletes the author, moves its aliases (pen names)
// onto the keep author under their own ids, and writes the tombstone, in one
// batch: every merge path goes through it, so a pen name of a merged author keeps
// resolving, to the keep author. The row is journaled BEFORE the delete from a
// read of the same facts, then rewritten with what the delete reported under its
// locks (the stored-credits pattern above), so the undo moves back exactly the
// aliases that moved and treats the name entry as the delete found it.
func mergeFinish(ctx context.Context, store authorMergeStore, opID string, from, keep *database.Author, out *authorMergeOutcome) bool {
	del, err := authormerge.MergeDelete(store, opID, *from, *keep)
	if err != nil {
		logging.Warn(ctx, "author merge: the author is not deleted", "author", from.ID, "err", err)
		out.Errors = append(out.Errors, fmt.Sprintf("author %d NOT deleted: %v", from.ID, err))
		return false
	}
	for _, p := range del.Problems {
		out.Errors = append(out.Errors, fmt.Sprintf("author %d: %s", from.ID, p))
	}
	return true
}

// AuthorMergeFailedIDsPrefix opens the list of author ids a failed
// entities.author-merge run did not merge: "<prefix>12 15] of ...". The web lane
// (useAuthorsLane) parses it; change both together.
const AuthorMergeFailedIDsPrefix = "author merge left author ids ["

// unmergedAuthorIDs lists, in request order, the ids that were asked for and
// are still there: not the kept id, not already merged.
func unmergedAuthorIDs(mergeIDs []int, keepID int, mergedIDs map[int]bool) []int {
	var failed []int
	for _, id := range mergeIDs {
		if id != keepID && !mergedIDs[id] {
			failed = append(failed, id)
		}
	}
	return failed
}

func joinInts(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, " ")
}

// RegisterResolveProductionAuthorOp registers the
// "entities.resolve-production-author" v2 OperationDef.
func (s *Server) RegisterResolveProductionAuthorOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "entities.resolve-production-author",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "entities",
		DisplayName:     "Resolve Production Author",
		Description:     "Attempt to discover real authors for books attributed to a production company via metadata lookups and AI cover analysis.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// RESUME AUDIT 2026-09-11 (b): ResumeDrop, was ResumeRestart with no
		// checkpoint. The work list is derived at run time (every book still
		// attributed to the production company), so a restart naturally skips
		// the books an earlier attempt resolved — but it re-issues a metadata
		// lookup and a paid AI cover call for every book that failed last time,
		// against daily provider quotas, with nobody having asked for the
		// second pass. There is no stable item list to checkpoint. Drop, and
		// let the user re-run against whatever is left.
		ResumePolicy:   opsregistry.ResumeDrop,
		ConcurrencyKey: "entities.resolve-production-author",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p resolveProductionAuthorOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("resolve-production-author: decode params: %w", err)
				}
			}

			store := s.Ops()
			authorID := p.AuthorID
			prodAuthorName := p.ProdAuthorName

			progress := registryProgressAdapter{r: reporter}

			books, err := store.GetBooksByAuthorIDWithRoleCore(authorID)
			if err != nil {
				return fmt.Errorf("failed to get books: %w", err)
			}
			_ = progress.Log("info", fmt.Sprintf("Resolving %d books for production company %q", len(books), prodAuthorName), nil)

			resolved := 0
			failed := 0
			hydrateSkipped := 0
			for i, book := range books {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				_ = progress.UpdateProgress(i, len(books), fmt.Sprintf("Processing %d/%d: %s", i+1, len(books), book.Title))

				// Try metadata fetch by title only
				resp, fetchErr := s.metadataFetchService.FetchMetadataForBookByTitle(ctx, book.ID)
				if fetchErr == nil && resp != nil && resp.Book != nil && resp.Book.AuthorID != nil {
					// Check if the found author is different from the production company
					newAuthor, _ := store.GetAuthorByID(*resp.Book.AuthorID)
					if newAuthor != nil && !dedup.IsProductionCompany(newAuthor.Name) {
						_ = progress.Log("info", fmt.Sprintf("Resolved %q → author %q (source: %s)", book.Title, newAuthor.Name, resp.Source), nil)
						// Reclassify production company as publisher. Hydrate the
						// full stored row before the write (see
						// assignPublisherPreservingRecord) so the full-replace
						// UpdateBook cannot wipe Title/FilePath/ratings/etc.
						if book.Publisher == nil || *book.Publisher == "" {
							if err := assignPublisherPreservingRecord(store, book.ID, prodAuthorName); err != nil {
								_ = progress.Log("warn", fmt.Sprintf("Skipped publisher reclassify for %q (hydrate failed, record left intact): %v", book.Title, err), nil)
								hydrateSkipped++
							}
						}
						resolved++
						continue
					}
				}

				// If metadata failed and AI is enabled, try cover art analysis
				aiParser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
				if aiParser.IsEnabled() && book.FilePath != "" {
					imgData, mime, imgErr := metadata.ExtractCoverArtBytes(book.FilePath)
					if imgErr == nil && len(imgData) > 0 {
						parsed, aiErr := aiParser.ParseCoverArt(ctx, imgData, mime)
						if aiErr == nil && parsed != nil && parsed.Author != "" && parsed.Confidence != "low" {
							_ = progress.Log("info", fmt.Sprintf("AI cover analysis for %q found author: %q (confidence: %s)", book.Title, parsed.Author, parsed.Confidence), nil)
							// Look up or create the discovered author. The
							// shared creation gate runs first: a junk AI
							// answer is no answer, and an existing junk row
							// must not be linked either.
							var existing *database.Author
							if aiAuthor, why := personname.PrepareAuthorNameForCreation(parsed.Author); why == "" {
								existing, _ = store.GetAuthorByName(aiAuthor)
								if existing == nil {
									existing, _ = store.CreateAuthor(aiAuthor)
								}
							}
							if existing != nil {
								// Assign the discovered author. Hydrate the full
								// stored row before the AuthorID write (see
								// assignResolvedAuthorPreservingRecord) so the
								// full-replace UpdateBook cannot wipe the record.
								// Fail-closed: on hydrate error nothing is written
								// (neither the Book row nor the join), leaving the
								// book consistently attributed to the production
								// company rather than half-resolved or wiped.
								if err := assignResolvedAuthorPreservingRecord(store, book.ID, existing.ID, authorID); err != nil {
									_ = progress.Log("warn", fmt.Sprintf("Skipped author resolve for %q (hydrate failed, record left intact): %v", book.Title, err), nil)
									hydrateSkipped++
									failed++
									continue
								}
								resolved++
								continue
							}
						}
					}
				}

				failed++
				_ = progress.Log("debug", fmt.Sprintf("Could not resolve author for %q", book.Title), nil)
			}

			if s.dedupCache != nil {
				s.dedupCache.Invalidate("author-duplicates")
			}
			s.authorsCache.InvalidateAll()

			resultMsg := fmt.Sprintf("Resolved %d/%d books for %q (%d unresolved, %d skipped on hydrate error)", resolved, len(books), prodAuthorName, failed, hydrateSkipped)
			_ = progress.Log("info", resultMsg, nil)
			_ = progress.UpdateProgress(len(books), len(books), resultMsg)
			return nil
		},
	})
}

// assignPublisherPreservingRecord reclassifies a production company as the
// book's Publisher WITHOUT wiping the rest of the stored record.
//
// database.Store.UpdateBook is a FULL-REPLACE write: it marshals the whole Book
// passed to it (only seven heavy Description/BookSig* fields are restored from
// the old row). Passing a near-empty literal such as &database.Book{Publisher:…}
// therefore wipes Title, FilePath (which also corrupts the book:path: index),
// AuthorID, ratings, media-info, and everything else. This helper instead goes
// through ModifyBook, which re-reads the current full row under the book's
// write lock and sets only Publisher, so every other field survives the write
// and a column another writer commits meanwhile is not reverted (audit A1#15).
//
// Fail-closed: if the row cannot be read it returns the error and writes
// NOTHING, and a book that no longer exists is reported as not found. A skipped
// publisher tag is far better than a wiped record; the caller counts the skip
// and moves on to the next book.
func assignPublisherPreservingRecord(store entityAssignStore, bookID, publisher string) error {
	written, err := store.ModifyBook(bookID, func(full *database.Book) error {
		pub := publisher
		full.Publisher = &pub
		return nil
	})
	if err != nil {
		return fmt.Errorf("update book %s publisher: %w", bookID, err)
	}
	if written == nil {
		return fmt.Errorf("hydrate book %s before publisher write: not found", bookID)
	}
	return nil
}

// assignResolvedAuthorPreservingRecord points a book at a newly discovered
// author: it sets the denormalized Book.AuthorID and rewrites the book_authors
// join to drop the production-company author and add the resolved one — WITHOUT
// wiping the rest of the stored record (see assignPublisherPreservingRecord for
// why a bare UpdateBook literal is catastrophic).
//
// Fail-closed: if the row cannot be read or no longer exists it returns the
// error before touching anything, so neither the Book row nor the join is
// written and the book stays consistently attributed to the production company
// (unresolved) rather than half-applied or wiped. The AuthorID write goes
// through ModifyBook, which sets only that column under the book's write lock
// (audit A1#15). The join rewrite itself is best-effort: an error there is
// logged by the caller path but does not roll back the AuthorID write (matching
// prior behavior).
func assignResolvedAuthorPreservingRecord(store authorAssignStore, bookID string, resolvedAuthorID, prodAuthorID int) error {
	written, err := store.ModifyBook(bookID, func(full *database.Book) error {
		aid := resolvedAuthorID
		full.AuthorID = &aid
		return nil
	})
	if err != nil {
		return fmt.Errorf("update book %s author: %w", bookID, err)
	}
	if written == nil {
		return fmt.Errorf("hydrate book %s before author write: not found", bookID)
	}

	// Rewrite the book_authors join: drop the production-company author, add the
	// resolved author. Best-effort, as before — a failure here is logged but does
	// not fail the resolution (the denormalized AuthorID already points correctly).
	//
	// The resolved author takes the production company's slot (its position
	// and role) rather than being appended at position 0, which collided
	// with whatever credit already held position 0; a book already crediting
	// the resolved author keeps that one credit.
	bookAuthors, _ := store.GetBookAuthors(bookID)
	updated := replaceAuthorCredit(bookID, bookAuthors, prodAuthorID, resolvedAuthorID)
	if err := store.SetBookAuthors(bookID, updated); err != nil {
		slog.Warn("resolve-production-author: failed to rewrite book_authors join", "book", bookID, "err", err)
	}
	return nil
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterResolveProductionAuthorOp(reg) })
}

// replaceAuthorCredit returns credits with every credit of fromID removed and
// toID credited once, in the first removed credit's position and role ("author"
// when that role is empty), or after the last position when fromID was not
// credited. A credit of toID already present is kept where it is.
func replaceAuthorCredit(bookID string, credits []database.BookAuthor, fromID, toID int) []database.BookAuthor {
	has := false
	maxPos := -1
	for _, ba := range credits {
		has = has || ba.AuthorID == toID
		if ba.Position > maxPos {
			maxPos = ba.Position
		}
	}
	var out []database.BookAuthor
	placed := has
	for _, ba := range credits {
		if ba.AuthorID != fromID {
			out = append(out, ba)
			continue
		}
		if !placed {
			placed = true
			role := ba.Role
			if role == "" {
				role = "author"
			}
			out = append(out, database.BookAuthor{BookID: bookID, AuthorID: toID, Role: role, Position: ba.Position})
		}
	}
	if !placed {
		out = append(out, database.BookAuthor{BookID: bookID, AuthorID: toID, Role: "author", Position: maxPos + 1})
	}
	return out
}
