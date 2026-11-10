// file: core/coretest/crashmatrix/chunks.go
// version: 1.0.0
// guid: ca419ac2-8938-45ee-a04a-fe0d1930eb92
// last-edited: 2026-11-10

package crashmatrix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// DataMigrationLabels are the crash-matrix labels of the data-migration
// runner's three boot writers (06 §6.5.1 "Boot writers"): the plan with the
// running state, each chunk's batch with its shard cursor, and the
// verifying, done and failed states with Verify's report. Name coined by
// F2-06.
func DataMigrationLabels() []string {
	return []string{storagemigrate.LabelPlan, storagemigrate.LabelChunk, storagemigrate.LabelState}
}

// ChunkScenario is one cell of the chunk-boundary crash matrix (05-storage
// §5S.3.13 TestCutoverCrashAtEveryChunk): a store, the data migrations that
// run on it, and what must hold once they are done. Name coined by F2-06.
type ChunkScenario struct {
	// Path is the store's absolute path on the in-memory filesystem.
	Path string
	// Seed writes the starting store on fs and closes it: an upgrade's
	// store, since a fresh install records every data migration done
	// without running it.
	Seed func(t testing.TB, fs vfs.FS)
	// Migrations are the data migrations under test.
	Migrations []datamigrate.DataMigration
	// Boot opens the store at Path on fs and runs the boot's migration DAG
	// with migs (storage.Open with the kernel's DataMigrations steps,
	// Prepare, Migrate, Close) and returns its error. The census's ctx
	// carries the recorder's seams; a reboot's carries none. It may run
	// many times, never concurrently.
	Boot func(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error
	// Check asserts the invariants on fs after a boot that completed: no
	// row lost or doubled, every migration done.
	Check func(t testing.TB, fs vfs.FS)
	// Labels are the writers whose kill points are cloned; nil = all three
	// (DataMigrationLabels). A cell of one writer names it.
	Labels []string
	// Inside also clones a point inside each chunk: after its first item
	// is stepped and before its commit, so the chunk's uncommitted batch is
	// what the crash drops.
	Inside bool
	// NoChunkStarts skips the clone at each chunk's first item (a cell of
	// one writer other than the chunk's).
	NoChunkStarts bool
}

// ChunkReport is what RunChunks broke. Name coined by F2-06.
type ChunkReport struct {
	// Observed is every label the census boot's durable operations carried.
	Observed map[string]bool
	// Kills is the kill points cloned at the labelled writers: the kill
	// before each WAL sync. Concurrent shards' commits share a sync (Pebble
	// groups them), so these alone do not reach every chunk boundary of
	// every shard; ChunkStarts does.
	Kills int
	// ChunkStarts is the clones taken at each chunk's first item: the
	// boundary after that shard's previous chunk committed, whatever the
	// other shards had committed by then. Inside is the points inside
	// chunks.
	ChunkStarts, Inside int
}

// RunChunks is the harness: it runs s's census boot over a crashable
// in-memory copy of the seeded store and takes a crash clone (no unsynced
// data) before every kill point of the labelled writers, at every chunk
// boundary of every shard (each chunk's first item: its shard's previous
// chunk is committed, this one is not), and, with Inside, one inside each
// chunk; then it boots every clone again with the unwrapped migrations and
// asserts that the boot completes and s.Check holds. A cell that clones
// nothing fails: it would prove nothing. Name coined by F2-06.
func RunChunks(t *testing.T, s ChunkScenario) ChunkReport {
	t.Helper()
	labels := map[string]bool{}
	for _, l := range s.Labels {
		labels[l] = true
	}
	if len(labels) == 0 {
		for _, l := range DataMigrationLabels() {
			labels[l] = true
		}
	}
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(diskUsage())
	s.Seed(t, mem)
	if t.Failed() {
		t.FailNow()
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{
		CloneIf: func(ev bootseamtest.Event) bool { return labels[ev.Label] },
		Stores:  []string{s.Path},
	})
	var mu sync.Mutex
	var starts, inside []*vfs.MemFS
	stepped := map[*datamigrate.StepWriter]int{}
	wrapped := slices.Clone(s.Migrations)
	for i := range wrapped {
		step := wrapped[i].Step
		wrapped[i].Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			mu.Lock()
			// One StepWriter per chunk: its first item is the boundary after
			// the shard's previous chunk, its second is inside it.
			stepped[w]++
			switch {
			case stepped[w] == 1 && !s.NoChunkStarts:
				starts = append(starts, mem.CrashClone(vfs.CrashCloneCfg{}))
			case stepped[w] == 2 && s.Inside:
				inside = append(inside, mem.CrashClone(vfs.CrashCloneCfg{}))
			}
			mu.Unlock()
			return step(ctx, kv, w)
		}
	}
	ctx := bootseam.With(context.Background(), rec.Seams())
	if err := s.Boot(ctx, rec.FS(), wrapped); err != nil {
		t.Fatalf("crashmatrix: census boot: %v", err)
	}
	rec.Freeze()
	s.Check(t, mem)
	report := ChunkReport{Observed: map[string]bool{}}
	for _, ev := range rec.Events() {
		report.Observed[ev.Label] = true
	}
	for l := range labels {
		if !report.Observed[l] {
			t.Errorf("crashmatrix: the census boot never reached writer %s", l)
		}
	}
	type kill struct {
		name string
		fs   *vfs.MemFS
	}
	var kills []kill
	for i, c := range rec.Clones() {
		kills = append(kills, kill{fmt.Sprintf("kill-%03d-%s-op%d", i, c.Event.Label, c.Event.Kind), c.FS})
	}
	report.Kills = len(kills)
	for i, c := range starts {
		kills = append(kills, kill{fmt.Sprintf("chunk-start-%03d", i), c})
	}
	for i, c := range inside {
		kills = append(kills, kill{fmt.Sprintf("inside-chunk-%03d", i), c})
	}
	report.ChunkStarts, report.Inside = len(starts), len(inside)
	if len(kills) == 0 {
		t.Fatal("crashmatrix: the cell cloned no kill point, so it proves nothing")
	}
	for _, k := range kills {
		t.Run(k.name, func(t *testing.T) {
			k.fs.TestingSetDiskUsage(diskUsage())
			if err := s.Boot(context.Background(), k.fs, s.Migrations); err != nil {
				t.Fatalf("reboot after the kill: %v", err)
			}
			s.Check(t, k.fs)
		})
	}
	t.Logf("crashmatrix: %d kill points at %v, %d chunk boundaries, %d chunk interiors, each rebooted and checked",
		report.Kills, slices.Sorted(maps.Keys(labels)), report.ChunkStarts, report.Inside)
	return report
}

