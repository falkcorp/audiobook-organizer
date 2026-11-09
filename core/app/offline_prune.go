// file: core/app/offline_prune.go
// version: 1.0.1
// guid: 7435e2a1-aaaa-450e-96a6-35082ffd5d60
// last-edited: 2026-11-08

package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// pruneLong is storage prune-checkpoints' full help.
const pruneLong = `Deletes the store's checkpoints now instead of 7 days after they stop being
needed: every completed checkpoint, every released one (an open checkpoint
that is no longer the live store's rollback) and every checkpoint a failed
restore set aside. A checkpoint is a full copy of the database, saved
passwords and sign-in tokens included.

An open checkpoint that is still the live store's rollback (an upgrade that
failed, which the previous build is serving from) is kept unless
--include-open --confirm is given: deleting it removes the only way back
from that upgrade.

It opens no store, so it runs with the service stopped or while it serves.
It refuses while a restore is staged or in flight.`

// pruneCheckpointsCommand is storage prune-checkpoints (05-storage §5S.3.12
// "Retention"; 06 §6.4.5): storage.PruneCheckpoints over the app's store
// path and state directory, as New would resolve them.
func pruneCheckpointsCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage prune-checkpoints",
		Short: "Delete checkpoints before their automatic deletion 7 days after they stop being needed",
		Long:  pruneLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fs := flag.NewFlagSet("storage prune-checkpoints", flag.ContinueOnError)
			// The flag package's own usage and error lines are not written:
			// a parse error is returned, and help is written below, so a
			// failed write to stdout is an error too.
			fs.SetOutput(io.Discard)
			fs.Usage = func() {}
			includeOpen := fs.Bool("include-open", false, "also delete open checkpoints, the live store's rollback among them (needs --confirm)")
			confirm := fs.Bool("confirm", false, "confirm --include-open")
			if err := fs.Parse(args); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					var defaults bytes.Buffer
					fs.SetOutput(&defaults)
					fs.PrintDefaults()
					_, err := fmt.Fprintf(stdout, "Usage: storage prune-checkpoints [--include-open --confirm]\n\n%s\n\nFlags:\n%s", pruneLong, defaults.String())
					return err
				}
				return fmt.Errorf("storage prune-checkpoints: %w", err)
			}
			switch {
			case fs.NArg() > 0:
				return fmt.Errorf("storage prune-checkpoints: unexpected arguments %q", fs.Args())
			case *includeOpen && !*confirm:
				return errors.New("storage prune-checkpoints: --include-open deletes the live store's rollback too; add --confirm to proceed")
			case *confirm && !*includeOpen:
				return errors.New("storage prune-checkpoints: --confirm confirms --include-open and does nothing alone")
			}
			return pruneCheckpoints(ctx, opts, mods, *includeOpen, stdout)
		},
	}
}

// pruneCheckpoints resolves the app's store path and state directory and
// prunes, writing one line per checkpoint deleted or kept.
func pruneCheckpoints(ctx context.Context, opts Options, mods []module.Module, includeOpen bool, stdout io.Writer) error {
	dbPath, stateDir, err := storagePath(opts, mods)
	if err != nil {
		return err
	}
	if dbPath == "" {
		return errors.New("storage prune-checkpoints: no module of this app declares storage, so it has no checkpoints")
	}
	res, err := storage.PruneCheckpoints(ctx, storage.PruneOptions{
		Path: dbPath, StateDir: stateDir, IncludeOpen: includeOpen, Logger: opts.Logger,
	})
	// What was deleted before a failure is reported with it.
	var report strings.Builder
	for _, n := range res.Deleted {
		fmt.Fprintf(&report, "deleted checkpoint %s\n", n)
	}
	if err == nil {
		for _, n := range res.KeptOpen {
			fmt.Fprintf(&report, "kept open checkpoint %s: it is the live store's rollback; --include-open --confirm deletes it\n", n)
		}
		if len(res.Deleted)+len(res.KeptOpen) == 0 {
			report.WriteString("no checkpoints to delete\n")
		}
	}
	_, werr := io.WriteString(stdout, report.String())
	if err != nil {
		return errors.Join(fmt.Errorf("storage prune-checkpoints: %w", err), werr)
	}
	return werr
}
