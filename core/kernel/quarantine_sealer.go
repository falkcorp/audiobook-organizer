// file: core/kernel/quarantine_sealer.go
// version: 1.0.0
// guid: 2f34c62b-ae1a-41b9-9d31-790f8b0fb3fd
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"errors"

	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// quarantineActor is the By of every sealed quarantine record in the secret
// store's history.
const quarantineActor = "core:storage-quarantine"

// quarantineSealer is storage.QuarantineSealer over the kernel's secret store
// (05-storage 5S.3.13 "Quarantine"): storage (L1) cannot import secrets (L2),
// so this adapter seals a sensitive row's raw bytes through
// secrets.Importer.ImportSecret into the chunk's batch, where the sealed record
// and the source row's delete commit together or not at all.
//
// It is late-bound: the migration list is built at P2 (dataSteps) and the
// secret store opens at the start of P4b (bindCoreStores), so the sealer reads
// Kernel.sec when a chunk quarantines, and fails closed, writing nothing, if no
// store is open. Chunks run concurrently; Kernel.sec is set before the first
// step and not changed until close, and ImportSecret locks per name.
//
// A sensitive row with no bytes cannot be sealed (a secret write carries a
// value) and fails the chunk with the importer's error: it is kept, not
// dropped, as every quarantine failure is.
type quarantineSealer struct{ k *Kernel }

func (q quarantineSealer) Seal(ctx context.Context, b *storage.Batch, name string, raw []byte) error {
	ks := q.k.sec
	if ks == nil {
		return errors.New("kernel: quarantine: the secret store is not open, so a sensitive row cannot be sealed")
	}
	return ks.store.Importer().ImportSecret(ctx, b, secrets.Write{
		Name: secrets.Name(name), Value: secrets.NewValue(string(raw)), By: quarantineActor,
	})
}