// ObservedChunks runs only s's census boot, fault-free and unbroken, and
// returns the labels its durable operations carried: the cells' half of
// TestBootCrashMatrixCoversEveryWriter (Observed's twin for a
// ChunkScenario). Name coined by F2-06.
func ObservedChunks(t *testing.T, s ChunkScenario) map[string]bool {
	t.Helper()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(diskUsage())
	s.Seed(t, mem)
	if t.Failed() {
		t.FailNow()
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{Stores: []string{s.Path}})
	if err := s.Boot(bootseam.With(context.Background(), rec.Seams()), rec.FS(), s.Migrations); err != nil {
		t.Fatalf("crashmatrix: census boot: %v", err)
	}
	rec.Freeze()
	out := map[string]bool{}
	for _, ev := range rec.Events() {
		out[ev.Label] = true
	}
	return out
}

// SyntheticCutover is the harness's proof migration (F2-06's brief): a
// Cutover-shaped data migration over a fixture family of a few thousand
// rows under the claim "case:" of a module owner, sharded with
// Parallelism. Each row is imported into the owner's keyspace as
// case/<n> through StepWriter.Batch (an importer's participant staging) and
// its source key deleted, or, every QuarantineEvery-th row, quarantined
// through a fake quarantine writer that stages c/storage/quarantine/<step>/
// <key> into the chunk's batch. The cell boots through the kernel's P4b
// entry point (kernel.NewDataMigrations, DataMigrations.Steps into
// storage.Open). Each real cutover step (M2 in F3-06, M3 in F4-06, ...)
// adds its own cells. Name coined by F2-06.
type SyntheticCutover struct {
	// Owner is a module owner; Spec its storage spec without the claim:
	// the cell adds "case:" read-write to seed the rows, retired to cut
	// over (§5S.3.13: the cutover build retires the family).
	Owner storage.Owner
	Spec  storage.Spec
	// Rows is the fixture's size; 0 = 2 400.
	Rows int
	// Parallelism is the migration's; zero = tasks.Fixed(4, "test").
	Parallelism tasks.Parallelism
	// ChunkItems is core.storage.data_migration.chunk_items; 0 = 150.
	ChunkItems int
	// QuarantineEvery quarantines every n-th row; 0 = 97.
	QuarantineEvery int
	// NoQuarantineWriter boots without the kernel's quarantine writer, as
	// every build does until F3-04: the first quarantine fails the chunk.
	NoQuarantineWriter bool
}

// SyntheticPath is where a synthetic cell's store lives on its in-memory
// filesystem. Name coined by F2-06.
const SyntheticPath = "/batcave/store"

// SyntheticID is the synthetic migration's ID for owner. Name coined by
// F2-06.
func SyntheticID(owner storage.Owner) string { return string(owner) + ".case-files" }

