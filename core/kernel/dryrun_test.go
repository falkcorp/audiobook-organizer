// file: core/kernel/dryrun_test.go
// version: 1.0.1
// guid: 1a867e9f-773e-4516-bfdc-da0d56a19606
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// gcpdMod is the cutover fixture's module: gcpd with the legacy claim
// "case:" in mode (read-write before the cutover, retired by it).
func gcpdMod(mode storage.ClaimMode) testModule {
	return mod("gcpd", nil).with(func(m *module.Manifest) {
		m.Storage = storage.Spec{Keyspace: true, Claims: []storage.Claim{
			{Prefix: "case:", Mode: mode, Description: "the GCPD's case files (a synthetic legacy family)"}}}
	})
}

// treeOf lists every regular file under dir with its size.
func treeOf(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fi.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dryRunHost is a kernel host whose store holds the cutover fixture's rows,
// written by the pre-cutover build (claim read-write); it returns the
// cutover build's Config (claim retired).
func dryRunHost(t *testing.T, rows map[string]string) (*swapHost, Config) {
	t.Helper()
	h := newSwapHost(t)
	seed := testConfig(t, gcpdMod(storage.ClaimReadWrite))
	seed.StoragePath, seed.StateDir = h.db, h.state
	k := h.mustBoot(seed)
	lr, _ := k.store.db.Legacy("gcpd")
	b := lr.NewBatch()
	for key, v := range rows {
		if err := b.Set([]byte(key), []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(h.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	h.close(k)
	c := testConfig(t, gcpdMod(storage.ClaimRetired))
	c.StoragePath, c.StateDir = h.db, h.state
	return h, c
}

// liveCutover runs the cutover for real over c's store, as P4b does: the
// kernel's own storage options, the step through NewDataMigrations, Open,
// Prepare, Migrate. It returns the runner's counts per outcome and the
// quarantine.
func liveCutover(t *testing.T, ctx context.Context, c Config) (map[string]int64, []storage.QuarantineEntry) {
	t.Helper()
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	opts, err := k.storageOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h, sf := &cutoverHandles{}, &fakeSealer{}
	dms, err := NewDataMigrations(DataMigrationsConfig{Build: opts.Build, ChunkItems: 7, LevelStep: LevelStep,
		Quarantine: storage.QuarantineWriter(sf)}, opts.Owners, cutoverMigration(h))
	if err != nil {
		t.Fatal(err)
	}
	opts.DataSteps = dms.Steps()
	o, err := storage.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	defer func() { _ = db.Close() }()
	h.ks, _ = db.Keyspace("gcpd")
	sf.db = db
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := dms.Stats(ctx, db)
	if err != nil || len(stats) != 1 {
		t.Fatalf("live stats = %+v, %v", stats, err)
	}
	q, err := db.Quarantine(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	return stats[0].Items, q
}

// TestDryRunMatchesLiveRun is F3-04's dry-run gate (05-storage §5S.3.6 "Dry
// run", §5S.10): `storage migrate --dry-run` over a scratch copy reports
// the counts per outcome and the would-quarantine rows a live run then
// produces, sensitive flags included and no value; the live directory's
// files (SSTs, MANIFEST, OPTIONS: names and sizes) are unchanged by it, so
// nothing was written and no format ratcheted; the scratch is deleted.
func TestDryRunMatchesLiveRun(t *testing.T) {
	rows := cutoverRows(40, 3)
	h, c := dryRunHost(t, rows)
	before := treeOf(t, h.db)
	if !slices.ContainsFunc(slices.Collect(maps.Keys(before)), func(n string) bool { return strings.HasPrefix(n, "MANIFEST-") }) {
		t.Fatalf("fixture: the live store has no MANIFEST: %v", before)
	}

	dh, dsf := &cutoverHandles{}, &fakeSealer{}
	rep, err := DryRun(h.ctx, c, DryRunOptions{
		Migrations: []datamigrate.DataMigration{cutoverMigration(dh)},
		Sealer:     func(scratch string) (storage.QuarantineSealer, error) { return dsf, nil },
		Bind: func(db *storage.DB) error {
			dh.ks, _ = db.Keyspace("gcpd")
			dsf.db = db
			return nil
		},
	})
	if err != nil {
		t.Fatalf("dry run: %v (report %+v)", err, rep)
	}
	if after := treeOf(t, h.db); !maps.Equal(before, after) {
		t.Fatalf("the dry run changed the live directory:\nbefore %v\nafter  %v", before, after)
	}
	var leftovers []string
	_ = filepath.WalkDir(filepath.Dir(h.state), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), "dryrun-") {
			leftovers = append(leftovers, p)
		}
		return nil
	})
	if len(leftovers) > 0 {
		t.Errorf("the dry run's scratch survived: %v", leftovers)
	}
	if len(rep.Steps) != 1 || rep.Steps[0].ID != "gcpd.case-files" || rep.Err != "" || len(rep.Blockers) != 0 {
		t.Fatalf("report = %+v, want the one step run clean", rep)
	}
	dry := rep.Steps[0]
	for _, line := range rep.Lines() {
		if strings.Contains(line, "safe combination") || strings.Contains(line, "Long Halloween") {
			t.Errorf("report line %q shows a value", line)
		}
	}

	items, q := liveCutover(t, h.ctx, c)
	if !maps.Equal(dry.Items, items) {
		t.Errorf("dry run counts %v, live run %v", dry.Items, items)
	}
	type qrow struct {
		key, reason string
		sensitive   bool
	}
	var live, dryQ []qrow
	for _, e := range q {
		live = append(live, qrow{storage.PrintableKey(e.Record.Key), e.Record.Reason, e.Record.Sensitive})
	}
	for _, d := range dry.Quarantine {
		dryQ = append(dryQ, qrow{d.Key, d.Reason, d.Sensitive})
	}
	cmp := func(a, b qrow) int { return strings.Compare(a.key, b.key) }
	slices.SortFunc(live, cmp)
	slices.SortFunc(dryQ, cmp)
	if len(live) != 11 || !slices.Equal(live, dryQ) {
		t.Errorf("dry run quarantine %v, live run %v (want 11 rows: 8 undecodable, 3 sensitive)", dryQ, live)
	}
	if dry.Items["imported"] != 32 {
		t.Errorf("dry run imported %d, want 32", dry.Items["imported"])
	}
	if _, err := os.Stat(h.db); err != nil {
		t.Fatal(err)
	}
}

// TestDryRunRefusesAnUnknownID: --id naming no data migration of this
// build is refused before any scratch copy, with a non-zero exit and the
// registered IDs named, rather than reporting a clean run of nothing; M8's
// ID and a registered step's are accepted.
func TestDryRunRefusesAnUnknownID(t *testing.T) {
	h, c := dryRunHost(t, cutoverRows(3, 1))
	rw := storage.NewRewriteStep("gcpd", "gcpd.rewrite")
	rep, err := DryRun(h.ctx, c, DryRunOptions{ID: "gcpd.riddles", Migrations: []datamigrate.DataMigration{cutoverMigration(&cutoverHandles{})}, Rewrite: &rw})
	if err == nil || ExitCode(err) == 0 || !strings.Contains(err.Error(), `"gcpd.riddles"`) ||
		!strings.Contains(err.Error(), "gcpd.case-files, gcpd.rewrite") {
		t.Fatalf("dry run --id gcpd.riddles = %+v, %v; want refused naming the registered IDs", rep, err)
	}
	var scratch []string
	_ = filepath.WalkDir(filepath.Dir(h.state), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), "dryrun-") {
			scratch = append(scratch, p)
		}
		return nil
	})
	if len(scratch) > 0 {
		t.Errorf("the refused dry run made scratch %v", scratch)
	}
	for _, id := range []string{"gcpd.case-files", "gcpd.rewrite", ""} {
		if migs, err := dryRunList([]datamigrate.DataMigration{cutoverMigration(&cutoverHandles{})}, id, rw.ID); err != nil {
			t.Errorf("--id %q = %v, %v; want accepted", id, migs, err)
		}
	}
}
