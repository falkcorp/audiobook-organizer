# scope-19 — owner decisions that must become tasks (none exist in the package yet)

Each is an OWNER DECISION already made on 2026-08-21 (docs/plans/DECISIONS-PENDING.md). Verdict must be `actionable` unless the code proves the work is already done. Use `todo_line` = the synthetic values given below and `src_id` = the DEC id. Produce one scout object per deliverable (use `part` if you split).

## todo_line 90006 — DEC-6 internal/server test migration
Decision: migrate the ~60 `internal/server` test call sites that hand-build a `*Server` (or `httptest`/router fixtures) to the shared `newTestServer` helper (find it: `grep -rn "func newTestServer" internal/server`). Goal: one fixture, fewer duplicated setups, and a step toward the `internal/server` test-package timeout problem (TODO-SRVTIMEOUT). Count the real call sites with grep and put the exact number in the brief; split into ≤4 parts by test file group so each part is a disjoint set of files (collision-free waves).

## todo_line 90010 — DEC-10 ComposeScore clamp + apply_confidence
Decision (a): clamp `ComposeScore` (find: `grep -rn "func ComposeScore" internal/`) to [0,100] and route Round-2 metadata apply decisions through a SEPARATE `apply_confidence` value rather than the display score. Scoring may legitimately exceed 100% today (memory: scoring system) — the clamp is for the DISPLAY/threshold path; the raw composite must remain available. Tier: Opus, review_critical=true (touches the apply threshold). Locate every consumer of the score that compares against a threshold and list them as exact_files.

## todo_line 90011 — DEC-11 generateTargetPath collision counter
Decision: detection-only. Add a counter/metric + structured log line when `generateTargetPath` (find: `grep -rn "func generateTargetPath\|func GenerateTargetPath" internal/`) would produce a path already produced for a DIFFERENT book in the same run. No behaviour change; fix deferred. Include the Prometheus metric name pattern already used in the package (`grep -rn "prometheus.NewCounter" internal/ | head`).

## todo_line 90013 — DEC-13 book_file no-bytes categorizing report op
Decision: a REPORT-ONLY maintenance op that categorizes the 41.8% of `book_file` rows with no bytes on disk (memory: #2515/#2516) into buckets (e.g. path-missing, zero-size, unreadable, sibling-present). No mutation. Check whether TASK-074's census op (title starts "Build a report-only census of books with a place") already covers this — if it does, return verdict `stale_done` with the grep that proves the overlap and a note naming TASK-074; if it only partially overlaps, scope the delta only. Register it in `internal/plugins/maintenance/plugin.go` like its siblings.
