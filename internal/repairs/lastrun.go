// file: internal/repairs/lastrun.go
// version: 1.0.0
// guid: 0d6b94e2-7a3f-4c18-b5e0-2f91c8a47d36
// last-edited: 2026-11-05

package repairs

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

// LastRunRecorder writes a last-run pointer. database.Store satisfies it; the
// apply op reaches it through a type assertion on the queue store, like OpReader.
type LastRunRecorder interface {
	SetSetting(key, value, typ string, isSecret bool) error
}
