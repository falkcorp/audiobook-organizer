// file: core/tasks/taskstest/storesuite_test.go
// version: 1.1.0
// guid: 630eea01-5924-4ce6-8c9e-1def4106715d
// last-edited: 2026-11-10

package taskstest_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// TestStoresSuite_NativeStores runs the kit against the one implementation
// there is (D10), so the kit's own package measures what it exercises.
// core/tasks' TestNativeStores_RoundTrip runs it too, with the native-only
// key-layout checks around it.
func TestStoresSuite_NativeStores(t *testing.T) {
	taskstest.StoresSuite(t, func(t *testing.T) tasks.Stores {
		return boot(t, vfs.NewCrashableMem(), filepath.Join(t.TempDir(), "main.pebble"))
	})
}

// TestStoresSuite_SkipsWithoutBatches: a Stores whose Runs offers no batches
// of its own DB skips every case that writes, rather than failing them.
func TestStoresSuite_SkipsWithoutBatches(t *testing.T) {
	taskstest.StoresSuite(t, func(t *testing.T) tasks.Stores {
		s := boot(t, vfs.NewCrashableMem(), filepath.Join(t.TempDir(), "main.pebble"))
		s.Runs = struct{ tasks.RunStore }{s.Runs}
		return s
	})
}

// boot opens path on fs with core/tasks registered and returns NativeStores
// over it, with Runs wrapped to add taskstest.Crasher.
func boot(t *testing.T, fs *vfs.MemFS, path string) tasks.Stores {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            path,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fs),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Errorf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	s := tasks.NativeStores(ks)
	s.Runs = crashRuns{RunStore: s.Runs, fs: fs, path: path}
	return s
}

// crashRuns adds taskstest.Crasher to NativeStores' RunStore: a crash clone
// of the file system, booted again.
type crashRuns struct {
	tasks.RunStore
	fs   *vfs.MemFS
	path string
}

func (c crashRuns) NewBatch() *storage.Batch { return c.RunStore.(taskstest.BatchOpener).NewBatch() }

func (c crashRuns) Importer() tasks.Importer {
	return c.RunStore.(taskstest.ImporterProvider).Importer()
}

func (c crashRuns) CrashClone(t *testing.T) tasks.Stores {
	return boot(t, c.fs.CrashClone(vfs.CrashCloneCfg{}), c.path)
}
