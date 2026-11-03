// file: internal/repairs/owner_ctx.go
// version: 1.0.0
// guid: abcb13e8-3c0e-45e1-87bc-da22e119a876
// last-edited: 2026-11-03

package repairs

import "context"

// The owner-apply marker: applyOne puts it on the ctx it hands a fixer's
// Replan and Apply, for the one row the owner's click named. A fixer that
// writes a row it lists as skipped (a row held back from a plain apply that
// the owner may apply himself) writes it only when OwnerApplyFor names that
// row AND its own re-plan still marks the row owner-applicable. A plain
// apply, a retry and a bulk selection never carry the marker, so a fixer
// that checks it fails closed.
//
// (Removed in 0e7e60346 with its last reader, the fragment fixer's owner
// rows; the folder-books fixer is its reader again.)

type ownerApplyKey struct{}

// WithOwnerApply marks ctx as the owner's apply of the row rowID.
func WithOwnerApply(ctx context.Context, rowID string) context.Context {
	return context.WithValue(ctx, ownerApplyKey{}, rowID)
}

// OwnerApplyFor reports whether ctx carries the owner's approval of exactly
// the row rowID. An empty rowID, a nil ctx and a marker for another row are
// all false.
func OwnerApplyFor(ctx context.Context, rowID string) bool {
	if ctx == nil || rowID == "" {
		return false
	}
	id, _ := ctx.Value(ownerApplyKey{}).(string)
	return id == rowID
}
