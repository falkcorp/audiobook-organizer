<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-013-build-a-report-only-scan-for-book-rows-that-may-.md -->
<!-- version: 1.0.0 -->
<!-- guid: b9296c73-cd79-40d6-801d-b87400ab8fb4 -->
<!-- last-edited: 2026-08-21 -->

# TASK-013 — Build a report-only scan for book rows that may have been spuriously created by the .tmp-rename bug (TODO.md L4844)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · ci-tooling subagent · **Why:** Combines a filesystem-pattern reuse (find_bogus_dirs), a live-API paginated book query, and two independent spurious-row heuristics (numeric title, path-segment pattern) into one coherent report — more judgment than a mechanical sweep, but well short of a design question given owner decision #9's report-only precedent. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4844 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Investigate book rows affected as a side effect. `" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-013-build-a-report-only-scan-for-book-rows-that-may-" -b agent/ci-tooling-013-build-a-report-only-scan-for-book-rows-that-may- origin/main
cd "$REPO/.worktrees/ci-tooling-013-build-a-report-only-scan-for-book-rows-that-may-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Write scripts/find_spurious_stranded_book_rows.py: a REPORT-ONLY script (no mutation, per the owner's standing 'never delete files/rows in any repair' rule and decision #9's report-only precedent for review-lane investigations) that queries the live book API for rows whose title matches `^\d+$` (purely numeric) or whose file_path contains a segment matching ` - \d+$`, cross-references those against the set of affected book directories scripts/repair_stranded_tracks.py's find_bogus_dirs() already identifies, and prints/writes counts broken down by (numeric-title-only, path-segment-only, both, neither-but-in-an-affected-dir) so a human can decide what — if anything — to do about soft-delete/purge archaeology, per the item's own 'Report counts before proposing any restore. Do not mass-restore rows.'

## Background (verify before editing)

