// file: core/app/offline_storage.go
// version: 1.0.0
// guid: 8fbea565-1376-4d4b-a491-c6078aa8e4db
// last-edited: 2026-11-10

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// The cutover's offline commands (05-storage §5S.3.6 "Dry run", §5S.3.13
// "Quarantine"; 09 §9.2.3): storage migrate --dry-run and storage quarantine
// list, export and purge. Until F2-16 and F3-07 wire the build's data
// migrations and the keyring's seams, the dry run has no step to run, export
// opens no sealed row and purge refuses a sealed row and a store without
// core/audit (it is audited, fail-closed).

const migrateLong = `Reports what the pending data migrations would do, without doing it.
--dry-run is required: this build has no live storage migrate.

It opens the store read-only (no WAL flush, no format change, no write),
copies it into a new directory of the checkpoint area's scratch/ (tables
hard-linked, the other engine files copied), opens the copy as a full store
and runs the cutover preflight, then every pending step, on the copy. It
prints the blockers first, then per step the counts per outcome, the held
rows, the rows it would quarantine with their reasons, the bytes written and
the first keys per outcome, never a value. The copy is deleted whatever the
outcome. --from-archive copies the store from a backup archive instead.
--estimate skips the copy and counts over the live store with every write
discarded: an estimate, labelled as one.

It runs with the service stopped (it refuses while the service holds the
store or a swap is staged). It needs free space for the copy: the store's
size, twice when the list holds the store rewrite (M8), plus the margin.
A blocked cutover exits with the blockers listed (storage cutover-blockers).`

const quarantineListLong = `Lists the cutover's quarantined rows (c/storage/quarantine/): per row its
step, number, source key, reason, size, whether it was sealed as sensitive,
the build and the time. Never a value: storage quarantine export writes
those. --step limits it to one step.

It opens the store read-only, so it runs with the service stopped.`

const quarantineExportLong = `Writes the cutover's quarantined rows with their values as JSON Lines to
--out, a new file only its owner can read (mode 0600; it refuses a file that
exists). A sensitive row's value is opened from its sealed secret record
through the keyring. --step limits it to one step.

It opens the store read-only, so it runs with the service stopped.`

const quarantinePurgeLong = `Deletes one step's quarantined rows (--step) and the sealed secret record
of each sensitive one, in one batch with the core.storage.quarantine_purge
audit event, and commits it durably. --confirm is required: the rows are
gone afterwards, so export them first if anyone may need them.

It runs only with the service stopped (it refuses while the service holds
the store) and only as the state directory's owner.`

// migrateCommand is storage migrate --dry-run (kernel.DryRun).
func migrateCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage migrate",
		Short: "Report what the pending data migrations would do, on a scratch copy, with the service stopped",
		Long:  migrateLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("storage migrate", flag.ContinueOnError)
			dry := fl.Bool("dry-run", false, "run on a scratch copy and report (required)")
			id := fl.String("id", "", "only this data migration and those it runs after")
			archive := fl.String("from-archive", "", "copy the store from this backup archive instead of the live store")
			estimate := fl.Bool("estimate", false, "count over the live store with every write discarded, no copy")
			if help, err := parseFlags(fl, args, stdout, "storage migrate --dry-run [--id <step>] [--from-archive <file> | --estimate]", migrateLong); help || err != nil {
				return err
			}
			if !*dry {
				return errors.New("storage migrate: --dry-run is required (this build has no live storage migrate)")
			}
			if *estimate && *archive != "" {
				return errors.New("storage migrate: --estimate counts over the live store; it does not take --from-archive")
			}
			c, err := offlineConfig(opts, mods)
			if err != nil {
				return err
			}
			path, err := absPath(*archive)
			if err != nil {
				return err
			}
			rep, runErr := kernel.DryRun(ctx, c, kernel.DryRunOptions{ID: *id, FromArchive: path, Estimate: *estimate})
			if rep != nil {
				if _, err := io.WriteString(stdout, strings.Join(rep.Lines(), "\n")+"\n"); err != nil {
					return errors.Join(runErr, err)
				}
			}
			if runErr != nil {
				return fmt.Errorf("storage migrate: %w", runErr)
			}
			return nil
		},
	}
}

