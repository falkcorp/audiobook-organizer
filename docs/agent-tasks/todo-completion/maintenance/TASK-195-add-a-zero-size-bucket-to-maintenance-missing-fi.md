<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-195-add-a-zero-size-bucket-to-maintenance-missing-fi.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5328b28d-4504-40c8-8c55-e60bea919c70 -->
<!-- last-edited: 2026-08-21 -->

# TASK-195 — Add a zero-size bucket to maintenance.missing-file-audit (the delta TASK-074 does not cover) (DEC-13)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** Small, additive extension to an already-well-tested report-only op; no prod-data mutation, no concurrency redesign (reuses the existing worker pool) — straightforward for Sonnet. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90013 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90013p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-195-add-a-zero-size-bucket-to-maintenance-missing-fi" -b agent/maintenance-195-add-a-zero-size-bucket-to-maintenance-missing-fi origin/main
cd "$REPO/.worktrees/maintenance-195-add-a-zero-size-bucket-to-maintenance-missing-fi"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extend maintenance.missing-file-audit's stat sweep (auditMissingFiles, missing_file_audit.go:369-501) to distinguish a fourth outcome — 'present but zero bytes' — from today's undifferentiated filePresent. Add fileZeroSize to the fileExistence enum (L287-292), set it when os.Stat succeeds AND info.Size() == 0 (instead of filePresent), tally it into a new report.ZeroSize int field (separate from Present so a truncated/corrupt file is never silently counted as healthy), and surface it in both the log summary (runMissingFileAudit, L355-359) and missingFileReport.summary() (L248-257). This closes the one bucket decision 13 names that the existing op does not already produce; the other three buckets (path-missing, unreadable, sibling-present) are already covered by Missing/Unreadable/Classify and need no change.

## Background (verify before editing)

