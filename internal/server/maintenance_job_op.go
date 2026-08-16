// file: internal/server/maintenance_job_op.go
// version: 1.2.0
// guid: 7f3a9c21-4b8e-4d56-a123-0e5f6c7d8e9f
// last-edited: 2026-05-16

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type maintenanceJobOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	JobID      string `json:"job_id"`
	DryRun     bool   `json:"dry_run"`
}

// RegisterMaintenanceJobOp registers the "maintenance.job" OperationDef which runs
// any named maintenance job by ID.
func (s *Server) RegisterMaintenanceJobOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "maintenance.job",
		Liveness: opsregistry.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Maintenance Job",
		Description:     "Run a named maintenance job.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "", // parallel maintenance jobs allowed
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p maintenanceJobOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("maintenance.job: decode params: %w", err)
			}
			job, err := maintenance.Get(p.JobID)
			if err != nil {
				return fmt.Errorf("maintenance.job: job %q not found: %w", p.JobID, err)
			}
			store := s.Store()
			ctx = maintenance.WithOperationID(ctx, p.LegacyOpID)
			progress := registryProgressAdapter{r: reporter}
			adapter := &maintenance.ProgressAdapter{Ops: progress}

			// Execute the job synchronously in this Run closure.
			runErr := job.Run(ctx, store, adapter, p.DryRun)

			// Emit an activity summary if an activity writer is available and a legacy
			// operation ID was provided. Prefer any saved OperationSummaryLog created
			// by the job; fall back to the job name.
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				if sum, serr := store.GetOperationSummaryLog(p.LegacyOpID); serr == nil && sum != nil {
					if sum.Result != nil {
						activity.EmitInfo(s.activityWriter, p.LegacyOpID, job.ID(), job.ID(), *sum.Result, activity.AlwaysShow)
					} else {
						activity.EmitInfo(s.activityWriter, p.LegacyOpID, job.ID(), job.ID(), job.Name(), activity.AlwaysShow)
					}
				} else {
					activity.EmitInfo(s.activityWriter, p.LegacyOpID, job.ID(), job.ID(), job.Name(), activity.AlwaysShow)
				}
			}

			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterMaintenanceJobOp(reg) })
}
