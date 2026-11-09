// file: cmd/transcode_skip.go
// version: 1.0.0
// guid: 6e2f9b40-3c1d-4a8e-b7f5-91d0c4a2e835
// last-edited: 2026-11-09

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/falkcorp/audiobook-organizer/internal/remux"
)

// transcodeSkipCmd marks files the malformed-M4B transcode pass must skip
// for good. It replaces the cmd/pebble-inject-skip tool (R1-25), which wrote
// the same rows with a raw Pebble open: past core storage's guard and the
// swap slot, and able to create an empty store at a path a half-applied
// restore had just moved away. This command writes through openCLIStore
// (app.OpenOffline) and store.SetSetting, so it refuses a store the service
// holds, one with a swap staged or in flight, and one never started.
var transcodeSkipCmd = &cobra.Command{
	Use:   "transcode-skip <file>...",
	Short: "Mark audio files the malformed-M4B transcode pass skips for good",
	Long: `Record a transcode skip for each file, so the malformed-M4B transcode
pass never tries it again. Run it with the service stopped. The file paths
are the ones the library records for each book file.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runTranscodeSkip,
}

func init() {
	rootCmd.AddCommand(transcodeSkipCmd)
}

// runTranscodeSkip writes one transcode_skip_<hash> = "true" setting per
// file through the offline open, and prints each key it set.
func runTranscodeSkip(cmd *cobra.Command, files []string) error {
	store, closeCLIStore, err := openCLIStore(commandContext(cmd), false)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer closeCLIStore()
	for _, f := range files {
		key := remux.TranscodeSkipKey(f)
		if err := store.SetSetting(key, "true", "bool", false); err != nil {
			return fmt.Errorf("set %s for %s: %w", key, f, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "set %s for %s\n", key, f)
	}
	return nil
}
