// file: internal/books/services.go
// version: 0.1.0
// guid: f7e34fd7-50ee-457e-93dc-7cc281bd2716
// last-edited: 2026-11-28

package books

import (
	"fmt"

	"github.com/falkcorp/mediacore/module"

	"github.com/falkcorp/audiobook-organizer/internal/plugins/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// taskInputsFrom is bookTaskLists' inputs over a built server and the module's
// handles: what only the server holds comes from srv.TaskInputs, what core owns
// (the run store, the planned-row reader) from deps.TaskRows, and the schedule
// hook from the effective config and the server's live checks.
//
// The run-store handle is core's own-kinds-only view (module.TaskRows), never the
// wide RunStore: it reads and writes the runs of this module's registered kinds
// and refuses every other (core/kernel/module_tasks.go). It satisfies the
// maintenance package's CreditsRunStore and the repairs package's PlannedRows
// as it is, so no adapter widens it.
//
// Every field left unset is named here, with the reason, so a reader does not
// have to compare this with the plugins' input structs:
//
//   - Maintenance.Scan: HEAD's degraded caller (no scanner to stand down). A
//     native kind stands the scan down through rc.StandDown over StandDownKey.
//   - ITunes: the iTunes plugin's definitions take no inputs but the schedule
//     (they are the import-only kinds, F5-18 A6).
//   - Acoustid.Candidates and Acoustid.NewEngine: server.TaskInputs says why.
func taskInputsFrom(deps module.Deps, srv *server.Server) (taskListInputs, error) {
	if deps.TaskRows == nil {
		return taskListInputs{}, fmt.Errorf("books: Deps.TaskRows is not bound: the kernel's tasks switch is off")
	}
	si, err := srv.TaskInputs()
	if err != nil {
		return taskListInputs{}, fmt.Errorf("books: %w", err)
	}
	hooks := srv.ScheduleHooks()
	in := taskListInputs{
		Schedules: NewSchedules(ScheduleDeps{
			Config:                    deps.Config,
			Log:                       deps.Log,
			HasDedupPlugin:            hooks.HasDedupPlugin,
			HasActivitySvc:            hooks.HasActivitySvc,
			HasMetadataFetchSvc:       hooks.HasMetadataFetchSvc,
			IdentificationPending:     hooks.IdentificationPending,
			CollectionsRefreshPending: hooks.CollectionsRefreshPending,
		}),
		Maintenance: si.Maintenance,
		Metafetch:   si.Metafetch,
		Deluge:      si.Deluge,
		ITunes:      itunes.BooksTaskInputs{},
		Acoustid:    si.Acoustid,
		Scheduler:   si.Scheduler,
		Repairs:     si.Repairs,
	}
	in.Maintenance.Runs = deps.TaskRows
	in.Maintenance.StandDownKey = scanStandDownKey
	in.Scheduler.StandDownKey = scanStandDownKey
	in.Repairs.PlanRows = deps.TaskRows
	in.Repairs.StandDownKey = scanStandDownKey
	return in, nil
}
