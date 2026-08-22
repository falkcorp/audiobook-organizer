<!-- file: docs/agent-tasks/todo-completion/scanner/TASK-123-delete-the-unused-internal-scanner-mocks-generat.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9707bce0-252a-4fe2-8f09-3c4f684000ca -->
<!-- last-edited: 2026-08-21 -->

# TASK-123 — Delete the unused internal/scanner/mocks generated package (TODO.md L4739)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · scanner subagent · **Why:** Delete a directory and one YAML entry; no logic to reason about. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 4739 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**zero**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/scanner-123-delete-the-unused-internal-scanner-mocks-generat" -b agent/scanner-123-delete-the-unused-internal-scanner-mocks-generat origin/main
cd "$REPO/.worktrees/scanner-123-delete-the-unused-internal-scanner-mocks-generat"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Delete internal/scanner/mocks/ entirely and remove its .mockery.yaml entry, since it has no importers and the package's own tests already use a hand-written double for the same interface.

## Background (verify before editing)

- The mockery config's `internal/scanner:` block (.mockery.yaml ~L32-37) has only the Scanner interface; removing the Scanner: line empties that block, so the whole `github.com/falkcorp/audiobook-organizer/internal/scanner:` top-level key should be removed too if Scanner is its only interface (verify no sibling interfaces are listed under it before deleting the whole block, not just the Scanner: line).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  wc -l internal/scanner/mocks/*.go   # 380 mock_scanner.go, 62 mock_scanner_coverage_test.go, 442 total — dir contents and line counts
  grep -rln "scanner/mocks" --include="*.go" .   # 0 hits — zero importers repo-wide
  grep -n "Scanner:" .mockery.yaml   # 1 hit ~L37, under the internal/scanner config block (dir: internal/scanner/mocks) — .mockery.yaml still generates it
  grep -n "fullMockScanner" internal/scanner/scanner_coverage_test.go   # ≥3 hits ~L642,655,659 — internal/scanner tests use a hand-rolled double instead, to dodge an import cycle
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Confirm no other interface is configured under the `internal/scanner:` mockery block: `grep -n "internal/scanner:" -A 6 .mockery.yaml` and check only `Scanner:` appears under `interfaces:`.
2. Delete internal/scanner/mocks/ (both files): `git rm -r internal/scanner/mocks/`.
3. In .mockery.yaml, remove the `github.com/falkcorp/audiobook-organizer/internal/scanner:` block (config + interfaces: Scanner:) in full.
4. Run `go build ./... && go vet ./...` to confirm nothing else was silently depending on the package (matches the zero-importer grep, so this should be a no-op).
5. Bump .mockery.yaml's version header if it carries one (check first: `head -5 .mockery.yaml`); if the file has no header convention, skip.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_scanner_123.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If a future mockery regeneration run (`mockery` CLI via Makefile target, check `grep -n mockery Makefile`) is invoked without regenerating this entry removed, confirm no Makefile target hard-codes internal/scanner/mocks/ as an expected output path that would now error on a missing dir.

## Tests

- No new test needed. Existing internal/scanner tests using fullMockScanner (scanner_coverage_test.go) must keep passing unmodified — they never depended on the deleted package.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/scanner/mocks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] test -d internal/scanner/mocks returns non-zero (directory gone).
- [ ] go build ./... exits 0.
- [ ] grep -n "internal/scanner:" .mockery.yaml returns 0 hits.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/scanner/mocks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_scanner_123.md`.

## Commit message

```
refactor(scanner): Delete the unused internal/scanner/mocks generated package (TODO L4739)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Pairs naturally with the sibling L4743 item (internal/operations/mocks) as one small cleanup PR, but keep them as two commits since the operations/mocks item has a wrinkle (see that item's notes) this one does not.