func (c SyntheticCutover) defaults() SyntheticCutover {
	if c.Rows == 0 {
		c.Rows = 2400
	}
	if c.Parallelism == (tasks.Parallelism{}) {
		c.Parallelism = tasks.Fixed(4, "test: four disjoint shards of the fixture")
	}
	if c.ChunkItems == 0 {
		c.ChunkItems = 150
	}
	if c.QuarantineEvery == 0 {
		c.QuarantineEvery = 97
	}
	return c
}

// owners is the build's owner list with the claim in mode.
func (c SyntheticCutover) owners(mode storage.ClaimMode) []storage.OwnerSpec {
	spec := c.Spec
	spec.Keyspace = true
	spec.Claims = append(slices.Clone(spec.Claims), storage.Claim{Prefix: "case:", Mode: mode, Description: "the GCPD's case files (a synthetic legacy family)"})
	return []storage.OwnerSpec{{Owner: c.Owner, Spec: spec, Enabled: true}}
}

// SyntheticRow is row n's source key and value. Name coined by F2-06.
func SyntheticRow(n int) (key, value []byte) {
	return []byte(fmt.Sprintf("case:%05d", n)), []byte(fmt.Sprintf("The Long Halloween, part %d", n))
}

// handles is what one boot's steps reach: the owner's keyspace (imports)
// and storage's own (the fake quarantine records), set between Prepare and
// Migrate.
type handles struct{ ks, sks storage.Keyspace }

// Migration is the synthetic migration, its Step and fake writer reaching
// h, the current boot's handles.
func (c SyntheticCutover) migration(h *handles) datamigrate.DataMigration {
	c = c.defaults()
	id := SyntheticID(c.Owner)
	return datamigrate.DataMigration{
		ID: id, Owner: c.Owner, Description: "import the GCPD's case files",
		Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: c.Parallelism, Mode: datamigrate.BootBlocking, Compat: storage.Breaking, Cutover: true,
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			var n int
			if _, err := fmt.Sscanf(string(kv.Key), "case:%05d", &n); err != nil {
				return err
			}
			if n%c.QuarantineEvery == 0 {
				return w.Quarantine(kv.Key, kv.Value, "Joker venom: the row does not decode")
			}
			b := w.Batch().Bind(h.ks)
			if err := b.In(h.ks).Set([]byte(fmt.Sprintf("case/%05d", n)), append([]byte("imported: "), kv.Value...)); err != nil {
				return err
			}
			if err := w.Delete(kv.Key); err != nil {
				return err
			}
			w.Count("imported")
			return nil
		},
		Verify: func(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
			var rep datamigrate.VerifyReport
			for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("case:"), End: []byte("case;"), KeysOnly: true}) {
				if err != nil {
					return rep, err
				}
				rep.Failures = append(rep.Failures, fmt.Sprintf("%s was neither imported nor quarantined", kv.Key))
			}
			return rep, nil
		},
	}
}

// quarantineKey is the fake writer's record of a source key.
func quarantineKey(step string, key []byte) []byte {
	return []byte("quarantine/" + step + "/" + string(key))
}

// Boot opens the store at SyntheticPath on fs with the cutover build's
// owners and the kernel's data-migration steps for migs, then prepares and
// migrates it (P2, P4a, P4b) and closes it.
func (c SyntheticCutover) boot(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration, h *handles) error {
	c = c.defaults()
	cfg := kernel.DataMigrationsConfig{ChunkItems: c.ChunkItems, Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler)}
	if !c.NoQuarantineWriter {
		cfg.Quarantine = func(_ context.Context, b *storage.Batch, step string, key, raw []byte, _ string, _ bool) error {
			return b.Bind(h.sks).In(h.sks).Set(quarantineKey(step, key), raw)
		}
	}
	owners := c.owners(storage.ClaimRetired)
	dms, err := kernel.NewDataMigrations(cfg, owners, migs...)
	if err != nil {
		return err
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: SyntheticPath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: Now})
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	h.ks, _ = db.Keyspace(c.Owner)
	h.sks, _ = db.Keyspace("core/storage")
	return errors.Join(db.Migrate(ctx), db.Close())
}

