<!-- file: changelog.d/feat-itunes-cleanup-merged.md -->
<!-- version: 0.1.0 -->
<!-- guid: c7e1b0a4-5d92-4f68-8b03-2a9f1e6d4c07 -->
<!-- last-edited: 2026-07-22 -->

### Added

#### iTunes merged-track cleanup (`/cleanup-merged`) — P3 of the 2-way-sync writeback

New `POST /api/v1/itunes/cleanup-merged` removes stale duplicate audiobook tracks
left in the library by books that were merged/superseded — the merge-cleanup that
never applied while the writeback was broken. A track is removed only if its PID
belongs to a **non-primary** book_file and **no primary** book_file also owns it
(shared PIDs are kept, defensively). Removal goes through `RemoveTracksByPIDLE`,
which excises the master track and auto-cleans orphaned playlist references in one
pass; the `no-new-dangling-refs` and bounded-delta (≤5000 removes) guards are the
safety net. Candidates come only from DB book_files, so music and podcast tracks
are never touched. `dry_run=true` previews the removal (to-remove / shared-skipped /
primary / non-primary counts). Implements P3 of
`docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md`.
