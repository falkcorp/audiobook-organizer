// file: internal/plugins/maintenance/regroup_apply.go
// version: 1.0.0
// guid: e2a7c9d4-1f68-4b03-9c5e-7a0d3f814b62
// last-edited: 2026-07-14

// Package maintenance — the APPLY path for the regroup review queue (PR-B2).
//
// B1 (regroup_shattered_ai.go) is a dry-run producer: it writes one review-queue
// HOLD per candidate folder and touches ZERO books. B2 is what makes the "Approve"
// button in the review UI actually merge. It supplies one apply function per
// CONFIDENT regroup Kind; the review handler dispatches on ReviewItem.Kind and, on
// a nil error, transitions the item to "applied" (handler.go:approveOne).
//
// Only the two confident kinds get an apply function:
//   - regroup.multidisc     → collapse N single-file books into 1 (CombineBooks).
//   - regroup.version-group → share a VersionGroupID + designate one primary, via
//     UpdateBook (NOT MergeBooks — both editions must stay visible; locked #8).
//
// regroup.anthology and regroup.ambiguous are deliberately handler-less: they need
// human sub-decisions (which files are distinct works, how to split) that a single
// apply function cannot make, so approving one only marks it "approved".
//
// DATA-LOSS SAFETY (this repo's dominant incident class is write-back wipes):
//   - Multidisc uses CombineBooks(ids, primary, nil) — a NIL override. The only
//     UpdateBook-on-survivor in merge.Service is gated behind a non-nil override, so
//     with nil the survivor row is never rewritten and its AcoustIDFingerprint /
//     Author / Series survive. Absorbed books' FILES move by file ID (fingerprints
//     ride along); absorbed ROWS are hard-deleted intentionally.
//   - Version-group uses re-fetch-and-patch: GetBookByID returns the FULL row, we
//     mutate ONLY VersionGroupID, then UpdateBook. Never construct a fresh/partial
//     Book and write it back — UpdateBook is a full-column replace.
// Both paths are covered by regroup_apply_test.go's invariant assertions.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// bookCombiner is the slim slice of merge.Service the multidisc apply path needs.
// Narrowing to an interface keeps the apply function unit-testable without a full
// merge service while the real wiring passes *merge.Service.
type bookCombiner interface {
	CombineBooks(bookIDs []string, primaryID string, override *merge.CombineOverride) (*merge.CombineResult, error)
}

// ApplyMultidisc builds the apply function for regroup.multidisc holds: it collapses
// the folder's single-file books into ONE multi-file book via CombineBooks.
//
// The returned func matches reviewhandler.ApplyFunc structurally (an unnamed func
// type is assignable to the named type), so the wiring registers it directly without
// this package importing the server handler.
func ApplyMultidisc(store database.Store, combiner bookCombiner) func(context.Context, database.ReviewItem) error {
	return func(ctx context.Context, item database.ReviewItem) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := decodeRegroupPayload(item)
		if err != nil {
			return err
		}
		// Retry tolerance: only combine members that still resolve. A double-approve,
		// or a re-approve after a partial failure, must not error on already-absorbed
		// (hard-deleted) rows.
		present, err := presentMembers(store, p.MemberBookIDs)
		if err != nil {
			return err
		}
		if len(present) < 2 {
			slog.Info("regroup multidisc apply: <2 members remain — already applied, no-op",
				"item", item.ID, "folder", p.Folder, "present", len(present))
			return nil
		}
		primaryID := pickPrimary(present)
		res, err := combiner.CombineBooks(present, primaryID, nil) // nil override = survivor metadata untouched
		if err != nil {
			return fmt.Errorf("regroup multidisc apply: combine %q (%d books): %w", p.Folder, len(present), err)
		}
		slog.Info("regroup multidisc apply: collapsed folder",
			"item", item.ID, "folder", p.Folder, "survivor", res.PrimaryID,
			"files_moved", res.FilesMoved, "books_deleted", res.BooksDeleted)
		return nil
	}
}

