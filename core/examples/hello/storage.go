// file: core/examples/hello/storage.go
// version: 1.0.0
// guid: e0be6b1e-382c-4ec6-8321-ac03847c4c5f
// last-edited: 2026-11-07

package hello

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
)

// storageSpec is hello's storage declaration (06 §6.12.1): its keyspace
// m/hello/ and one migration.
func storageSpec() storage.Spec {
	return storage.Spec{
		Keyspace: true, // m/hello/
		Migrations: []storage.Migration{
			// Seed: runs on a fresh install too, with every later step of
			// hello; a step below it would be stamped applied there without
			// running (COR5-8, COR6-4).
			{Version: 1, Name: "seed-names", Compat: storage.Additive, Seed: true, Up: seedNames},
		},
	}
}

// seedNames writes the first name so the count task has something to count.
// Keys are relative to m/hello/; "name/world" is the key the service reads as
// Keyspace.Sub("name") + "world". A seed must leave rows that already exist
// unchanged (05-storage Migration.Seed), so it writes only an absent key and
// never resets a count (COR6-4).
func seedNames(ctx context.Context, tx *storage.MigrationTx) error {
	key := []byte("name/world")
	_, err := tx.Keyspace().Get(ctx, key)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("hello: seed: %w", err)
	}
	if err := tx.Set(key, []byte("1")); err != nil {
		return fmt.Errorf("hello: seed: %w", err)
	}
	return nil
}
