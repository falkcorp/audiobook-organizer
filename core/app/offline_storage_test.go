// file: core/app/offline_storage_test.go
// version: 1.0.2
// guid: 1805cd97-f796-434f-9fa0-32dc6931c03b
// last-edited: 2026-11-11

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
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
	aks, ok := db.Keyspace("core/audit")
	if !ok {
		t.Fatal("the store has no core/audit keyspace")
	}
	as, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq")})
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
