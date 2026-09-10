<!-- file: docs/agent-tasks/todo-completion-2026-09/organize/TASK-303-single-file-organize-no-op-paths-report-success.md -->
<!-- version: 1.7.0 -->
<!-- guid: 410607bb-e6e3-4819-8eb2-01a2769667a0 -->
<!-- last-edited: 2026-09-10 -->

# TASK-303 — Single-file organize no-op paths report success without ever stat-verifying the file, unlike the directory path's explicit post-copy check (SF-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SF-01` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **PARTLY**
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — organizer.go:141-142 returns success on FilePath==targetPath with no os.Stat; brief already narrowed to this range.
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · organize subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `SF-01` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **PARTLY**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-303" -b agent/organize-303-single-file-organize-no-op-paths-report origin/main
cd "$REPO/.worktrees/organize-303"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

**Correction from the adversarial re-check (2026-09-10) — this overrides the audit's suggested fix where they differ:** Narrow the fix to :141-142 only. Directory sibling check (service.go:1490-1496) confirmed.

Add the same os.Stat verification the directory path already has to OrganizeBook's two no-op branches (organizer.go:141-143, 184-190), or push a single shared "verify landing.Path exists" check into OrganizeSingleFile so every caller gets it for free.

Why it matters: A book row whose FilePath already equals the computed target (e.g. after a prior successful organize, or a stale/edited DB row) is reported organized successfully even if the file was deleted, corrupted, or moved out from under the row between the last scan and this organize call. The itunes importer (internal/itunes/service/importer.go:1736-1741) calls the exact same OrganizeSingleFile and also does no stat before treating it as done. The user sees "organized" for a book whose file may not exist, and nothing before the next full rescan will catch it.

## Background (verify before editing)

- OrganizeBook at organizer.go:141-143 returns `(targetPath, "", nil)` the instant `book.FilePath == targetPath`, with no os.Stat/os.Lstat on either path — it assumes a match means the file is really there. The sibling case at organizer.go:184-190 (owner lookup says the target already belongs to this book.ID) returns the same success shape, also with no stat. OrganizeSingleFile (organizer.go:794-804) wraps this 1:1 into a Landing with Created==nil whenever mode=="". The caller, Service.OrganizeOneBook (internal/organizer/service.go:1424), returns that Landing straight to its own caller with zero post-check. Contrast with the directory path: organizeDirectoryBookRows (service.go:1477-1498) explicitly os.Stat()s every landing.Files entry after organizeBookDirectory returns and fails the book if copiedCount==0, with a comment (service.go:1488-1489) explaining exactly why that check exists ("pathMap records what organize believed it wrote, and this verifies the files are still there") — a check the single-file path never received.
- Anchor: `internal/organizer/organizer.go:141` (audit `SF-01`, confidence high, severity high).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): PARTLY** — organizer.go:141-142 (FilePath==targetPath) returns success with no stat — the os.Stat at :124 only errors on err==nil&&IsDir, so a missing source falls through. BUT the second branch (owner.ID==book.ID, :187-188) sits inside `if targetInfo, err := os.Stat(targetPath); err == nil` at :174 — existence already proven there. Brief's 'also no stat' claim for branch 2 is wrong at HEAD.
  - Blast radius: OrganizeBook; Service.OrganizeOneBook (service.go:1424); OrganizeSingleFile (:794-804); itunes importer.go:1736-1741.
  - Existing tests to extend: internal/organizer/organizer_test.go
  - Standing-ban contact: none
  - Note: Narrow the fix to :141-142 only. Directory sibling check (service.go:1490-1496) confirmed.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/organizer/organizer.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '135,149p' internal/organizer/organizer.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '178,196p' internal/organizer/organizer.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '788,810p' internal/organizer/organizer.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '1418,1430p' internal/organizer/service.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/organizer/organizer.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_organize_303.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_organize_303.md`.

## Commit message

```
fix(organize): Single-file organize no-op paths report success without ever stat-veri (SF-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
