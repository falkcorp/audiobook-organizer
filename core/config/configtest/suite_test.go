// file: core/config/configtest/suite_test.go
// version: 1.0.0
// guid: 065fac7b-d83f-4b3c-beeb-c6d97d36f478
// last-edited: 2026-11-10

package configtest_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/internal/configstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

const auditOwner storage.Owner = "core/audit"

// sections names the section of every fixture key for the committer.
type sections struct{}

func (sections) Lookup(p config.Path) (config.KeyState, bool) {
	for _, sec := range []config.Path{configtest.BatcavePath, configtest.WatchPath} {
		if p == sec || len(p) > len(sec) && p[:len(sec)+1] == sec+"." {
			return config.KeyState{Path: p, Section: sec}, true
		}
	}
	return config.KeyState{}, false
}

// nativeHarness boots a fresh in-memory store and returns the suite's view of
// the native override store over it. The package's own tests are the only
// ones that credit configtest's coverage, so the suite runs here as well as in
// internal/configstore, where it is the store's contract test.
func nativeHarness(t *testing.T) configtest.Harness {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			configstore.OwnerSpec(),
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "configtest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(configstore.Owner)
	if !ok {
		t.Fatal("no config keyspace")
	}
	aks, ok := db.Keyspace(auditOwner)
	if !ok {
		t.Fatal("no audit keyspace")
	}
	as, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq")})
	if err != nil {
		t.Fatal(err)
	}
	store := configstore.New(ks)
	committer, err := configstore.NewCommitter(configstore.CommitterOptions{Store: store, Audit: as, Keys: sections{}})
	if err != nil {
		t.Fatal(err)
	}
	return configtest.Harness{
		Store:     store,
		Committer: committer,
		Commit: func(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
			return storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
				b := ks.NewBatch()
				defer func() { err = errors.Join(err, b.Close()) }()
				if err := stage(ctx, b); err != nil {
					return err
				}
				return b.Commit(ctx, storage.Sync)
			})
		},
		Rows: func(ctx context.Context) (map[config.Path][]byte, error) {
			out := map[config.Path][]byte{}
			for kv, err := range ks.ScanPrefix(ctx, nil) {
				if err != nil {
					return nil, err
				}
				segs, err := keys.Split(kv.Key)
				if err != nil || len(segs) != 2 {
					return nil, errors.Join(errors.New("not a row key"), err)
				}
				out[config.Path(segs[1])] = append([]byte(nil), kv.Value...)
			}
			return out, nil
		},
		Corrupt: func(ctx context.Context, p config.Path, raw []byte) (err error) {
			b := ks.NewBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			if _, err := b.GetForUpdate(ctx, ks, configstore.RowKey(p)); err != nil && !errors.Is(err, storage.ErrNotFound) {
				return err
			}
			if err := b.Set(configstore.RowKey(p), raw); err != nil {
				return err
			}
			return b.Commit(ctx, storage.Sync)
		},
	}
}

// TestOverrideStoreSuite_NativeStore proves the conformance suite itself
// passes against the real native store.
func TestOverrideStoreSuite_NativeStore(t *testing.T) {
	configtest.OverrideStoreSuite(t, nativeHarness)
}
