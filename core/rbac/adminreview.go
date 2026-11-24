// file: core/rbac/adminreview.go
// version: 0.1.0
// guid: f5015a54-bbcc-4964-a68d-06c2b207ba38
// last-edited: 2026-11-21

package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// ErrNotInAdminReview is AcknowledgeAdminReview of an account the stored restore
// report does not list for review: one that never was, one already
// acknowledged, or no report at all. It is not a statement about the account
// itself (it may no longer exist, or no longer hold core:admin); the report is
// the only thing asked.
var ErrNotInAdminReview = errors.New("rbac: the account is not listed for admin review")

// ReviewStore is the stored restore report's review list, which rbac's restore
// hook filled (05-rbac 7.1) and the kernel's core.backup probe reads
// (05-health 5H.3.1). *journal.ReviewAcks is the one implementation: the record
// and its format are backup's, the rule that clears an entry is rbac's.
type ReviewStore interface {
	// Acknowledge removes userID from the review list, calling stage with the
	// removal's batch so that what it stages commits with it or not at all. It
	// returns journal.ErrNotInReview for an account not listed.
	Acknowledge(ctx context.Context, userID string, stage func(*storage.Batch) error) error
}

var _ ReviewStore = (*journal.ReviewAcks)(nil)

// AdminReview acknowledges the core:admin holders a restore with no pre-swap
// store listed for review (05-rbac 7.1: "health reports degraded
// (restore_admins_review) naming them until an interactive core:admin session
// acknowledges each one (audited core.role.review_admin)"). It is a separate
// type from Service because it never changes a role or an assignment: Service's
// write path bumps the role generation and publishes the role-changed event,
// and an acknowledgement is neither.
type AdminReview struct {
	store ReviewStore
	audit audit.Logger
}

// NewAdminReview builds the acknowledgement over store, recording each one to
// log in the batch that removes the entry.
func NewAdminReview(store ReviewStore, log audit.Logger) (*AdminReview, error) {
	switch {
	case store == nil:
		return nil, errors.New("rbac: NewAdminReview needs a ReviewStore")
	case log == nil:
		return nil, errors.New("rbac: NewAdminReview needs an audit.Logger")
	}
	return &AdminReview{store: store, audit: log}, nil
}

// AcknowledgeAdminReview removes userID from the restore report's review list
// and audits core.role.review_admin, both in one commit. The entry is read
// again when the removal is made (ReviewStore.Acknowledge holds the record
// exclusively from the read to the commit), so two acknowledgements of one
// account record one audit row and the second is ErrNotInAdminReview.
//
// actor must hold core:admin (ErrDenied otherwise). Whether the credential is
// interactive is the caller's to decide: a Checker does not know it, and
// core:admin is not Sensitive, so a non-interactive principal holding it passes
// this check; rbachttp's route carries Guards.Interactive and its handler
// refuses a non-interactive principal itself. The actor is not barred from
// acknowledging itself: the only administrator of an install has nobody else to
// do it, and the audit record names who acknowledged whom.
//
// userID is looked up in the report alone, never in the user store: an account
// deleted since the restore must still be acknowledgeable, or its entry could
// never clear. The audit actor comes from ctx (audit.WithActor), as every other
// rbac record's does; the record names the account as its target and carries no
// credential or secret.
func (r *AdminReview) AcknowledgeAdminReview(ctx context.Context, actor Checker, userID string) error {
	if actor == nil || !actor.Has(CoreAdmin) || !actor.Satisfies(Need(CoreAdmin)) {
		return fmt.Errorf("%w: acknowledging an admin review needs %s", ErrDenied, CoreAdmin)
	}
	err := r.store.Acknowledge(ctx, userID, func(b *storage.Batch) error {
		// A string-literal Action, so audit's TestAuditAction_Grammar sees it.
		if err := r.audit.RecordIn(ctx, b, audit.Event{
			Action: "core.role.review_admin", Outcome: audit.Success,
			Target: entity.Ref{Module: "core", Kind: "user", ID: userID},
		}); err != nil {
			return fmt.Errorf("rbac: audit core.role.review_admin: %w", err)
		}
		return nil
	})
	if errors.Is(err, journal.ErrNotInReview) {
		return ErrNotInAdminReview
	}
	return err
}
