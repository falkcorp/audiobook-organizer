// file: core/kernel/offline_accounts.go
// version: 1.0.0
// guid: badf1c46-61fc-4f46-991b-6984ed75be07
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/authstore"
)

// Users is every account core's identity store holds, read from an offline
// open: the stored user rows in ID order, filtered by f through
// auth.FilterUsers, the rule auth.Service.Users lists through (an empty
// filter yields every account, every status and the system user included). An undecodable row yields its
// error and the scan goes on, as the serve path's does; a store that cannot
// be read yields one error.
//
// It reads and never writes: it opens the identity keyspace as it is and runs
// no part of the identity boot (no core config load, no seeds, no boot
// writes), so on a read-only open (OpenOffline readOnly) nothing below it can
// write either. A diagnostic that must count core's accounts without starting
// the service reads through it.
func (o *Offline) Users(ctx context.Context, f auth.UserFilter) iter.Seq2[*auth.User, error] {
	aks, ok := o.db.Keyspace(authstore.Owner)
	if !ok {
		return failedUsers(fmt.Errorf("%w: kernel: offline open: no %s keyspace in the store", auth.ErrStoreUnavailable, authstore.Owner))
	}
	st, err := authstore.New(aks, authstore.Options{})
	if err != nil {
		return failedUsers(fmt.Errorf("%w: kernel: offline open: auth store: %w", auth.ErrStoreUnavailable, err))
	}
	return auth.FilterUsers(st.ScanUsers(ctx), f)
}

// failedUsers is an account list that yields err alone.
func failedUsers(err error) iter.Seq2[*auth.User, error] {
	return func(yield func(*auth.User, error) bool) { yield(nil, err) }
}

// DataMigrationDone is whether the data migration id has completed on the
// open store: the storage admin's answer, the one read a serve boot's
// readiness checks make. It reads and never writes.
func (o *Offline) DataMigrationDone(ctx context.Context, id string) (bool, error) {
	return o.db.Admin().DataMigrationDone(ctx, id)
}
