<!-- file: changelog.d/itunes-pid-uniqueness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f0b2d81-9c34-4a57-8e12-3b7a5c0d4e26 -->
<!-- last-edited: 2026-07-23 -->

### Fixed

#### book_file iTunes persistent IDs are now unique (forward invariant + backfill)

A book_file's iTunes persistent ID (PID) is the join key to its iTunes track, and
`TrackProvisioner` mints one uniquely per file. But version-split copy paths
(`internal/organizer/service.go`, `internal/metafetch/service_apply.go`) did
`newBF := bf`, carrying the PID onto the new organized primary while the demoted
original kept it too — leaving two rows with one PID. A prod census found **8,987**
duplicate PIDs (8,762 same-file duplicate rows, 225 different-file copied PIDs), and
**94** PIDs sat on more than one primary book_file with differing paths, making the
relocate writeback's first-wins match order-dependent.

- **Forward invariant:** `CreateBookFile` now enforces PID uniqueness at the write
  chokepoint — if a PID is already held by another row, ownership TRANSFERS to the new
  (organized) row via the existing `ClearITunesPID` primitive. Only the
  `itunes_persistent_id` DB field moves; no audio file is touched.
- **Census + repair endpoints:** `GET /api/v1/itunes/pid-integrity` (read-only census)
  and `POST /api/v1/itunes/pid-repair` (dry-run-gated backfill). The repair keeps the PID
  on one canonical row and clears it from the rest — same-file keeps a live primary,
  different-file keeps the row matching the live ITL track location; ambiguous cases are
  left untouched for review. No row or file is ever deleted.
