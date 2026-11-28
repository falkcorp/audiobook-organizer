// file: core/app/offline_accounts.go
// version: 1.0.0
// guid: 8efb209c-c179-485c-84e9-2a5c130b137b
// last-edited: 2026-11-28

package app

import (
	"context"
	"iter"

	"github.com/falkcorp/mediacore/auth"
)

// Users is every account core's identity store holds, read from the offline
// open (kernel.Offline.Users): filtered as auth.Service.Users filters, read
// only, with no part of the identity boot run.
func (o *Offline) Users(ctx context.Context, f auth.UserFilter) iter.Seq2[*auth.User, error] {
	return o.o.Users(ctx, f)
}

// DataMigrationDone is whether the data migration id has completed on the
// open store (kernel.Offline.DataMigrationDone). It reads and never writes.
func (o *Offline) DataMigrationDone(ctx context.Context, id string) (bool, error) {
	return o.o.DataMigrationDone(ctx, id)
}
