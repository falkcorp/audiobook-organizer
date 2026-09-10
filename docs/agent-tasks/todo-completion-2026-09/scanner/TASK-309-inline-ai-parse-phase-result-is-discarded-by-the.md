<!-- file: docs/agent-tasks/todo-completion-2026-09/scanner/TASK-309-inline-ai-parse-phase-result-is-discarded-by-the.md -->
<!-- version: 1.7.0 -->
<!-- guid: 024835ae-bcc2-4d7e-985f-282ae502b980 -->
<!-- last-edited: 2026-09-10 -->

# TASK-309 — Inline AI-parse phase result is discarded by the scan, so a fully-aborted LLM phase (revoked key, quota exhausted, 3+ batch failures) still lets library.scan report success (SF-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SF-02` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **CONFIRMED**
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — scanner.go still discards AIPhaseSummary at both call sites; ProcessBooksParallel returns nil unconditionally; fix shape proven in library_ai_parse_op.go.
**Priority:** P0 · **Effort:** S · **Recommended subagent:** Haiku-class · scanner subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SF-02` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/scanner-309" -b agent/scanner-309-inline-ai-parse-phase-result-is-discarde origin/main
cd "$REPO/.worktrees/scanner-309"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Capture the AIPhaseSummary from both call sites (scanner.go:1705, 1714), and when `.Failed()` is true, surface it into the scan's own reporter/log (and consider folding it into ProcessBooksParallel's returned error or a non-fatal warning surfaced to the operation record), matching what library_ai_parse_op.go already does for the queued path.

Why it matters: This is exactly the pattern CLAUDE.md/the mission calls out: "Ops that report completed when a phase returned an error." Whenever the AI parse queue is unavailable (ErrAIParseEnqueueUnavailable) or partially rejects candidates, the AI batch phase runs inline inside the scan. If the LLM backend is fully down (revoked API key, exhausted quota — the exact 2026-08-16 incident ai_failure.go documents) the phase aborts every remaining batch, but library.scan and folder-autoscan still finish reporting COMPLETED with a full progress bar, identical to a run that AI-parsed everything. The only surviving evidence is the internal log.Warn lines inside runAIBatchPhase itself — nothing reaches the operation record, unlike the queued path which was specifically built to fix this exact blind spot for itself.

## Background (verify before editing)

- runAIBatchPhase returns an AIPhaseSummary whose own doc comment (internal/scanner/ai_batch_phase.go:61-66) says "Every failure in this phase is a log.Warn and a `return nil` ... The queued library.ai-parse operation reports this summary into its own operation record so a run that did nothing cannot show up green." scanner.go:1705 and scanner.go:1714 call `runAIBatchPhase(...)` as a bare statement — the returned AIPhaseSummary is never assigned to a variable, so `.Failed()`, `.String()`, and `.FailureDetails()` (which the queued path at internal/server/library_ai_parse_op.go:143-167 uses to turn a failed phase into `reporter.Log(WARN, ...)` plus `return fmt.Errorf(...)`) are never consulted here. ProcessBooksParallel (scanner.go:1106) always falls through to `return nil` at line 1728 regardless of what the AI phase did. ProcessBooksParallel's own callers (internal/scanner/service.go:455, the library.scan chunk loop, and internal/server/folder_autoscan_op.go:90) treat that nil as the definitive chunk/op success signal.
- Anchor: `internal/scanner/scanner.go:1705` (audit `SF-02`, confidence high, severity critical).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — Both runAIBatchPhase calls are bare statements; AIPhaseSummary discarded; ProcessBooksParallel returns nil regardless. AIPhaseSummary.Failed() exists (ai_batch_phase.go:415).
  - Blast radius: scanner_test.go, ai_batch_phase_test.go; callers service.go:455 (library.scan chunk loop) and folder_autoscan_op.go:90 treat nil as success — do NOT make an LLM outage fail an otherwise-good scan chunk; surface as non-fatal warning on the op record.
  - Existing tests to extend: internal/scanner/scanner_test.go, internal/scanner/ai_batch_phase_test.go
  - Standing-ban contact: scan reporting path — validate with unit mocks, never a real scan

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/scanner/scanner.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '1100,1112p' internal/scanner/scanner.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '1699,1734p' internal/scanner/scanner.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '55,72p' internal/scanner/ai_batch_phase.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '449,461p' internal/scanner/service.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '84,96p' internal/server/folder_autoscan_op.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '137,173p' internal/server/library_ai_parse_op.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/scanner/scanner.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_scanner_309.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/scanner/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_scanner_309.md`.

## Commit message

```
fix(scanner): Inline AI-parse phase result is discarded by the scan, so a fully-abor (SF-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
