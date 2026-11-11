// file: core/app/offline.go
// version: 1.0.3
// guid: 418dd945-667d-43dc-a795-d22188dd4b58
// last-edited: 2026-11-10

package app

import (
	"context"
	"io"

	"github.com/falkcorp/mediacore/module"
)

// Command is one of core's offline commands, for the app's own CLI to wrap
// (06 §6.4.5): ABO makes a cobra command of each, so core's offline
// commands are reachable from its CLI without an import of kernel (MOD2-4,
// R9-9, COR9-6).
type Command struct {
	Name  string // space-separated path, e.g. "storage prune-checkpoints"
	Short string // one-line help
	Long  string // full help, including whether the service must be stopped
	// Run runs the command with its arguments after the path (flags
	// included: the wrapper parses none) and writes its report to stdout.
	Run func(ctx context.Context, args []string, stdout io.Writer) error
}

// OfflineCommands returns core's offline commands for the app's own CLI,
// which wraps each one (06 §6.4.5). A command whose owner has not landed is
// absent (R9-9, COR9-6), so at this build the list holds storage
// prune-checkpoints (R1-24), which opens no store and works while the
// server runs (SEC10-9), and storage restore-offline (05-storage §5S.3.12)
// and storage reset (§5S.3.15) (R1-25), which run with the service stopped
// through the kernel's own swap path, so none needs an import of kernel or
// core/internal/restoreswap; then F3's three secrets commands (05-config
// §2.4; F3-03): secrets export-keyring, which opens no store and works while
// the server runs, and secrets rotate-derive and secrets set-install-id,
// which go through kernel.OpenOffline with the service stopped; and config
// check (05-config §1.3.6; F3-02), which reads only the operator layers,
// opens no store and works while the server runs. R2's other flips add
// theirs (06 §6.4.10).
func OfflineCommands(opts Options, mods ...module.Module) []Command {
	return []Command{
		pruneCheckpointsCommand(opts, mods), restoreOfflineCommand(opts, mods), resetCommand(opts, mods),
		exportKeyringCommand(opts, mods), rotateDeriveCommand(opts, mods), setInstallIDCommand(opts, mods),
		configCheckCommand(opts, mods),
	}
}