// quarantineListCommand is storage quarantine list.
func quarantineListCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage quarantine list",
		Short: "List the cutover's quarantined rows (never a value), with the service stopped",
		Long:  quarantineListLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("storage quarantine list", flag.ContinueOnError)
			step := fl.String("step", "", "only this step's rows")
			if help, err := parseFlags(fl, args, stdout, "storage quarantine list [--step <id>]", quarantineListLong); help || err != nil {
				return err
			}
			o, err := openKernelOffline(ctx, opts, mods, true)
			if err != nil {
				return err
			}
			entries, err := o.QuarantineList(ctx, *step)
			if err = errors.Join(err, o.Close()); err != nil {
				return fmt.Errorf("storage quarantine list: %w", err)
			}
			return writeQuarantineList(stdout, entries)
		},
	}
}

// writeQuarantineList prints entries, one line each, never a value.
func writeQuarantineList(w io.Writer, entries []storage.QuarantineEntry) error {
	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("No quarantined rows.\n")
	}
	for _, e := range entries {
		r := e.Record
		size := len(r.Value)
		fmt.Fprintf(&b, "step=%s n=%d key=%s reason=%q sensitive=%t sealed=%t size=%d build=%s at=%s\n",
			r.Step, e.N, storage.PrintableKey(r.Key), r.Reason, r.Sensitive, e.Sealed(), size, r.Build, r.At.Format("2006-01-02T15:04:05Z07:00"))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// quarantineExportCommand is storage quarantine export.
func quarantineExportCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage quarantine export",
		Short: "Write the cutover's quarantined rows with their values to a 0600 file, with the service stopped",
		Long:  quarantineExportLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) (err error) {
			fl := flag.NewFlagSet("storage quarantine export", flag.ContinueOnError)
			out := fl.String("out", "", "the file to write (created with mode 0600; must not exist)")
			step := fl.String("step", "", "only this step's rows")
			if help, err := parseFlags(fl, args, stdout, "storage quarantine export --out <file> [--step <id>]", quarantineExportLong); help || err != nil {
				return err
			}
			if strings.TrimSpace(*out) == "" {
				return errors.New("storage quarantine export: --out is required")
			}
			o, err := openKernelOffline(ctx, opts, mods, true)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, o.Close()) }()
			f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("storage quarantine export: %w", err)
			}
			// Nothing unseals until F3-07 hands the keyring's unsealer in: a
			// sealed row fails the export and the file is removed.
			n, err := o.QuarantineExport(ctx, *step, nil, f)
			if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
				return errors.Join(fmt.Errorf("storage quarantine export: %w", err), os.Remove(*out))
			}
			_, err = fmt.Fprintf(stdout, "Exported %d quarantined rows to %s (mode 0600). It holds the rows' values: keep it private.\n", n, *out)
			return err
		},
	}
}

// quarantinePurgeCommand is storage quarantine purge.
func quarantinePurgeCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage quarantine purge",
		Short: "Delete one step's quarantined rows and their sealed secrets, audited, with the service stopped",
		Long:  quarantinePurgeLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) (err error) {
			fl := flag.NewFlagSet("storage quarantine purge", flag.ContinueOnError)
			step := fl.String("step", "", "the step whose rows to delete (required)")
			confirm := fl.Bool("confirm", false, "delete them (required)")
			if help, err := parseFlags(fl, args, stdout, "storage quarantine purge --step <id> --confirm", quarantinePurgeLong); help || err != nil {
				return err
			}
			if strings.TrimSpace(*step) == "" {
				return errors.New("storage quarantine purge: --step is required")
			}
			if !*confirm {
				return errors.New("storage quarantine purge: --confirm is required; the rows are deleted (export them first)")
			}
			o, err := openKernelOffline(ctx, opts, mods, false)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, o.Close()) }()
			// The secret deleter is the keyring's, wired by F3-07; until then
			// a sealed row refuses the purge before anything is deleted.
			n, err := o.QuarantinePurge(ctx, *step, nil, operatorName())
			if err != nil {
				return fmt.Errorf("storage quarantine purge: %w", err)
			}
			_, err = fmt.Fprintf(stdout, "Purged %d quarantined rows of %s.\n", n, *step)
			return err
		},
	}
}

// openKernelOffline is kernel.OpenOffline over the app's configuration.
func openKernelOffline(ctx context.Context, opts Options, mods []module.Module, readOnly bool) (*kernel.Offline, error) {
	c, err := offlineConfig(opts, mods)
	if err != nil {
		return nil, err
	}
	return kernel.OpenOffline(ctx, c, readOnly)
}

// operatorName names the operator for the purge's audit event: the OS user
// running the command, or its uid when the name does not resolve.
func operatorName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "uid " + strconv.Itoa(os.Getuid())
}
