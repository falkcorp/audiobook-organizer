// file: internal/server/library_writeback_op.go
// version: 1.2.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	ulid "github.com/oklog/ulid/v2"
)

// bulkWriteBackOpParams is the JSON params for the library.bulk-write-back op.
type bulkWriteBackOpParams struct {
	BookIDs []string `json:"book_ids"`
	Rename  bool     `json:"rename"`
}

// RegisterBulkWriteBackOp registers the "library.bulk-write-back" v2 OperationDef.
// The HTTP handler handleBulkWriteBack pre-filters books and passes the resulting
// book IDs as params; the Run func executes the actual tag-write work.
func (s *Server) RegisterBulkWriteBackOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.bulk-write-back",
		Plugin:          "library",
		DisplayName:     "Bulk Tag Write-back",
		Description:     "Write metadata from the database back to audio file tags for a set of audiobooks.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		ResumePolicy:    opsregistry.ResumeRestart,
		ConcurrencyKey:  "library.bulk-write-back",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p bulkWriteBackOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("bulk-write-back: decode params: %w", err)
				}
			}
			if len(p.BookIDs) == 0 {
				return nil
			}
			opID := ulid.Make().String()
			progress := registryProgressAdapter{r: reporter}
			runErr := s.runBulkWriteBack(ctx, opID, p.BookIDs, p.Rename, 0, progress)
			if s.activityWriter != nil {
				activity.FlushOperation(s.activityWriter, opID)
				summary := fmt.Sprintf("Bulk tag write-back completed for %d books", len(p.BookIDs))
				if runErr != nil {
					summary = fmt.Sprintf("Bulk tag write-back failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, opID, "library.bulk-write-back", "library", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBulkWriteBackOp(reg) })
}
