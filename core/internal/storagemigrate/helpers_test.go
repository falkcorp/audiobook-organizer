// file: core/internal/storagemigrate/helpers_test.go
// version: 1.0.0
// guid: 469954e6-2076-4fec-aa1e-f7a1f1a96c2a
// last-edited: 2026-11-10

package storagemigrate_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

const storePath = "/batcave/store"

// seed is gotham's first versioned step.
func seed() storage.Migration {
	return storage.Migration{Version: 1, Name: "bat-signal", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return nil }}
}

func sequential() tasks.Parallelism { return tasks.Sequential("test: one shard") }

// plenty is the in-memory filesystem's disk usage: the checkpoint's
// headroom check passes.
func plenty() vfs.DiskUsage {
	return vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}
}

// readOnly opens c's store read-only for checks (no data steps, so a
// pending migration does not refuse the offline open).
func readOnly(t *testing.T, fs vfs.FS, c crashmatrix.SyntheticCutover) *storage.DB {
	t.Helper()
	spec := c.Spec
	spec.Keyspace = true
	spec.Claims = append(spec.Claims, storage.Claim{Prefix: "case:", Mode: storage.ClaimRetired})
	o, err := storage.Open(context.Background(), storage.OpenOptions{Path: crashmatrix.SyntheticPath, FS: storage.FSOf(fs),
		Owners: []storage.OwnerSpec{{Owner: c.Owner, Spec: spec, Enabled: true}}, Build: "batcomputer-2", ReadOnly: true, MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Offline()
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// gotham is the module owner of the keyspace tests: versions 1..n.
func gotham(versions ...storage.Migration) storage.OwnerSpec {
	return storage.OwnerSpec{Owner: "gotham", Enabled: true, Spec: storage.Spec{Keyspace: true,
		Migrations: append([]storage.Migration{seed()}, versions...)}}
}

// item is row n of the keyspace fixture, relative to m/gotham/.
func item(n int) []byte { return []byte(fmt.Sprintf("item/%05d", n)) }

// itemsSource is the absolute range of every item row.
func itemsSource() []storage.Range {
	start := []byte("m/gotham/item/")
	return []storage.Range{{Start: start, End: keys.PrefixEnd(start)}}
}

// env is one in-memory store of the keyspace tests.
type env struct {
	t      *testing.T
	fs     *vfs.MemFS
	owners []storage.OwnerSpec
	viewed *storage.DB // the last view's read-only DB, closed by the next view or boot
}

// closeView closes the last view: Pebble's LOCK admits one open per store.
func (e *env) closeView() {
	if e.viewed != nil {
		_ = e.viewed.Close()
		e.viewed = nil
	}
}

// newEnv seeds a gotham store (a fresh install at version 1) with rows
// item rows of value "0".
func newEnv(t *testing.T, rows int) *env {
	t.Helper()
	e := &env{t: t, fs: vfs.NewCrashableMem(), owners: []storage.OwnerSpec{gotham()}}
	e.fs.TestingSetDiskUsage(plenty())
	db, err := e.boot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ks, _ := db.Keyspace("gotham")
	b := ks.NewBatch()
	for n := 1; n <= rows; n++ {
		if err := b.Set(item(n), []byte("0")); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(b.Commit(context.Background(), storage.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
	return e
}

// boot opens the store with e.owners and r's steps (nil = none), prepares
// and migrates it. On a Migrate failure it returns the open DB with the
// error, so a test can read what the failed boot left; the caller closes.
func (e *env) boot(ctx context.Context, r *storagemigrate.Runner) (*storage.DB, error) {
	return e.bootFS(ctx, e.fs, r)
}

func (e *env) bootFS(ctx context.Context, fs vfs.FS, r *storagemigrate.Runner) (*storage.DB, error) {
	e.closeView()
	var steps []storage.DataStep
	if r != nil {
		steps = r.Steps()
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: storePath, FS: storage.FSOf(fs), Owners: e.owners,
		Build: "batcomputer-1", FreeSpaceMargin: 1, DataSteps: steps})
	if err != nil {
		return nil, err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return nil, errors.Join(err, o.Close())
	}
	return db, db.Migrate(ctx)
}

// mustBoot boots and closes, failing the test on any error.
func (e *env) mustBoot(r *storagemigrate.Runner) {
	e.t.Helper()
	db, err := e.boot(context.Background(), r)
	if db != nil {
		err = errors.Join(err, db.Close())
	}
	if err != nil {
		e.t.Fatal(err)
	}
}

// runner builds a runner over e.owners, failing the test on a refusal.
func (e *env) runner(opts storagemigrate.Options, migs ...datamigrate.DataMigration) *storagemigrate.Runner {
	e.t.Helper()
	opts.Owners = e.owners
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	r, err := storagemigrate.New(opts, migs...)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// view opens the store read-only, without data steps, and returns gotham's
// keyspace and storage's own, valid until the next view or boot.
func (e *env) view(fs vfs.FS) (ks, sks storage.Keyspace) {
	e.t.Helper()
	e.closeView()
	o, err := storage.Open(context.Background(), storage.OpenOptions{Path: storePath, FS: storage.FSOf(fs), Owners: e.owners,
		Build: "batcomputer-1", ReadOnly: true, MustExist: true})
	if err != nil {
		e.t.Fatal(err)
	}
	db, err := o.Offline()
	if err != nil {
		_ = o.Close()
		e.t.Fatal(err)
	}
	e.viewed = db
	e.t.Cleanup(e.closeView)
	ks, _ = db.Keyspace("gotham")
	sks, _ = db.Keyspace("core/storage")
	return ks, sks
}

// values is every item row's value, by row number.
func values(t *testing.T, ks storage.Keyspace) map[int]string {
	t.Helper()
	out := map[int]string{}
	start := []byte("item/")
	for kv, err := range ks.Scan(context.Background(), storage.ScanOptions{Start: start, End: keys.PrefixEnd(start)}) {
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if _, err := fmt.Sscanf(string(kv.Key), "item/%05d", &n); err != nil {
			t.Fatalf("unexpected key %q", kv.Key)
		}
		out[n] = string(kv.Value)
	}
	return out
}

// state is migration id's stored state, "" when absent.
func state(t *testing.T, sks storage.Keyspace, id string) string {
	t.Helper()
	raw, err := sks.Get(context.Background(), storage.DataMigrationKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	rec, err := storage.DataMigrationCodec().Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rec.State
}

// recount is the in-place migration of the keyspace tests: each item's
// value goes up by one, so an item stepped twice (committed twice) reads 2
// and a skipped one 0.
func recount(par tasks.Parallelism) datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gotham.recount", Owner: "gotham", Description: "count the Bat-signal's sightings again",
		Source: itemsSource(), Parallelism: par, Mode: datamigrate.BootBlocking, Compat: storage.Additive,
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			n, err := strconv.Atoi(string(kv.Value))
			if err != nil {
				return err
			}
			w.Count("converted")
			return w.Set(kv.Key, []byte(strconv.Itoa(n+1)))
		},
	}
}

// newMem is a crashable in-memory filesystem with room for a checkpoint.
func newMem() *vfs.MemFS {
	fs := vfs.NewCrashableMem()
	fs.TestingSetDiskUsage(plenty())
	return fs
}
