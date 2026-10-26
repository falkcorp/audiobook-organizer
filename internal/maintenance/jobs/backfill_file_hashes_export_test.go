// file: internal/maintenance/jobs/backfill_file_hashes_export_test.go
// version: 1.1.0
// guid: 7b3e9d15-4c2a-4f68-a0d1-e5b8c2f4a913
// last-edited: 2026-10-26

package jobs

// HashBackfillChunkSize lets the external tests aim a cancellation at a
// position inside a chunk without copying the number.
const HashBackfillChunkSize = hashBackfillChunkSize

// SetHashBackfillCommitHook installs a hook that is called inside each chunk
// commit (stages "after-journal", "after-store", "after-flush") and returns a
// function that removes it. A non-nil error from the hook stops the commit at
// that point, leaving what a process kill there would leave.
func SetHashBackfillCommitHook(f func(stage string, chunkStart int) error) (restore func()) {
	prev := hbCommitHook
	hbCommitHook = f
	return func() { hbCommitHook = prev }
}
