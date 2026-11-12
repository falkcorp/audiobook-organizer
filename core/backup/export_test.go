// file: core/backup/export_test.go
// version: 1.0.1
// guid: 0c6f2b84-9d31-4a57-8e0b-1f5a7d3c9e62
// last-edited: 2026-11-12

package backup

// The task bodies, for the external tests of this package.
var (
	TaskCreate = (*Svc).taskCreate
	TaskVerify = (*Svc).taskVerify
	TaskPrune  = (*Svc).taskPrune
	TaskStage  = (*Svc).taskStage
)

// ExtractStoreWith is the offline extraction over a Disk the test supplies, so
// the free-space refusal is reachable without a full filesystem.
var ExtractStoreWith = extractStore

// Internals the tests drive directly.
var (
	RunReasonLabel = runReasonLabel
	TruncateUTF8   = truncateUTF8
)
