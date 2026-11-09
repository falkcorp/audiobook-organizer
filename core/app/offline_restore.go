// file: core/app/offline_restore.go
// version: 1.0.1
// guid: 10f91970-5117-4c65-b504-d2c27759490c
// last-edited: 2026-11-09

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

const restoreOfflineLong = `Restores the store with the service stopped, without booting the app.
Stop the service first: the command refuses while
another process holds the store, and while a restore or reset is in flight
(boot the current build once to finish it), except a failed checkpoint
restore, which the previous binary's --checkpoint restore finishes.

  --checkpoint <name>  the checkpoint of the store's latest upgrade attempt
  --archive <file>     an archive (this app's backup format)
  --into <dir>         copy the source into a new directory inside the
                       checkpoint scratch area instead of replacing the store,
                       with the copy's own <dir>.storage-levels beside it
  --accept-loss        restoring a served or completed checkpoint, or an
                       archive, discards every write made since it was taken

In place, the live store and the state files the source replaces are set
aside (restore/<id>/previous/ and .mediacore-restore/<id>/previous/), not
deleted. A pending restore or reset is superseded.`

// restoreOfflineCommand is storage restore-offline.
func restoreOfflineCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage restore-offline",
		Short: "Restore a checkpoint or an archive with the service stopped",
		Long:  restoreOfflineLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("storage restore-offline", flag.ContinueOnError)
			checkpoint := fl.String("checkpoint", "", "the latest upgrade attempt's checkpoint, by name")
			archive := fl.String("archive", "", "an archive file")
			into := fl.String("into", "", "a new directory inside the checkpoint scratch area")
			acceptLoss := fl.Bool("accept-loss", false, "discard every write made since the source was taken")
			if help, err := parseFlags(fl, args, stdout,
				"storage restore-offline (--checkpoint <name> | --archive <file>) [--into <dir>] [--accept-loss]", restoreOfflineLong); help || err != nil {
				return err
			}
			req := kernel.RestoreOfflineRequest{Checkpoint: *checkpoint, AcceptLoss: *acceptLoss, RequestedBy: "cli:storage restore-offline"}
			var err error
			if req.Archive, err = absPath(*archive); err != nil {
				return err
			}
			if req.Into, err = absPath(*into); err != nil {
				return err
			}
			c, err := offlineConfig(opts, mods)
			if err != nil {
				return err
			}
			res, err := kernel.RestoreOffline(ctx, c, req)
			// The warnings are reported with a failure too; a failed write
			// to stdout is an error, joined to the restore's.
			var report strings.Builder
			for _, w := range res.Warnings {
				report.WriteString("Warning: " + w + "\n")
			}
			if err == nil {
				switch {
				case res.Into != "":
					fmt.Fprintf(&report, "Copied %s into %s (state files in %s).\n", res.Source, res.Into, res.IntoState)
				case res.Escaped:
					fmt.Fprintf(&report, "Finished the failed checkpoint restore %s with %s; start the service.\n", res.ID, res.Source)
				default:
					if res.Superseded != "" {
						fmt.Fprintf(&report, "Rejected the pending swap %s (superseded by this restore).\n", res.Superseded)
					}
					fmt.Fprintf(&report, "Restored %s (journal %s); start the service.\n", res.Source, res.ID)
				}
			}
			_, werr := io.WriteString(stdout, report.String())
			return errors.Join(err, werr)
		},
	}
}
