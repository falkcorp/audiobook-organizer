### Changed

#### `TODO.md` — 17 verified-done items checked off from the 2026-10-03 overnight audit

Three audit passes over all ~475 unchecked tasks in `TODO.md` (HEAD a6a649e53)
found 17 that were already finished on `main` but never checked off. Each proof
(merged PR state via `gh pr view`, the cited commit on `main`, and the cited
file/line in the worktree) was re-verified before the box was ticked, and the
proof is appended to each task line: SPLIT-MERGE-ABS-FOLLOW, DB-04, the five
remaining masked config secrets (#3199), the two staticcheck-gate follow-ups
(332248a8e), VG-DOUBLE-PRIMARY (#3535/#3375/#3661), `POST /api/session/local-all`
(#3470), `make ci` staticcheck findings (#3569), `OperationDef.Permissions`
enforcement (#2536/#2551), the purge-empty-narrators op (bae801737), ABS-SYNC
TASK-12 (#2074), and the two iTunes 2-way-sync P0/P2 items (#2042, 8b42d17ef).
Zero items were marked obsolete; nothing was rejected.

A second pass (`todo-audit-pass2.md` + the three `todo-judge-part{A,B,C}.md`
verdict files) then checked off 15 more verified-done items (14 DONE, 1
obsolete — every cited file/line, commit and PR re-verified; 0 proofs
rejected), dropped 58 items with a strike-through and a dated reason (code
gone, superseded by a merged change, contradicted by a later recorded owner
decision, or a duplicate of another listed item — 18 judge DROP verdicts were
left in place because their reason fell outside that set), and tagged 73 items
with a one-line `❓ owner decision` question. `TODO.md` line count is unchanged
(18,482); 146 task lines edited.

#### Architecture, operations and review docs refreshed to describe the current code

The architecture overview, operations-registry, Repairs-lane and review-page
docs were checked claim-by-claim against the handlers, `internal/repairs`,
`internal/plugins/maintenance/*_fixer.go` and the `wire_*_routes.go` files, and
corrected where they still described planned work as future. The Repairs lane
is documented with its seven shipped fixers (duplicate-copies, folder-books,
fragment-consolidation, normalize-letter-l-ordinals, repair-junk-authors,
repair-junk-titles, version-group-primary-repair) and its four routes.
