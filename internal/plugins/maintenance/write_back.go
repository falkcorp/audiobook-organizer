// file: internal/plugins/maintenance/write_back.go
// version: 1.0.0
// guid: d0e1f2a3-b4c5-6789-3456-901234567890
// last-edited: 2026-05-07

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Hard rule: bulk-write-back = ResumeAsk (file writes; operator must confirm).

// BulkWriteBackParams are the JSON parameters for bulk write-back.
type BulkWriteBackParams struct {
	BookIDs  []string `json:"book_ids"`
	Rename   bool     `json:"rename"`
	StartIdx int      `json:"start_idx"` // checkpoint resume index
}

func (p *Plugin) bulkWriteBackDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.bulk-write-back",
		Liveness: sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Bulk write-back",
		Description:     "Writes metadata tags back to files for a set of books. Interrupted runs surface in UI for operator confirmation before resuming.",
		ResumePolicy:    sdk.ResumeAsk,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.bulk-write-back",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         240 * time.Minute,
		Schedule:        nil,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesRead, sdk.CapFilesWrite,
		},
		Run: p.runBulkWriteBack,
	}
}

func (p *Plugin) runBulkWriteBack(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params BulkWriteBackParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	if len(params.BookIDs) == 0 {
		_ = reporter.Log(slog.LevelInfo, "No book IDs specified, nothing to do")
		return nil
	}
	opID := ctxOpID(ctx)
	return p.deps.RunBulkWriteBack(ctx, opID, params.BookIDs, params.Rename, params.StartIdx, newOpsAdapter(reporter))
}
