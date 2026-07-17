// file: internal/plugins/itunes/import.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-07-17

package itunes

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

//nolint:unused // operation definition stub for plugin registration
func (p *Plugin) importDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:                    "itunes.import",
		Plugin:                "itunes",
		DisplayName:           "iTunes Library Import",
		Description:           "Import audiobooks from the iTunes/Music library into the organizer.",
		Isolate:               false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		ResumePolicy:          sdk.ResumeRestart,
		DefaultPriority:       sdk.PriorityNormal,
		Cancellable:           true,
		Timeout:               240 * time.Minute,
		ConcurrencyKey:        "itunes.import",
		MinCheckpointInterval: 30 * time.Second,
		Run:                   p.runImport,
	}
}

//nolint:unused // no-op run stub for future itunes.import operation
func (p *Plugin) runImport(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// TODO: Implement iTunes import operation.
	// This should handle parameterized imports (genre, selection, etc.).
	// NOTE: the canonical itunes.import op is registered by
	// server.RegisterITunesImportOp (itunes_ops.go); this stub is
	// intentionally unregistered (see Register in plugin.go).
	_ = reporter.Log(slog.LevelWarn, "op not implemented — no-op", slog.String("def_id", "itunes.import"))
	return nil
}

// Ensure methods are referenced so staticcheck doesn't flag them as unused (U1000).
var _ = []interface{}{(*Plugin).importDef, (*Plugin).runImport}
