// file: internal/books/store/user_merge_identity.go
// version: 0.1.0
// guid: 8e3b6d14-7a92-4c05-b1f8-2d9c4a7e5b63
// last-edited: 2026-11-21

package store

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
)

// ErrUserMergeNeedsCoreBatch is what ApplyUserMergeIdentity returns on a store
// whose batches are not core storage batches (the raw engine): the merge's auth
// half joins the same batch as books' rows, and only a core batch can carry it.
var ErrUserMergeNeedsCoreBatch = errors.New("database: the identity user merge needs a store opened by core storage")

// IdentityUserMerger is the storage capability behind the admin user merge once
// accounts live in core (F4-08 B10l). Not on Store; resolve it with
// AsCapability.
type IdentityUserMerger interface {
	// ScanBooksUserData reads the rows userID owns in the families books owns
	// (see PebbleStore.ScanBooksUserData); the core-owned fields stay empty.
	ScanBooksUserData(userID string) (*UserData, error)
	// ApplyUserMergeIdentity is ApplyUserMergeOps for a merge that must commit
	// together with another owner's writes. Under the same mutex as
	// ApplyUserMergeOps it checks every op against the row it was planned from
	// (HEAD's hash compare, strict: no op may find its row already rewritten,
	// because an identity-era merge is one batch and has no resumed chunk),
	// stages every op and the journal into one core batch, calls stage with that
	// batch so the other owner stages its writes beside them, then commits the
	// batch once. A failed op check, a failed stage or a failed commit leaves
	// every row as it was: nothing is committed unless all of it is. The caller
	// holds whatever locks make the compare meaningful.
	ApplyUserMergeIdentity(ops []UserMergeOp, journalKey string, journal []byte, stage func(b *storage.Batch) error) error
}

var _ IdentityUserMerger = (*PebbleStore)(nil)

// ApplyUserMergeIdentity implements IdentityUserMerger.
func (p *PebbleStore) ApplyUserMergeIdentity(ops []UserMergeOp, journalKey string, journal []byte, stage func(b *storage.Batch) error) error {
	if stage == nil {
		return errors.New("user merge: no staging step for the other owner's half")
	}
	userMergeApplyMu.Lock()
	defer userMergeApplyMu.Unlock()
	b := p.db.NewBatch()
	defer func() { _ = b.Close() }()
	if b.lb == nil {
		return ErrUserMergeNeedsCoreBatch
	}
	for i := range ops {
		op := &ops[i]
		conflict := &UserMergeConflictError{Key: op.Key, Family: op.Family, Index: i}
		cur, err := p.GetRaw(op.Key)
		if err != nil {
			return err
		}
		if op.IsOwnerOp() {
			if op.OwnerState(cur) != "from" {
				return conflict
			}
			out, _, err := RewriteJSONField(cur, op.OwnerField, op.OwnerTo)
			if err != nil {
				return conflict
			}
			if err := b.Set([]byte(op.Key), out, nil); err != nil {
				return err
			}
			continue
		}
		if UserMergeValueHash(cur, cur != nil) != op.BeforeHash() {
			return conflict
		}
		if op.Delete {
			err = b.Delete([]byte(op.Key), nil)
		} else {
			err = b.Set([]byte(op.Key), op.After, nil)
		}
		if err != nil {
			return err
		}
	}
	if journalKey != "" {
		if err := b.Set([]byte(journalKey), journal, nil); err != nil {
			return err
		}
	}
	if err := stage(b.lb); err != nil {
		return err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit the user merge batch: %w", err)
	}
	return nil
}
