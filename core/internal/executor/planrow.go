// file: core/internal/executor/planrow.go
// version: 0.1.0
// guid: 4d9a2e61-0b73-4c58-9f1e-8a3c6d2b7e05
// last-edited: 2026-11-28

package executor

// PlanRowStoredID is the ID a Fixer plan's row is stored under in its plan run:
// planPrefix plus the definition's Row.ID. The kernel's module handle over the
// stored rows (Deps.TaskRows, PlannedRow) reads a plan's rows by it, so the
// prefix stays the executor's alone.
func PlanRowStoredID(rowID string) string { return planPrefix + rowID }
