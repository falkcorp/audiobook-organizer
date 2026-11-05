// file: internal/repairs/lastrun.go
// version: 1.1.0
// guid: 0d6b94e2-7a3f-4c18-b5e0-2f91c8a47d36
// last-edited: 2026-11-05

package repairs

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Each fixer's newest plan and apply are recorded under a settings key when
// they are enqueued, so GET /repairs is two point reads per fixer (scanning the
// op history would decode every op row and its result blob on every tab open).
// The keys are shared: the Repairs handler writes them for a run started from
// the lane, and the apply op writes the plan key for the plan it starts itself
// to refresh the trial after a write (maintenance.runRepairsApply).

// LastPlanKey is the settings key holding the id of fixerID's newest plan op.
func LastPlanKey(fixerID string) string { return "repairs_last_plan_op:" + fixerID }

// LastApplyKey is the settings key holding the id of fixerID's newest apply op.
func LastApplyKey(fixerID string) string { return "repairs_last_apply_op:" + fixerID }

// LastCompletedPlanKey is the settings key holding the id of fixerID's newest
// COMPLETED plan op. LastPlanKey moves at enqueue, so a plan that is queued,
// running, failed or dropped (ResumeDrop on a restart) hides the trial the
// reviewer was working from; this one moves only when a plan has stored all of
// its rows (maintenance.runRepairsPlan), so the last good trial stays readable.
func LastCompletedPlanKey(fixerID string) string { return "repairs_last_completed_plan_op:" + fixerID }

// LastRunRecorder reads and writes the last-run pointers. database.Store
// satisfies it; the plan and apply ops reach it through a type assertion on the
// queue store, like OpReader.
type LastRunRecorder interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}
