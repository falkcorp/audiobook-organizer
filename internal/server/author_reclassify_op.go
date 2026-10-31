// file: internal/server/author_reclassify_op.go
// version: 1.0.0
// guid: 40643c5c-2000-4271-813a-297ab2aa23e1
// last-edited: 2026-10-30

// author_reclassify_op registers entities.author-reclassify: the op behind the
// Authors view's "reclassify as narrator" button (POST
// /authors/:id/reclassify-as-narrator). The route used to run the move inline with
// no operation to journal under, so it deleted the author and moved its credits
// with no way back. Run as an op it journals every write under the op's own id
// (entities.ReclassifyJournal, change types author_reclassify_credits and
// author_reclassify_delete), and the existing revert path undoes it.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
)

// authorReclassifyOpParams is the wire shape, defined once in package entities.
type authorReclassifyOpParams = entities.AuthorReclassifyOpParams

// RegisterAuthorReclassifyOp registers the "entities.author-reclassify" v2 OperationDef.
func (s *Server) RegisterAuthorReclassifyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              entities.AuthorReclassifyOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "entities",
		DisplayName:     "Author Reclassify as Narrator",
		Description:     "Turn an author into a narrator of the same name: every book credited to the author gains a narrator credit and loses the author credit, and the author record is deleted. Undoable.",
		DefaultPriority: opsregistry.PriorityNormal,
		Isolate:         false,
		// Interactive: a person pressed the button and is waiting on it.
		NoWarmupWait: true,
		Timeout:      time.Hour,
		// RESUME AUDIT 2026-10-30: ResumeDrop, as entities.author-merge. A re-run after
		// a restart would find the author gone (a clean 404-shaped failure) or half
		// moved, and the journal would get a second set of rows under the same op id;
		// the interrupted run is surfaced for the person to re-issue deliberately.
		ResumePolicy: opsregistry.ResumeDrop,
		// The SAME key as entities.author-merge and ai.author-scan-apply: all three
		// rewrite book_authors and delete authors, and the registry serializes runs
		// that share a key, so a reclassify cannot interleave with a merge on the
		// same rows.
		ConcurrencyKey: "entities.author-merge",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p authorReclassifyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("author-reclassify: decode params: %w", err)
				}
			}
			progress := registryProgressAdapter{r: reporter}
			opID := opsregistry.ReporterOpID(reporter)
			msg, err := s.runAuthorReclassify(ctx, opID, p.AuthorID)
			if err != nil {
				_ = progress.Log("error", err.Error(), nil)
				return err
			}
			_ = progress.Log("info", msg, nil)
			return nil
		},
	})
}

// runAuthorReclassify is the op's body: hold the scan stand-down, reclassify the
// author journaled under opID, and drop the author caches on EVERY exit after the
// first write (a partial move changes the lists too). It returns the success line.
func (s *Server) runAuthorReclassify(ctx context.Context, opID string, authorID int) (string, error) {
	if authorID <= 0 {
		return "", fmt.Errorf("author-reclassify: invalid author id %d", authorID)
	}
	// Taken before the first write, so a refusal leaves nothing to invalidate.
	release, herr := s.holdReclassifyStandDown(ctx, opID, "author-reclassify")
	if herr != nil {
		return "", fmt.Errorf("author-reclassify: %w", herr)
	}
	defer release()

	store := s.Ops()
	defer func() {
		s.dedupCache.Invalidate("author-duplicates")
		s.authorsCache.InvalidateAll()
	}()
	out, err := entities.ReclassifyAuthorAsNarrator(store, authorID, entities.ReclassifyOptions{
		// Journaled under this operation, so a revert of it undoes the reclassify.
		Journal: &entities.ReclassifyJournal{OperationID: opID, Store: store},
		// No SkipBook: this is a person's explicit action on one author, unlike the
		// AI apply, which leaves a book whose author_name or narrator is locked.
		// Lease renews the stand-down before each book's writes.
		Lease: func() bool { return s.scanGate().RenewScanStandDown(opID) },
	})
	if err != nil {
		return "", reclassifyFailure(authorID, out, err)
	}
	return fmt.Sprintf("Reclassified author %d as narrator %d: %d book(s) updated", authorID, out.NarratorID, out.BooksUpdated), nil
}

// reclassifyFailure words a failed reclassify for the op's error_message, which
// the Authors view shows. The author is only deleted when nothing was left behind.
func reclassifyFailure(authorID int, out entities.ReclassifyOutcome, err error) error {
	detail := ""
	if len(out.Skipped) > 0 {
		detail = ": " + strings.Join(out.Skipped[:min(len(out.Skipped), 3)], "; ")
	}
	switch {
	case errors.Is(err, entities.ErrAuthorNotFound):
		return fmt.Errorf("author-reclassify: author %d no longer exists", authorID)
	case errors.Is(err, database.ErrAuthorStillLinked):
		return fmt.Errorf("author-reclassify: author %d was kept: %d book(s) moved, but some still credit it%s",
			authorID, out.BooksUpdated, detail)
	case errors.Is(err, entities.ErrReclassifyLeaseLost):
		return fmt.Errorf("author-reclassify: %w after %d book(s); the author was kept", err, out.BooksUpdated)
	}
	return fmt.Errorf("author-reclassify: %w%s", err, detail)
}

// holdReclassifyStandDown stands the library scanner down for the reclassify and
// returns the release to defer: a concurrent library.scan rewrites book_authors on
// the rows the reclassify moves. It fails CLOSED, like the series ops: without an op
// id the undo rows would be unattributable and the gate has no holder key, and a
// scan that will not park refuses the run before any write. The release is always
// safe to call.
//
// This duplicates holdAuthorMergeStandDown from PR #4302 (author merge), which was
// not on main when this was written; swap this for that helper once both land.
func (s *Server) holdReclassifyStandDown(ctx context.Context, opID, reason string) (release func(), err error) {
	if opID == "" {
		return nil, fmt.Errorf(
			"refusing to reclassify without an operation id: its undo-ledger rows would be unattributable and the scan stand-down is keyed by it")
	}
	release, aerr := s.scanGate().AcquireScanStandDown(ctx, opID, reason)
	if aerr != nil {
		return nil, fmt.Errorf("refusing to reclassify while a library.scan is running or queued: %w", aerr)
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorReclassifyOp(reg) })
}