- maintenance.missing-file-audit already satisfies 3 of decision 13's 4 example buckets: path-missing -> fileMissing/report.Missing, unreadable -> fileUnreadable/report.Unreadable, sibling-present -> the opt-in Classify pass's Recoverable bucket (a derived nearby-path stat, missing_file_audit.go:660-748).
- The op's os.Stat call (missing_file_audit.go:404) currently discards the returned FileInfo on success — `case serr == nil: results[it.idx] = filePresent` — so a 0-byte file on disk is indistinguishable from a healthy one in the current report.
- The op is REPORT-ONLY by explicit design (missing_file_audit.go:73-77's doc comment: 'the right repair is not yet decided... Measure first, then choose') — decision 13 explicitly forbids adding a mutation op, so this extension must stay read-only, matching the existing Capabilities: []sdk.Capability{sdk.CapLibraryRead}.
- TASK-074 (a sibling brief in this task family) is unrelated — it audits the 'Unknown Author' placeholder baked into organized paths, not book_file byte presence.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "book_file\|bytes on disk\|no bytes" "/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/maintenance/TASK-074-build-a-report-only-census-of-books-with-a-place.md"   # 0 hits — TASK-074 is about the Unknown-Author placeholder, unrelated to book_file bytes
  grep -n "Capabilities: \[\]sdk.Capability{sdk.CapLibraryRead}" internal/plugins/maintenance/missing_file_audit.go   # 1 hit ~L279 — maintenance.missing-file-audit is REPORT-ONLY, CapLibraryRead only
  grep -n "fileUnknown fileExistence\|filePresent\|fileMissing\|fileUnreadable" internal/plugins/maintenance/missing_file_audit.go   # hits ~L287-291 — it already buckets Present/Missing/Unreadable
  grep -n "type missingFileClassification struct" internal/plugins/maintenance/missing_file_audit.go   # 1 hit ~L606 — it already has a sibling-present style Classify pass (Recoverable/ShapeNoBytes/NoShape)
  grep -n "\.Size()" internal/plugins/maintenance/missing_file_audit.go   # 0 hits — it does NOT check file size — the stat result's Size() is never read
  grep -n "missingFileAuditDef()" internal/plugins/maintenance/plugin.go   # 1 hit ~L62 — it is already registered in plugin.go like its siblings — no new registration needed, this is an extension of the existing op
  ```

### Reuse — don't invent

- Use `fileExistence enum + the os.Stat sweep in auditMissingFiles (extend, don't duplicate)` in `internal/plugins/maintenance/missing_file_audit.go` (verify: `grep -n "func auditMissingFiles" internal/plugins/maintenance/missing_file_audit.go`) — do NOT write a parallel helper.
- Use `registry.RunItems bounded worker pool (missingFileStatConcurrency)` in `internal/plugins/maintenance/missing_file_audit.go` (verify: `grep -n "missingFileStatConcurrency" internal/plugins/maintenance/missing_file_audit.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/plugins/maintenance/missing_file_audit.go, add fileZeroSize to the fileExistence enum (L287-292): const (fileUnknown fileExistence = iota; filePresent; fileZeroSize; fileMissing; fileUnreadable) — insert it after filePresent so existing MISSING/UNREADABLE ordinal comparisons (if any exist elsewhere) are unaffected; grep for any switch/comparison using the enum's raw int value first (grep -n "fileExistence(" internal/plugins/maintenance/*.go) to confirm none exist beyond this file.
2. In auditMissingFiles' stat sweep (L403-417), change the `case serr == nil:` branch to inspect the stat result: capture it as `info, serr := os.Stat(it.file.FilePath)`, then `case serr == nil: if info.Size() == 0 { results[it.idx] = fileZeroSize; zeroSize.Add(1) } else { results[it.idx] = filePresent; present.Add(1) }` — add a new `var zeroSize atomic.Int64` alongside the existing `missing, present, unreadable atomic.Int64` (L398).
3. Add `ZeroSize int` to missingFileReport (near L216-218, beside TotalRows/Missing/Present) and populate it from `int(zeroSize.Load())` where the report is built (L432-438).
4. Include a per-book/per-row sample for zero-size rows the same way Missing rows get one (L456-459's `if len(report.Sample) < sampleLimit` pattern) — add a separate `ZeroSizeSample []string` field so it does not get mixed into the missing-paths sample, and populate it in the per-book roll-up loop's switch (L452-465) with a new `case fileZeroSize:` arm.
5. Update missingFileReport.summary() (L248-257) to append ` zero_size=%d` to the base line.
6. Update runMissingFileAudit's final log.Info call (L355-359) to include "zero_size", report.ZeroSize and, if non-empty, a zero_size_sample field.
7. Bump the file's version header and last-edited date.
8. Add a changelog fragment (changelog.d/, no header).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_195.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A row with an empty FilePath (already skipped at L388's `if path == "" { continue }`) must remain skipped — it is a different defect (no path at all), not a zero-size file, and must not be miscounted into ZeroSize.
- A zero-size file must NOT be folded into report.Missing or report.Unreadable — those are structurally distinct findings (path absent vs. I/O error vs. present-but-empty) and decision 13 explicitly asks for them as separate buckets.
- The Classify pass (opt-in, track-slash shape) only ever runs over rows tallied as fileMissing (L481-497) — a zero-size row must NOT be fed into classifyMissingRows, since its bytes ARE present at the recorded path (just empty); conflating the two would misreport a truncated file as 'recoverable via a differently-named sibling'.

## Tests

- internal/plugins/maintenance/missing_file_audit_test.go: TestMissingFileAudit_SeparatesZeroSizeFromPresent — seed a fake filesystem/store with 3 book_file rows: one pointing at a real file with content (present, non-zero), one pointing at a real file truncated to 0 bytes (zero-size), one pointing at a nonexistent path (missing); assert report.Present==1, report.ZeroSize==1, report.Missing==1, and that ZeroSize is NOT counted in Present or Missing.

Anti-over-suppression test: `N/A — no filter/guard/skip is being added; this is purely a new report bucket alongside the existing ones. The anti-over-suppression concern is instead structural: TestMissingFileAudit_SeparatesZeroSizeFromPresent explicitly asserts a healthy present file still counts as Present (report.Present==1), proving the new zero-size check does not reclassify or hide genuinely-present rows.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestMissingFileAudit -count=1 exits 0.
- [ ] grep -n "ZeroSize" internal/plugins/maintenance/missing_file_audit.go returns >=3 hits (enum, struct field, tally site).
- [ ] go build ./... && go vet ./... exits 0.
- [ ] Anti-over-suppression test: `N/A — no filter/guard/skip is being added; this is purely a new report bucket alongside the existing ones. The anti-over-suppression concern is instead structural: TestMissingFileAudit_SeparatesZeroSizeFromPresent explicitly asserts a healthy present file still counts as Present (report.Present==1), proving the new zero-size check does not reclassify or hide genuinely-present rows.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_195.md`.

## Commit message

```
refactor(maintenance): Add a zero-size bucket to maintenance.missing-file-audit (th (DEC-13)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Verdict is NOT stale_done: TASK-074 has zero overlap (different subsystem entirely — author-placeholder census, not byte presence). Overlap IS real but PARTIAL against maintenance.missing-file-audit, which already covers 3 of the 4 example buckets (path-missing, unreadable, sibling-present via its opt-in Classify pass) — this brief scopes ONLY the zero-size delta per the scout instructions' 'if it only partially overlaps, scope the delta only.' No new op registration needed (missing-file-audit is already registered at plugin.go:62); this extends the existing op in place.