// ApplyVersionGroup builds the apply function for regroup.version-group holds: it
// links the folder's members (e.g. an Abridged + an Unabridged edition) into one
// version group by giving them a shared VersionGroupID and designating one primary.
//
// LOCKED decision #8 (plan §B3): this uses UpdateBook, NOT merge.MergeBooks —
// MergeBooks soft-deletes the loser, but a version group must keep BOTH editions
// visible (the user picks which to play). The group-ID-reuse mirrors
// merge/service.go: reuse an existing VersionGroupID if any member already has one.
func ApplyVersionGroup(store database.Store) func(context.Context, database.ReviewItem) error {
	return func(ctx context.Context, item database.ReviewItem) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := decodeRegroupPayload(item)
		if err != nil {
			return err
		}
		// Resolve all members up front (atomic-validate-then-write). Prefer an
		// existing VersionGroupID for idempotency — a re-approve then re-links to the
		// same group instead of minting a fresh one every time.
		books := make([]*database.Book, 0, len(p.MemberBookIDs))
		target := ""
		for _, id := range p.MemberBookIDs {
			b, err := store.GetBookByID(id)
			if err != nil {
				return fmt.Errorf("regroup version-group apply: lookup %s: %w", id, err)
			}
			if b == nil {
				continue // tolerate a vanished member
			}
			books = append(books, b)
			if b.VersionGroupID != nil && *b.VersionGroupID != "" {
				if target == "" || *b.VersionGroupID < target {
					target = *b.VersionGroupID
				}
			}
		}
		if len(books) < 2 {
			slog.Info("regroup version-group apply: <2 members remain — nothing to group",
				"item", item.ID, "folder", p.Folder, "present", len(books))
			return nil
		}
		if target == "" {
			target = ulid.Make().String()
		}
		// Designate a single primary deterministically: the smallest ULID (earliest-
		// created), consistent with pickPrimary and stable across retries. The plan
		// prefers the Unabridged edition, but that signal is not in the payload; a
		// deterministic primary is the safe fallback. BOTH editions stay visible — we
		// never soft-delete here.
		primaryID := books[0].ID
		for _, b := range books[1:] {
			if b.ID < primaryID {
				primaryID = b.ID
			}
		}
		// Re-fetch-and-patch: mutate ONLY VersionGroupID + IsPrimaryVersion on the
		// full fetched row (UpdateBook is a full-column replace — never write back a
		// fresh/partial Book). Skip a write only when both already match (idempotent).
		for _, b := range books {
			vg := target
			isPrimary := b.ID == primaryID
			if b.VersionGroupID != nil && *b.VersionGroupID == target &&
				b.IsPrimaryVersion != nil && *b.IsPrimaryVersion == isPrimary {
				continue
			}
			b.VersionGroupID = &vg
			b.IsPrimaryVersion = &isPrimary
			if _, err := store.UpdateBook(b.ID, b); err != nil {
				return fmt.Errorf("regroup version-group apply: set version group on %s: %w", b.ID, err)
			}
		}
		slog.Info("regroup version-group apply: linked members",
			"item", item.ID, "folder", p.Folder, "version_group", target,
			"primary", primaryID, "members", len(books))
		return nil
	}
}

// decodeRegroupPayload unmarshals the hold's JSON payload (the shape the producer
// wrote in buildRegroupPayload).
func decodeRegroupPayload(item database.ReviewItem) (regroupPayload, error) {
	var p regroupPayload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		return regroupPayload{}, fmt.Errorf("regroup apply: decode payload for item %s: %w", item.ID, err)
	}
	return p, nil
}

// presentMembers returns the subset of ids whose Book rows still resolve, preserving
// order. A read error is fatal (aborts before any mutation); a not-found is skipped.
func presentMembers(store database.Store, ids []string) ([]string, error) {
	present := make([]string, 0, len(ids))
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("regroup apply: lookup %s: %w", id, err)
		}
		if b != nil {
			present = append(present, id)
		}
	}
	return present, nil
}

// pickPrimary chooses the survivor deterministically: the lexicographically smallest
// member ID. Member IDs are ULIDs (time-sortable), so the smallest is the earliest-
// created book — a stable, sensible canonical survivor that is identical across
// retries (which the idempotency/partial-failure story relies on).
func pickPrimary(ids []string) string {
	primary := ids[0]
	for _, id := range ids[1:] {
		if id < primary {
			primary = id
		}
	}
	return primary
}
