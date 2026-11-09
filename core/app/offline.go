// file: core/app/offline.go
// version: 1.0.0
// guid: 418dd945-667d-43dc-a795-d22188dd4b58
// last-edited: 2026-11-08

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
// server runs (SEC10-9); storage restore-offline and storage reset join it
// with R1-25, and R2's flips add theirs (06 §6.4.10).
func OfflineCommands(opts Options, mods ...module.Module) []Command {
	return []Command{pruneCheckpointsCommand(opts, mods)}
}
