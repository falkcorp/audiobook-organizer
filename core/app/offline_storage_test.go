// file: core/app/offline_storage_test.go
// version: 1.1.0
// guid: 1805cd97-f796-434f-9fa0-32dc6931c03b
// last-edited: 2026-11-19

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// quarantineBuild is the binary ID the test hands the kernel through bootseam,
// so the direct opens below agree with the offline commands' on the store's
// build fields without hashing the test binary.
const quarantineBuild = "batcomputer-r1"

// quarantineStore opens the served store's directory for a test's own reads
// and writes: core/storage and core/audit are all it names, so the keeper's
// keyspace is dormant. The kernel's offline open and this one are never open
// together (Pebble's lock).
func quarantineStore(ctx context.Context, t *testing.T, opts Options) *storage.DB {
	t.Helper()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:      filepath.Join(opts.StateDir, "storage", "main.pebble"),
		Owners:    []storage.OwnerSpec{{Owner: "core/audit", Spec: storage.Spec{Keyspace: true}, Enabled: true}},
		Build:     quarantineBuild,
		MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Offline()
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	return db
}

// command is the offline command name among cmds.
func command(t *testing.T, cmds []Command, name string) Command {
	t.Helper()
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no offline command %q", name)
	return Command{}
}

// TestQuarantineCommandsCLI: the cutover's offline commands over an app's
// served store. storage migrate requires --dry-run, refuses --estimate with
// --from-archive, and a dry run prints its report and leaves the live store's
// files as they were; storage quarantine list prints no value (here: none);
// export writes a new 0600 file and refuses one that exists; purge requires
// --step and --confirm and is audited, fail-closed (kernel's cutover test
// covers a store opened without core/audit). A store the kernel served has
// core/audit claimed since F2-16, so the purge of a step with a record
// succeeds and its audit event is in the store (a step with no record commits
// nothing, event included). The quarantine's behaviour over records is
// kernel's TestQuarantineCommands.
func TestQuarantineCommandsCLI(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: quarantineBuild})
	a, err := New(ctx, opts, keeper)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cmds := OfflineCommands(opts, keeper)

	migrate := command(t, cmds, "storage migrate")
	var out bytes.Buffer
	if err := migrate.Run(ctx, nil, &out); err == nil || !strings.Contains(err.Error(), "--dry-run is required") {
		t.Errorf("storage migrate without --dry-run = %v", err)
	}
	if err := migrate.Run(ctx, []string{"--dry-run", "--estimate", "--from-archive", "/batcave/backup.tar"}, &out); err == nil {
		t.Error("storage migrate accepted --estimate with --from-archive")
	}
	before := storeFiles(t, opts)
	out.Reset()
	if err := migrate.Run(ctx, []string{"--dry-run"}, &out); err != nil {
		t.Fatalf("storage migrate --dry-run: %v (output %q)", err, out.String())
	}
	if s := out.String(); !strings.Contains(s, "storage migrate --dry-run: dry run of ") || !strings.Contains(s, "the scratch copy is deleted") {
		t.Errorf("dry run report = %q", s)
	}
	if after := storeFiles(t, opts); !slices.Equal(before, after) {
		t.Errorf("the dry run changed the live store:\nbefore %v\nafter  %v", before, after)
	}
	out.Reset()
	if err := migrate.Run(ctx, []string{"--dry-run", "--estimate"}, &out); err != nil || !strings.Contains(out.String(), "ESTIMATE") {
		t.Errorf("storage migrate --dry-run --estimate = %v, output %q", err, out.String())
	}

	out.Reset()
	if err := command(t, cmds, "storage quarantine list").Run(ctx, nil, &out); err != nil || out.String() != "No quarantined rows.\n" {
		t.Errorf("storage quarantine list = %v, output %q", err, out.String())
	}

	export := command(t, cmds, "storage quarantine export")
	if err := export.Run(ctx, nil, &out); err == nil {
		t.Error("storage quarantine export accepted no --out")
	}
	file := filepath.Join(t.TempDir(), "quarantine.jsonl")
	if err := export.Run(ctx, []string{"--out", file}, &out); err != nil {
		t.Fatalf("storage quarantine export: %v", err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
		t.Errorf("export file = %v, %v; want an empty 0600 file", fi, err)
	}
	if err := export.Run(ctx, []string{"--out", file}, &out); err == nil {
		t.Error("storage quarantine export overwrote a file that exists")
	}

	purge := command(t, cmds, "storage quarantine purge")
	if err := purge.Run(ctx, []string{"--confirm"}, &out); err == nil || !strings.Contains(err.Error(), "--step is required") {
		t.Errorf("purge without --step = %v", err)
	}
	if err := purge.Run(ctx, []string{"--step", "gcpd.case-files"}, &out); err == nil || !strings.Contains(err.Error(), "--confirm is required") {
		t.Errorf("purge without --confirm = %v", err)
	}
	// A purge of a step with no record commits nothing, audit event included
	// (storage.DB.PurgeQuarantine), so the audited purge needs a row to
	// delete: one is quarantined into the served store first.
	db := quarantineStore(ctx, t, opts)
	ks, ok := db.Keyspace("core/storage")
	if !ok {
		t.Fatal("the store has no core/storage keyspace")
	}
	b := ks.NewBatch()
	if err := storage.QuarantineWriter(nil)(ctx, b, "gcpd.case-files", []byte("case:harvey_dent"), []byte("two-face"), "unknown mode", false); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(b.Close(), db.Close()); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := purge.Run(ctx, []string{"--step", "gcpd.case-files", "--confirm"}, &out); err != nil {
		t.Fatalf("purge over a served store, which has core/audit = %v, want it audited and done", err)
	}
	db = quarantineStore(ctx, t, opts)
	defer func() { _ = db.Close() }()
	if left, err := db.Quarantine(ctx, "gcpd.case-files"); err != nil || len(left) != 0 {
		t.Errorf("quarantine after the purge = %d records, %v; want none", len(left), err)
	}
	as, err := kernel.OpenAudit(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	var events int
	for rec, err := range as.Query(ctx, audit.Filter{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Action == "core.storage.quarantine_purge" && rec.Reason == "operator_purge" && rec.Actor.Method == "offline" {
			events++
		}
	}
	if events != 1 {
		t.Errorf("core.storage.quarantine_purge audit events = %d, want 1", events)
	}
}

// The cutover build of the keeper "alfred" for storage migrate --dry-run: a
// stand-in for books' M2 over the keeper's legacy "case:" range (retired for
// this build) in Manifest.Cutover, and M8 in Manifest.Rewrite. The step reads
// its legacy row, stages a core config key and imports a secret, so it needs
// every importer the offline command binds over the scratch copy.
const (
	flipStepID    = "alfred.case-files"
	flipRewriteID = "alfred.rewrite"
	flipRow       = "case:file/001"
	flipRowValue  = "The Long Halloween, part 1"
)

func flipKeeper(mode storage.ClaimMode, cutover bool) manifestOnly {
	m := manifestOnly{ID: "alfred", Version: "1.0.0", DefaultEnabled: true,
		Storage: storage.Spec{Keyspace: true, Claims: []storage.Claim{
			{Prefix: "case:", Mode: mode, Description: "the GCPD's case files (a synthetic legacy family)"}}}}
	if !cutover {
		return m
	}
	rw := storage.NewRewriteStep("alfred", flipRewriteID)
	m.Rewrite = &rw
	m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
		return []datamigrate.DataMigration{{
			ID: flipStepID, Owner: "alfred", Description: "import the GCPD's case files, as M2 does",
			Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
			Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
			Compat: storage.Breaking, Cutover: true,
			Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				r := imp.Legacy("alfred")
				if r == nil {
					return errors.New("the legacy range is not bound")
				}
				got, err := r.Get(ctx, []byte(flipRow))
				if err != nil || string(got) != flipRowValue {
					return fmt.Errorf("the legacy read = %q, %v", got, err)
				}
				b := w.Batch()
				if err := imp.Config.Stage(b, "core.backup.retention.max_count", []byte("7"), "alfred.probe"); err != nil {
					return fmt.Errorf("stage a config key: %w", err)
				}
				if err := imp.Secrets.ImportSecret(ctx, b, secrets.Write{
					Name: "core.alfred.batphone", Value: secrets.NewValue("Gotham Gazette hotline"), By: "alfred.probe"}); err != nil {
					return fmt.Errorf("import a secret: %w", err)
				}
				w.Count("imported")
				return nil
			},
		}}
	}
	return m
}

