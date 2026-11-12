// file: core/app/offline_storage_test.go
// version: 1.0.1
// guid: 1805cd97-f796-434f-9fa0-32dc6931c03b
// last-edited: 2026-11-11

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
// core/audit claimed since F2-16, so the purge of an empty step succeeds. The
// quarantine's behaviour over records is kernel's TestQuarantineCommands.
func TestQuarantineCommandsCLI(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	served(t, opts, keeper)
	cmds := OfflineCommands(opts, keeper)
	ctx := t.Context()

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
	if err := purge.Run(ctx, []string{"--step", "gcpd.case-files", "--confirm"}, &out); err != nil {
		t.Errorf("purge over a served store, which has core/audit = %v, want it audited and done", err)
	}
}
