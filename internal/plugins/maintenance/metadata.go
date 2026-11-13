// file: internal/plugins/maintenance/metadata.go
// version: 1.8.0
// guid: a7b8c9d0-e1f2-3456-0123-678901234567
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// --- metadata-refresh ---

// metadataRefreshDef is the only registration of the metadata refresh scan.
// scheduler.metadata-refresh ran a second copy of the same read-only body under
// its own ID and ConcurrencyKey until 2026-10-11 (04-P4c); it resolves here as
// a FormerID, and the metadata_refresh scheduler task enqueues this def. The
// 4h Timeout is the longer budget the scheduled copy carried (this def had
// 120m), and Permissions is the gate that copy enforced, so the alias does not
// widen who can run it. ResumeRequeue stays: the scan writes nothing (it declares no CapLibraryWrite), so
// re-running it after a restart is safe. CapNetworkGeneric stays: the kept body
// only lists books and calls no OpenAI-backed service, so the loser's
// network.openai declaration gated nothing real.
func (p *Plugin) metadataRefreshDef() sdk.OperationDef {
	sched := "0 6 * * *" // 06:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.metadata-refresh",
		FormerIDs:       []string{"scheduler.metadata-refresh"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Metadata refresh scan",
		Description:     "Counts and logs books with incomplete metadata (no author or no title). Read-only: it fetches and changes nothing.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.metadata-refresh",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapNetworkGeneric},
		Run:             p.runMetadataRefresh,
	}
}

// metadataRefreshKind is the native kind of maintenance.metadata-refresh (F5-10).
const metadataRefreshKind tasks.Kind = "books.maintenance.metadata-refresh"

// runMetadataRefreshEnv is what runMetadataRefreshCore needs from its engine:
// the base and the scan stand-down hold.
type runMetadataRefreshEnv struct {
	t6g5Base
	// Hold takes the scan stand-down for the run (HEAD: registry.HoldScanStandDown;
	// native: rc.StandDown).
	Hold t6g5HoldFunc
}

// runMetadataRefresh is the v2 shell of maintenance.metadata-refresh.
func (p *Plugin) runMetadataRefresh(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	env := runMetadataRefreshEnv{t6g5Base: p.t6g5HeadBase(ctx, reporter, true), Hold: p.t6g5HeadHold(reporter)}
	_, err := runMetadataRefreshCore(ctx, struct{}{}, env)
	return err
}

// metadataRefreshKind builds the native kind. Declared ReadOnly: the body only
// lists books (HEAD declares no Writes and no CapLibraryWrite). HEAD's cron
// schedule is not carried (rule R5); F5-14 declares it.
func (p *Plugin) metadataRefreshKind(deps t6g5Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return tasks.Task(metadataRefreshKind, tasks.TaskSpec[struct{}, struct{}]{
		Common: t6g5Common(p.metadataRefreshDef(), metadataRefreshKind, tasks.ReadOnly(), sched),
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			base, err := t6g5NativeBase(metadataRefreshKind, rc, deps, false)
			if err != nil {
				return struct{}{}, err
			}
			env := runMetadataRefreshEnv{t6g5Base: base, Hold: t6g5NativeHold(rc, deps, base)}
			return runMetadataRefreshCore(rc.Context(), struct{}{}, env)
		},
	})
}

// runMetadataRefreshCore is the shared body of both shells.
func runMetadataRefreshCore(ctx context.Context, _ struct{}, env runMetadataRefreshEnv) (_ struct{}, retErr error) {
	// The pass must not overlap a library scan: hold the scan stand-down before
	// reading the library (asks the scan to park; fails the op if it does not
	// park within the wait). The body itself writes nothing.
	hold, sdErr := env.Hold(ctx, "maintenance.metadata-refresh apply")
	if sdErr != nil {
		return struct{}{}, sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	return struct{}{}, env.Store.RunMetadataRefreshScan(hold.Ctx, hold.Progress)
}

// --- metadata-upgrade ---
// Removed 2026-09-27: maintenance.metadata-upgrade duplicated
// scheduler.metadata-upgrade (internal/scheduler/extra_ops.go), which now
// lists this ID as a FormerID so old rows and enqueues still resolve.