// hashTree is every regular file under dir with its content's SHA-256.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrateCommand_DryRunPlansTheManifestsCutover is F3-07 e1's acceptance
// at the command: over a fixture data directory served by the pre-cutover
// build (gate off, so no keyring), storage migrate --dry-run of the cutover
// build with the gate on prints M2's step with its counts and M8's copy, and
// no byte of the state directory (store, swap lock, and the keyring above
// all) changes. kernel's TestWireDryRun_* own the wiring's cells.
func TestMigrateCommand_DryRunPlansTheManifestsCutover(t *testing.T) {
	opts := offlineOpts(t)
	ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: quarantineBuild})
	a, err := New(ctx, opts, flipKeeper(storage.ClaimReadWrite, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   filepath.Join(opts.StateDir, "storage", "main.pebble"),
		Owners: []storage.OwnerSpec{{Owner: "alfred", Enabled: true, Spec: flipKeeper(storage.ClaimReadWrite, false).Storage}},
		Build:  quarantineBuild, MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Offline()
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	lr, ok := db.Legacy("alfred")
	if !ok {
		t.Fatal("no legacy range for alfred")
	}
	b := lr.NewBatch()
	if err := b.Set([]byte(flipRow), []byte(flipRowValue)); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(b.Commit(ctx, storage.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.StateDir, "secrets.keyring")); !os.IsNotExist(err) {
		t.Fatalf("fixture: the pre-cutover serve left a keyring: %v", err)
	}

	opts.CoreConfigBoot = true
	migrate := command(t, OfflineCommands(opts, flipKeeper(storage.ClaimRetired, true)), "storage migrate")
	before := hashTree(t, opts.StateDir)
	var out bytes.Buffer
	if err := migrate.Run(ctx, []string{"--dry-run"}, &out); err != nil {
		t.Fatalf("storage migrate --dry-run: %v (output %q)", err, out.String())
	}
	report := out.String()
	for _, want := range []string{"step " + flipStepID + ": state=done", "  imported: 1", "M8 store rewrite:"} {
		if !strings.Contains(report, want) {
			t.Errorf("the dry run report lacks %q:\n%s", want, report)
		}
	}
	after := hashTree(t, opts.StateDir)
	for name, sum := range before {
		if got, ok := after[name]; !ok || got != sum {
			t.Errorf("the dry run changed %s (removed %t)", name, !ok)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("the dry run created %s", name)
		}
	}
}