- This is Task 3 from the original incident write-up (.claude/notes/2026-08-16-tmp-rename-recovery-prompt.md): 'Hypothesis worth testing: books whose files all became stranded would have looked empty to any scan/cleanup pass, and may have been soft-deleted or purged... purge-deleted and trash-cleanup are scheduled ops — check whether either ran over these in April.'
- Per the owner decision list in this scope's instructions (#12, missing-file lane): 'never delete; must REPOINT' and (#9) 'review-only drain; per-population REPORT ops may be built; NO purge/delete ops' — this item must produce a report, not a mutation.
- The API requires the long-lived key at ~/.config/audiobook-organizer/api-key per this repo's operational conventions; a server-bootstrap-style auth flow is out of scope for this item (assume the key file already exists).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln "purely numeric title" --include="*.py" --include="*.go" scripts/ internal/   # 0 hits — no existing tooling covers 'purely numeric title' detection by that phrase
  grep -rln "numeric_title" --include="*.py" --include="*.go" scripts/ internal/   # 1 hit — internal/metafetch/service_test.go:161, a subtest name for stop-word extraction ("14" → {"14": true}), unrelated to this item's spurious-book-row concept — 'numeric_title' only appears as an unrelated test-case label (stop-word extraction test), not as spurious-row detection tooling
  grep -rln "spurious.*book.*row" --include="*.py" --include="*.go" scripts/ internal/   # 0 hits — no existing tooling covers 'spurious...book...row' detection by that phrase
  grep -n "85 separate Book records" internal/organizer/path_format.go   # 1 hit ~L59-60 — the defect mechanism is documented and real (85-Book-records example)
  grep -n "def find_bogus_dirs" scripts/repair_stranded_tracks.py   # 1 hit ~L119 — the affected-book-directory list already exists in the sibling recovery tool and can be reused/imported
  grep -l "api-token\|API_TOKEN\|Authorization" scripts/*.py   # ≥1 hit, e.g. scripts/abs_capture_fixtures.py — existing scripts already have a precedent for talking to the live API with an auth token
  ```

### Reuse — don't invent

- Use `find_bogus_dirs()` in `scripts/repair_stranded_tracks.py` (verify: `grep -n "def find_bogus_dirs" scripts/repair_stranded_tracks.py`) — do NOT write a parallel helper.

## Step-by-step

1. Create scripts/find_spurious_stranded_book_rows.py with the standard file header for a Python script per file-headers.md.
2. Import or re-implement (prefer import if repair_stranded_tracks.py exposes find_bogus_dirs as an importable function without side effects) the bogus-directory detector to get the current set of affected book paths.
3. Add an API client function using the same auth pattern as scripts/abs_capture_fixtures.py (Authorization header from the key file), paginating GET /api/v1/books (check the real endpoint path via `grep -rn "router.GET(\"/api/v1/books\"" internal/server/`) to enumerate all books with id, title, file_path.
4. For each book, test: (a) `re.fullmatch(r'\d+', title.strip())` for purely-numeric title, (b) `re.search(r' - \d+$', PurePath(file_path).parent.name)` or similar for a path segment ending in ' - N'.
5. Cross-reference matches against the bogus-directory set from step 2: report counts for (in an affected dir AND numeric-title), (in an affected dir AND path-segment match), (numeric-title but NOT in an affected dir — could be a false positive, e.g. a book genuinely titled a number), (in an affected dir but neither heuristic fired — worth a closer look).
6. Write output to a timestamped report file (mirroring repair_stranded_tracks.py's own reporting convention) and print a summary to stdout; make no API mutation calls anywhere in the script.
7. Do NOT implement any restore/purge action in this script — per the item and owner decision, that is a separate, later, human-reviewed step.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_013.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A real book genuinely titled a bare number (e.g. '1984', '2001') would false-positive on the numeric-title heuristic alone — this is why the cross-reference against find_bogus_dirs' affected-directory set is required, not optional; report numeric-title matches OUTSIDE an affected directory as a separate, lower-confidence bucket rather than folding them into the main count.
- Books whose file_path no longer exists on disk (already cleaned up some other way) still need to be counted from the DB side — do not skip a book just because os.path.exists(file_path) is false; the whole point is finding rows the filesystem side can no longer explain.

## Tests

- scripts/tests/test_find_spurious_stranded_book_rows.py (or inline `if __name__ == "__main__":` smoke test): unit-test the two regex heuristics against known-good and known-bad title/path strings (e.g. 'Tarkin - Star Wars - 3' parent + '85' — wait, verify against the REAL shape: the numeric fragment becomes the Title of a spuriously-created Book, so test with title='85', title='Foundation and Empire', path segment 'Project Hail Mary - 24').
- Test the cross-reference logic against a small fixture of 3-4 fake book records: one clearly spurious (numeric title, in an affected dir), one false-positive-shaped (numeric title, NOT in an affected dir — e.g. a real book called '1984'), one clean (neither).

Anti-over-suppression test: `N/A — this is a read-only investigation script, not a filter/guard in the runtime path.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] python3 scripts/find_spurious_stranded_book_rows.py --help exits 0.
- [ ] Running it against a local/dev server (not prod, for the acceptance check) produces a report file and prints non-negative counts for each category with no exceptions.
- [ ] grep -rn "DELETE\|requests.delete\|api.*delete" scripts/find_spurious_stranded_book_rows.py returns 0 hits — confirms the report-only constraint is honored in the code, not just the docstring.
- [ ] Anti-over-suppression test: `N/A — this is a read-only investigation script, not a filter/guard in the runtime path.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_013.md`.

## Commit message

```
feat(ci-tooling): Build a report-only scan for book rows that may have been sp (TODO L4844)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`python3 scripts/find_spurious_stranded_book_rows.py --help exits 0.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Explicitly a REPORT-only deliverable per owner decision #9's precedent and the item's own 'Report counts before proposing any restore. Do not mass-restore rows.' Depends on L4832's find_bogus_dirs() (already built, not blocking).
