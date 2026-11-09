// file: core/app/offline_reset.go
// version: 1.0.0
// guid: 452027d9-8145-4e24-87f4-b5d83dc57d89
// last-edited: 2026-11-08

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
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const resetLong = `Stages a factory reset or a store reset with the service stopped;
it applies at the next start of the service. This
release resets only from the command line.

  --kind factory|store    factory (the default) also resets the app's own
                          files through its reset hook; store resets the
                          database and its settings only
  --confirm RESET         required
  --delete-library-files  factory only: also delete the files under the
                          library root, confirmed with --library-root and
                          --library-file-count (the command prints both when
                          they are missing or differ)

The database and its settings are set aside, not deleted: moving them back
undoes the reset, but not the library files or Open Library data it
deleted. A reset leaves config.yaml beside the database unchanged; delete it
by hand for a full reset.`

// resetCommand is storage reset.
func resetCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "storage reset",
		Short: "Stage a factory or store reset with the service stopped",
		Long:  resetLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("storage reset", flag.ContinueOnError)
			kind := fl.String("kind", string(restorejournal.ResetFactory), "factory or store")
			confirm := fl.String("confirm", "", "must be RESET")
			deleteFiles := fl.Bool("delete-library-files", false, "also delete the files under the library root (factory only)")
			root := fl.String("library-root", "", "the library root the wipe deletes under, as the command printed it")
			count := fl.Int("library-file-count", 0, "the number of entries under the library root, as the command printed it")
			if help, err := parseFlags(fl, args, stdout,
				"storage reset --confirm RESET [--kind factory|store] [--delete-library-files --library-root <dir> --library-file-count <n>]", resetLong); help || err != nil {
				return err
			}
			req := storage.ResetRequest{Kind: restorejournal.ResetKind(strings.TrimSpace(*kind)), Confirm: *confirm,
				DeleteLibraryFiles: *deleteFiles, LibraryRoot: *root, LibraryFileCount: *count}
			c, err := offlineConfig(opts, mods)
			if err != nil {
				return err
			}
			staged, err := kernel.StageResetOffline(ctx, c, "cli:storage reset", req)
			// A failed write to stdout is an error, joined to the reset's.
			var report strings.Builder
			if lce, ok := errors.AsType[*storage.LibraryConfirmError](err); ok {
				fmt.Fprintf(&report, "The library root is %s with %d top-level entries. To delete them, rerun with:\n  --delete-library-files --library-root %q --library-file-count %d\n",
					lce.Root, lce.Files, lce.Root, lce.Files)
			}
			if err == nil {
				fmt.Fprintf(&report, "Staged the %s reset %s; it applies at the next start of the service.\n", req.Kind, staged.ID)
				if req.DeleteLibraryFiles {
					fmt.Fprintf(&report, "The files under %s (%d top-level entries) are deleted then.\n", staged.LibraryRoot, staged.LibraryFileCount)
				}
			}
			_, werr := io.WriteString(stdout, report.String())
			return errors.Join(err, werr)
		},
	}
}
