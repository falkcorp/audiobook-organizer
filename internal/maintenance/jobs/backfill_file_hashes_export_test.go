// file: internal/maintenance/jobs/backfill_file_hashes_export_test.go
// version: 1.0.0
// guid: 6c2f8d14-a9b3-4e70-b185-d3e7f0a42c96
// last-edited: 2026-10-25

package jobs

// HashBackfillChunkSize and HashBackfillPhase expose the job's chunk size and
// checkpoint phase to the external jobs_test package, so the chunk-boundary and
// resume tests follow the constants instead of repeating their values.
const (
	HashBackfillChunkSize = hashBackfillChunkSize
	HashBackfillPhase     = hashBackfillPhase
)
