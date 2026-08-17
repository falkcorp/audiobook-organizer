// file: internal/server/scheduler_maintenance_window_op.go
// version: 1.3.0
// guid: 2a4b6c8d-0e1f-2a3b-4c5d-6e7f8a9b0c1d
// last-edited: 2026-08-16

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// RegisterMaintenanceWindowOp registers the "maintenance.window" OperationDef which
// runs all maintenance-window-eligible tasks in order, respecting the configured
// maintenance window unless IgnoreWindow is set.
func (s *Server) RegisterMaintenanceWindowOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "maintenance.window",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Maintenance Window",
		Description:     "Run all maintenance-window-eligible tasks in order.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         12 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "maintenance.window",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p scheduler.MaintenanceWindowOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("maintenance.window: decode params: %w", err)
			}

			// Activity-log correlation id. This run's OWN id is preferred: the
			// scheduler no longer creates a legacy operations row, so LegacyOpID
			// is empty for anything enqueued after 2026-08-16 and non-empty only
			// for runs still in flight from before. Tagging with the v2 id also
			// fixes what the legacy id never did — the entries now point at an
			// operation that can actually be looked up.
			opID := opsregistry.ReporterOpID(reporter)
			if opID == "" {
				opID = p.LegacyOpID
			}
			ignoreWindow := p.IgnoreWindow
			ts := s.scheduler

			store := s.Store()
			if store == nil {
				return fmt.Errorf("maintenance.window: database not initialized")
			}
			if ts == nil {
				return fmt.Errorf("maintenance.window: scheduler not initialized")
			}

			progress := registryProgressAdapter{r: reporter}

			// Step 1: Auto-update (if enabled and not already completed post-restart)
			if config.AppConfig.AutoUpdate.Enabled {
				updateDone, _ := store.GetSetting("maintenance_window_update_completed")
				today := time.Now().Format("2006-01-02")
				if updateDone == nil || updateDone.Value != today {
					_ = progress.Log("info", "Running auto-update (step 1)", nil)
					// Auto-update is a single bounded step (0/1 → done).
					_ = reporter.UpdateProgress(0, 1, "Running auto-update... (0/1 0%)")
					_ = store.SetSetting("maintenance_window_update_completed", today, "string", false)
					if s.updater != nil {
						channel := config.AppConfig.AutoUpdate.Channel
						info, checkErr := s.updater.CheckForUpdate(channel)
						if checkErr != nil {
							_ = progress.Log("warning", fmt.Sprintf("Auto-update check failed: %v", checkErr), nil)
						} else if info != nil && info.UpdateAvailable {
							_ = progress.Log("info", fmt.Sprintf("Update available: %s, applying...", info.LatestVersion), nil)
							if applyErr := s.updater.DownloadAndReplace(info); applyErr != nil {
								_ = progress.Log("error", fmt.Sprintf("Auto-update apply failed: %v", applyErr), nil)
							} else {
								_ = progress.Log("info", "Update applied, server will restart", nil)
								go s.updater.RestartSelf()
								return nil // Exit — server restarting
							}
						} else {
							_ = progress.Log("info", "No update available", nil)
						}
					}
					_ = progress.Log("info", "Auto-update step complete", nil)
				} else {
					_ = progress.Log("info", "Auto-update already completed today, skipping", nil)
				}
			}

			// Step 2+: Maintenance tasks in order
			var eligible []string
			tasks := ts.Tasks()
			for _, name := range ts.MaintenanceOrder() {
				task, ok := tasks[name]
				if !ok {
					continue
				}
				if task.IsEnabled() && task.RunInMaintenanceWindow != nil && task.RunInMaintenanceWindow() {
					eligible = append(eligible, name)
				}
			}

			mwTag := "mw:" + opID
			taskSource := operations.TriggerScheduled
			if ignoreWindow {
				taskSource = operations.TriggerManual
			}
			windowStartTags := []string{activity.Scheduled, mwTag}
			if ignoreWindow {
				windowStartTags = []string{activity.AlwaysShow, mwTag}
			}

			sp := sdk.NewProgress(reporter, len(eligible))
			sp.Start(fmt.Sprintf("Maintenance window starting: %d tasks eligible", len(eligible)))
			_ = progress.Log("info", fmt.Sprintf("Maintenance window starting: %d tasks eligible: %s", len(eligible), strings.Join(eligible, ", ")), nil)
			activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, "maintenance-window",
				fmt.Sprintf("Maintenance window starting: %d tasks: %s", len(eligible), strings.Join(eligible, ", ")),
				windowStartTags...)

			type taskFailure struct{ name, errMsg string }
			var failures []taskFailure
			var skipped []string
			ran := 0

			for i, name := range eligible {
				// Check if window is still open (skip for manual "Run Now" triggers)
				if !ignoreWindow && !scheduler.IsInMaintenanceWindow() {
					remaining := eligible[i:]
					_ = progress.Log("warning", fmt.Sprintf("Maintenance window closed after task %d/%d, skipping: %s", i, len(eligible), strings.Join(remaining, ", ")), nil)
					skipped = append(skipped, remaining...)
					break
				}

				// Duplicate prevention: skip if already running from interval ticker
				if ts.IsTaskRunning(name) {
					_ = progress.Log("info", fmt.Sprintf("Task %s already running (interval), skipping", name), nil)
					skipped = append(skipped, name)
					continue
				}

				sp.StepN(i, fmt.Sprintf("Running task %d/%d: %s", i+1, len(eligible), name))
				_ = progress.Log("info", fmt.Sprintf("Starting maintenance task: %s", name), nil)
				ran++

				taskOp, taskErr := ts.RunTaskWithSource(name, taskSource)
				if taskErr != nil {
					errMsg := taskErr.Error()
					failures = append(failures, taskFailure{name, errMsg})
					_ = progress.Log("error", fmt.Sprintf("Task %s failed to start: %v", name, taskErr), nil)
					activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, name,
						fmt.Sprintf("Task %s failed to start: %s", name, errMsg),
						activity.Scheduled, mwTag)
				} else if taskOp != nil {
					// Wait for the task operation to complete before starting next.
					//
					// Heartbeat on every poll tick. This op reports once per task,
					// and a single maintenance task routinely runs longer than the
					// 5m ProgressTimeout, so a supervisor that is waiting normally
					// is indistinguishable from one that is wedged. 28 of the 44
					// stuck-op cancellations in the 30 days to 2026-08-16 were
					// this op. This mechanism explains that shape; whether each of
					// the 28 was individually healthy was not verified.
					//
					// The child has its own watchdog entry, so a genuinely hung
					// task is still caught; see WaitForOperation's doc comment.
					ts.WaitForOperation(ctx, taskOp.ID, func(child *database.Operation) {
						_ = reporter.UpdateProgress(i, len(eligible),
							fmt.Sprintf("Task %d/%d: %s — %s (%d%%)",
								i+1, len(eligible), name, child.Message, child.Progress))
					})
					completedOp, _ := store.GetOperationByID(taskOp.ID)
					if completedOp != nil && completedOp.Status == "failed" {
						errMsg := ""
						if completedOp.ErrorMessage != nil {
							errMsg = *completedOp.ErrorMessage
						}
						failures = append(failures, taskFailure{name, errMsg})
						_ = progress.Log("warning", fmt.Sprintf("Task %s operation failed: %s", name, errMsg), nil)
						activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, name,
							fmt.Sprintf("Task %s failed: %s", name, errMsg),
							windowStartTags...)
					} else {
						msg := completedOp.Message
						_ = progress.Log("info", fmt.Sprintf("Task %s completed: %s (op: %s)", name, msg, taskOp.ID), nil)
						activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, name,
							fmt.Sprintf("Task %s ok: %s", name, msg),
							windowStartTags...)
					}
				} else {
					_ = progress.Log("info", fmt.Sprintf("Task %s triggered (no operation)", name), nil)
					activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, name,
						fmt.Sprintf("Task %s triggered", name),
						windowStartTags...)
				}
			}

			summaryParts := []string{fmt.Sprintf("%d/%d tasks ran", ran, len(eligible))}
			if len(failures) > 0 {
				failNames := make([]string, len(failures))
				for i, f := range failures {
					if f.errMsg != "" {
						failNames[i] = f.name + ": " + f.errMsg
					} else {
						failNames[i] = f.name
					}
				}
				summaryParts = append(summaryParts, fmt.Sprintf("%d failed: %s", len(failures), strings.Join(failNames, "; ")))
			}
			if len(skipped) > 0 {
				summaryParts = append(summaryParts, fmt.Sprintf("%d skipped: %s", len(skipped), strings.Join(skipped, ", ")))
			}
			summary := strings.Join(summaryParts, ", ")

			if len(failures) > 0 {
				sp.Done("Maintenance window completed with errors")
				activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, "maintenance-window",
					"Maintenance window done (errors): "+summary,
					windowStartTags...)
				return fmt.Errorf("maintenance window: %s", summary)
			}
			sp.Done("Maintenance window completed successfully")
			activity.EmitInfo(s.activityWriter, opID, activity.MaintenanceWindow, "maintenance-window",
				"Maintenance window done: "+summary,
				windowStartTags...)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterMaintenanceWindowOp(reg) })
}
