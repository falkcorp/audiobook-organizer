<!-- file: docs/agent-tasks/todo-completion/organize/TASK-119-replace-the-size-equality-heuristic-in-organizeb.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4e42d90-9aaa-4771-bf4a-da0a99a7eedc -->
<!-- last-edited: 2026-08-21 -->

# TASK-119 — Replace the size-equality heuristic in OrganizeBookDirectory's destination-adoption check with a content hash (F5)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · organize subagent · **Why:** small, localized change but touches a prod-data-path chokepoint (organize/rename) that three callers depend on -- warrants care and the existing regression-test suite must stay green · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 872 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**F5 (remainder) — `OrganizeBookDirectory` still c" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-119-replace-the-size-equality-heuristic-in-organizeb" -b agent/organize-119-replace-the-size-equality-heuristic-in-organizeb origin/main
cd "$REPO/.worktrees/organize-119-replace-the-size-equality-heuristic-in-organizeb"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the `srcInfo.Size() == dstInfo.Size()` adoption check at organizer.go:765 with a content-hash comparison using scanner.ComputeFileHash, so two different files that happen to share a size are no longer silently treated as the same file (which currently corrupts pathMap and, downstream, the book's file rows).

## Background (verify before editing)

- organizer.go:222-235 documents the SameFile fast path (hardlink/reflink, already organized) as the trusted case; the size-equality branch at L763-768 is the weaker fallback this item targets.
- internal/organizer/organizer_regression_test.go:140 TestOrganizeBookDirectory_DstAlreadyExists already exercises this code path and will need a new case added, not just an update, to prove the hash comparison actually discriminates two same-sized different-content files.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'srcInfo.Size() == dstInfo.Size()' internal/organizer/organizer.go   # 1 hit L765 — the destination-adoption check still uses size equality, not content hash
  grep -n 'os.SameFile(srcInfo, dstInfo)' internal/organizer/organizer.go   # 1 hit L763 — SameFile (inode-identical) is checked first and remains the fast, always-safe path
  grep -n 'func ComputeFileHash' internal/scanner/scanner.go   # 1 hit L2517 — scanner.ComputeFileHash exists and can be reused
  ```

### Reuse — don't invent

- Use `scanner.ComputeFileHash(filePath string) (string, error)` in `internal/scanner/scanner.go` (verify: `grep -n 'func ComputeFileHash' internal/scanner/scanner.go`) — do NOT write a parallel helper.

## Step-by-step

1. In organizer.go around line 760-776 (inside the loop that builds pathMap for OrganizeBookDirectory), replace the `case srcErr == nil && srcInfo.Size() == dstInfo.Size():` branch's condition with a content-hash comparison: compute `scanner.ComputeFileHash(srcPath)` and `scanner.ComputeFileHash(dstPath)` and adopt only when the hashes match.
2. Add `github.com/falkcorp/audiobook-organizer/internal/scanner` to organizer.go's imports (verify no import cycle first: `grep -n 'audiobook-organizer/internal/organizer' internal/scanner/*.go` should return 0 hits, confirming scanner does not already import organizer).
3. Keep the size check as a cheap pre-filter (skip hashing entirely when sizes already differ -- sizes differing is still a hard 'not the same file' signal) before paying the cost of two full-file hashes.
4. Treat a hash error (unreadable file) as 'not adopted, leave the row unchanged' (matching the existing default: branch's behavior at organizer.go:769-774), not as a fatal error for the whole organize.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_119.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Very large files: hashing both src and dst on every same-sized-destination-exists case adds I/O cost proportional to file size -- acceptable here since this path only runs when a destination ALREADY exists (the common case is SameFile, which short-circuits before hashing), but call this out in the PR description.
- dst unreadable mid-check (race with another process): treat as 'not adopted' (safe default), not a crash.

## Tests

- internal/organizer/organizer_regression_test.go: extend TestOrganizeBookDirectory_DstAlreadyExists (or add TestOrganizeBookDirectory_DstSameSizeDifferentContent_NotAdopted) -- src and dst files of identical size but different byte content must NOT be adopted into pathMap (this is the anti-over-suppression / correctness test: proves the old bug is actually fixed).
- Add TestOrganizeBookDirectory_DstSameSizeSameContent_Adopted -- src and dst files of identical size AND identical content (the legitimate interrupted-copy-resume case) ARE adopted, proving the fix doesn't regress the case the size check was originally added for.

Anti-over-suppression test: `TestOrganizeBookDirectory_DstSameSizeDifferentContent_NotAdopted` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/organizer/... -run TestOrganizeBookDirectory passes, including both new cases.
- [ ] grep -n 'ComputeFileHash' internal/organizer/organizer.go returns >=2 hits (src and dst) after the change.
- [ ] Anti-over-suppression test: `TestOrganizeBookDirectory_DstSameSizeDifferentContent_NotAdopted` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/organizer/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_119.md`.

## Commit message

```
refactor(organize): Replace the size-equality heuristic in OrganizeBookDirectory (F5)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is part 1 of 2 for TODO.md L872 (F5) -- part 2 covers the larger, separately-scoped 'route the three rename paths through MoveBookFile' refactor the TODO calls out as 'Also worth doing'.