// Scenario is the cell over this migration: Seed writes the rows with the
// claim read-write (the pre-cutover build), Boot cuts over, Check asserts
// that every row was imported or quarantined exactly once and its source
// deleted, nothing else written, and the migration done.
func (c SyntheticCutover) Scenario() ChunkScenario {
	c = c.defaults()
	h := &handles{}
	return ChunkScenario{
		Path:       SyntheticPath,
		Migrations: []datamigrate.DataMigration{c.migration(h)},
		Seed:       func(t testing.TB, fs vfs.FS) { c.Seed(t, fs) },
		Boot: func(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error {
			return c.boot(ctx, fs, migs, h)
		},
		Check: func(t testing.TB, fs vfs.FS) { c.Check(t, fs) },
	}
}

// Boot is the cell's boot with its own migration, for a test that drives
// one boot by hand (the nil-writer refusal).
func (c SyntheticCutover) Boot(ctx context.Context, fs vfs.FS) error {
	h := &handles{}
	return c.boot(ctx, fs, []datamigrate.DataMigration{c.migration(h)}, h)
}

// Seed writes the pre-cutover store at SyntheticPath on fs: a fresh install
// of the owner with the claim read-write, then the rows, then a close.
func (c SyntheticCutover) Seed(t testing.TB, fs vfs.FS) {
	t.Helper()
	c = c.defaults()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: SyntheticPath, FS: storage.FSOf(fs), Owners: c.owners(storage.ClaimReadWrite),
		Build: "batcomputer-1", FreeSpaceMargin: 1, Clock: Now})
	if err != nil {
		t.Fatalf("crashmatrix: seed open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("crashmatrix: seed boot: %v", errors.Join(err, o.Close()))
	}
	lr, _ := db.Legacy(c.Owner)
	b := lr.NewBatch()
	for n := 1; n <= c.Rows; n++ {
		k, v := SyntheticRow(n)
		if err := b.Set(k, v); err != nil {
			t.Fatalf("crashmatrix: seed row %d: %v", n, err)
		}
	}
	if err := errors.Join(b.Commit(ctx, storage.Sync), db.Close()); err != nil {
		t.Fatalf("crashmatrix: seed commit: %v", err)
	}
}

// Check opens the store read-only and asserts the cutover's invariants:
// for every row exactly one of its import and its quarantine record, with
// the source's bytes, the source key gone, no other key written, and the
// migration's state done.
func (c SyntheticCutover) Check(t testing.TB, fs vfs.FS) {
	t.Helper()
	c = c.defaults()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: SyntheticPath, FS: storage.FSOf(fs), Owners: c.owners(storage.ClaimRetired),
		Build: "batcomputer-2", ReadOnly: true, MustExist: true, Clock: Now})
	if err != nil {
		t.Fatalf("crashmatrix: check open: %v", err)
	}
	defer func() {
		if err := o.Close(); err != nil {
			t.Errorf("crashmatrix: check close: %v", err)
		}
	}()
	db, err := o.Offline()
	if err != nil {
		t.Fatalf("crashmatrix: check offline: %v", err)
	}
	ks, _ := db.Keyspace(c.Owner)
	sks, _ := db.Keyspace("core/storage")
	lr, _ := db.Legacy(c.Owner)
	id := SyntheticID(c.Owner)
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: []byte("case:"), End: []byte("case;"), KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("source row %s left after the cutover", kv.Key)
	}
	imported, quarantined := 0, 0
	for n := 1; n <= c.Rows; n++ {
		k, v := SyntheticRow(n)
		got, ierr := ks.Get(ctx, []byte(fmt.Sprintf("case/%05d", n)))
		raw, qerr := sks.Get(ctx, quarantineKey(id, k))
		switch {
		case ierr == nil && qerr == nil:
			t.Errorf("row %d both imported and quarantined (doubled)", n)
		case ierr == nil:
			imported++
			if want := append([]byte("imported: "), v...); !bytes.Equal(got, want) {
				t.Errorf("row %d imported as %q, want %q", n, got, want)
			}
		case qerr == nil:
			quarantined++
			if !bytes.Equal(raw, v) || n%c.QuarantineEvery != 0 {
				t.Errorf("row %d quarantined as %q (every %d-th row is), want its bytes %q", n, raw, c.QuarantineEvery, v)
			}
		case errors.Is(ierr, storage.ErrNotFound) && errors.Is(qerr, storage.ErrNotFound):
			t.Errorf("row %d lost: neither imported nor quarantined", n)
		default:
			t.Fatalf("row %d: %v, %v", n, ierr, qerr)
		}
	}
	extra := -imported
	for _, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte("case/"), End: []byte("case0"), KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		extra++
	}
	if extra != 0 || imported+quarantined != c.Rows {
		t.Errorf("imported %d + quarantined %d of %d rows, %d other import keys", imported, quarantined, c.Rows, extra)
	}
	raw, err := sks.Get(ctx, storage.DataMigrationKey(id))
	if err != nil {
		t.Fatalf("state record of %s: %v", id, err)
	}
	if rec, err := storage.DataMigrationCodec().Decode(raw); err != nil || rec.State != datamigrate.StateDone {
		t.Errorf("state of %s = %+v, %v; want done", id, rec, err)
	}
}
