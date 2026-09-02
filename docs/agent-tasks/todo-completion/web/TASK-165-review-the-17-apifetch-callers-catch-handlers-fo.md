<!-- file: docs/agent-tasks/todo-completion/web/TASK-165-review-the-17-apifetch-callers-catch-handlers-fo.md -->
<!-- version: 1.1.0 -->
<!-- guid: 48fab227-4013-4104-807f-47b4027c44e3 -->
<!-- last-edited: 2026-09-02 -->

# TASK-165 — Review the 17 apiFetch-callers' catch handlers for session-expiry messaging (TODO.md L2486)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — isAuthRedirectError used in one non-test product file (useMetadataLane.ts:40,896) + apiFetch.ts:70; none of the 17 named call sites (BookDetail/Library/FileManager/UserMenu) reference it. Recommendation: keep - audit-shaped; consider splitting per-surface if it stalls again.

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · web subagent · **Why:** Mechanically similar review across 18 files, but each catch site needs a judgment call on whether the existing message reads sensibly for a session-expiry vs. a genuine failure — not pure mechanical replacement. · **Depends on:** none · **Wave:** 7 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2486 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**17 API calls will now surface an expired session" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-165-review-the-17-apifetch-callers-catch-handlers-fo" -b agent/web-165-review-the-17-apifetch-callers-catch-handlers-fo origin/main
cd "$REPO/.worktrees/web-165-review-the-17-apifetch-callers-catch-handlers-fo"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 17 functions (quarantineBook, unquarantineBook, restoreSoftDeletedBook, removeImportPath, changePassword, linkBookVersion, markNoMatch, includeFilesystemPath, deleteBackup, clearMetadataNoMatch, runMaintenanceWindow, updateTaskConfig, saveUserColumnConfig, saveSavedFilterPresets, mergeDedupCandidate, dismissDedupCandidate, revokeAPIKey), find its call site(s) in exact_files, check whether the surrounding try/catch (or .catch()) shows the raw error message to the user. Where it would show something misleading (e.g. 'Failed to quarantine audiobook' for what is actually an expired session), branch on isAuthRedirectError(err) from web/src/utils/apiFetch.ts and show a distinct 'Your session expired — please sign in again' message instead.

## Background (verify before editing)

- apiFetch(...) throws ApiAuthRedirectError before the caller ever inspects response.ok, so the existing 'if (!response.ok) throw await buildApiError(...)' block inside each api.ts function is never reached on an expired session (confirmed for quarantineBook/unquarantineBook/restoreSoftDeletedBook).
- isAuthRedirectError(err) is already exported from web/src/utils/apiFetch.ts and used by exactly one caller today (useMetadataLane.ts).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'throw new ApiAuthRedirectError' web/src/utils/apiFetch.ts   # 2 hits at L100 and L110 — apiFetch throws ApiAuthRedirectError on a login-page response
  sed -n '1192,1218p' web/src/services/api.ts   # 3 functions, each 'const response = await apiFetch(...)' then 'if (!response.ok)' — quarantineBook/unquarantineBook/restoreSoftDeletedBook use apiFetch and only check response.ok
  grep -rln "isAuthRedirectError\|ApiAuthRedirectError" web/src --include=*.tsx --include=*.ts | grep -v test | grep -v apiFetch.ts   # 1 hit: web/src/components/review/lanes/useMetadataLane.ts — only one caller special-cases the auth-redirect error today
  ```

### Reuse — don't invent

- Use `isAuthRedirectError(err)` in `web/src/utils/apiFetch.ts` (verify: `grep -n 'export function isAuthRedirectError' web/src/utils/apiFetch.ts`) — do NOT write a parallel helper.

## Step-by-step

1. For each function name, run grep -n "<name>(" on its caller file(s) listed in exact_files to find the call site and its enclosing try/catch or .catch(err => ...).
2. Read the current error-display code (toast/snackbar/alert call) at that site.
3. If the message is generic enough to already read sensibly on session expiry (e.g. a raw 'err.message' passthrough with no book-specific wording), leave it — note 'OK as-is' and move on.
4. If the message would mislead (e.g. hard-coded 'Failed to X' text that implies the operation itself failed), add an `if (isAuthRedirectError(err)) { <show session-expired toast>; return; }` branch before the generic handler, importing isAuthRedirectError from '../../utils/apiFetch' (adjust relative path per file).
5. Repeat useMetadataLane.ts's existing pattern (read it first) for the toast wording so all 17 sites are consistent.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_165.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A caller with no user-visible error surface at all (e.g. a fire-and-forget background save) should get a console.warn distinguishing session-expiry from a genuine save failure, not silently do nothing differently.
- mergeDedupCandidate/dismissDedupCandidate are on the dedup-apply path — a session-expiry there must not be mistaken for 'merge failed, data unchanged' when in fact the request never reached the server in a way the UI can distinguish without this fix.

## Tests

- web/src/pages/BookDetail.test.tsx (or nearest existing test file per component) — add a case that mocks the API call to reject with an ApiAuthRedirectError instance and asserts the session-expired message is shown, not the generic failure message.
- Happy-path anti-over-suppression: existing tests asserting the ORIGINAL generic failure message on a non-auth error (e.g. a 500) must still pass — do not let the new branch swallow real failures.

Anti-over-suppression test: `Test asserting a genuine 500 still shows the original 'Failed to X' message, not the session-expired one.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] P
- [ ] r
- [ ] o
- [ ] d
- [ ] u
- [ ] c
- [ ] e
- [ ]  
- [ ] a
- [ ]  
- [ ] w
- [ ] r
- [ ] i
- [ ] t
- [ ] t
- [ ] e
- [ ] n
- [ ]  
- [ ] p
- [ ] e
- [ ] r
- [ ] -
- [ ] f
- [ ] u
- [ ] n
- [ ] c
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ]  
- [ ] t
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ]  
- [ ] i
- [ ] n
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] f
- [ ] i
- [ ] n
- [ ] a
- [ ] l
- [ ]  
- [ ] r
- [ ] e
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ]  
- [ ] c
- [ ] o
- [ ] v
- [ ] e
- [ ] r
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] a
- [ ] l
- [ ] l
- [ ]  
- [ ] 1
- [ ] 7
- [ ]  
- [ ] n
- [ ] a
- [ ] m
- [ ] e
- [ ] s
- [ ]  
- [ ] (
- [ ] q
- [ ] u
- [ ] a
- [ ] r
- [ ] a
- [ ] n
- [ ] t
- [ ] i
- [ ] n
- [ ] e
- [ ] B
- [ ] o
- [ ] o
- [ ] k
- [ ]  
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] r
- [ ] e
- [ ] v
- [ ] o
- [ ] k
- [ ] e
- [ ] A
- [ ] P
- [ ] I
- [ ] K
- [ ] e
- [ ] y
- [ ] )
- [ ] ,
- [ ]  
- [ ] e
- [ ] a
- [ ] c
- [ ] h
- [ ]  
- [ ] m
- [ ] a
- [ ] r
- [ ] k
- [ ] e
- [ ] d
- [ ]  
- [ ] C
- [ ] H
- [ ] A
- [ ] N
- [ ] G
- [ ] E
- [ ] D
- [ ]  
- [ ] o
- [ ] r
- [ ]  
- [ ] O
- [ ] K
- [ ] -
- [ ] A
- [ ] S
- [ ] -
- [ ] I
- [ ] S
- [ ]  
- [ ] w
- [ ] i
- [ ] t
- [ ] h
- [ ]  
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] f
- [ ] i
- [ ] l
- [ ] e
- [ ] :
- [ ] l
- [ ] i
- [ ] n
- [ ] e
- [ ] ;
- [ ]  
- [ ] e
- [ ] v
- [ ] e
- [ ] r
- [ ] y
- [ ]  
- [ ] C
- [ ] H
- [ ] A
- [ ] N
- [ ] G
- [ ] E
- [ ] D
- [ ]  
- [ ] s
- [ ] i
- [ ] t
- [ ] e
- [ ]  
- [ ] m
- [ ] u
- [ ] s
- [ ] t
- [ ]  
- [ ] s
- [ ] h
- [ ] o
- [ ] w
- [ ]  
- [ ] a
- [ ] n
- [ ]  
- [ ] i
- [ ] s
- [ ] A
- [ ] u
- [ ] t
- [ ] h
- [ ] R
- [ ] e
- [ ] d
- [ ] i
- [ ] r
- [ ] e
- [ ] c
- [ ] t
- [ ] E
- [ ] r
- [ ] r
- [ ] o
- [ ] r
- [ ]  
- [ ] b
- [ ] r
- [ ] a
- [ ] n
- [ ] c
- [ ] h
- [ ]  
- [ ] (
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] i
- [ ] s
- [ ] A
- [ ] u
- [ ] t
- [ ] h
- [ ] R
- [ ] e
- [ ] d
- [ ] i
- [ ] r
- [ ] e
- [ ] c
- [ ] t
- [ ] E
- [ ] r
- [ ] r
- [ ] o
- [ ] r
- [ ] '
- [ ]  
- [ ] <
- [ ] f
- [ ] i
- [ ] l
- [ ] e
- [ ] >
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] r
- [ ] l
- [ ]  
- [ ] '
- [ ] i
- [ ] s
- [ ] A
- [ ] u
- [ ] t
- [ ] h
- [ ] R
- [ ] e
- [ ] d
- [ ] i
- [ ] r
- [ ] e
- [ ] c
- [ ] t
- [ ] E
- [ ] r
- [ ] r
- [ ] o
- [ ] r
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ]  
- [ ] -
- [ ] -
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] e
- [ ] =
- [ ] '
- [ ] *
- [ ] .
- [ ] t
- [ ] s
- [ ] '
- [ ]  
- [ ] -
- [ ] -
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] e
- [ ] =
- [ ] '
- [ ] *
- [ ] .
- [ ] t
- [ ] s
- [ ] x
- [ ] '
- [ ]  
- [ ] |
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] v
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] |
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] v
- [ ]  
- [ ] a
- [ ] p
- [ ] i
- [ ] F
- [ ] e
- [ ] t
- [ ] c
- [ ] h
- [ ] .
- [ ] t
- [ ] s
- [ ]  
- [ ] l
- [ ] i
- [ ] s
- [ ] t
- [ ] s
- [ ]  
- [ ] e
- [ ] v
- [ ] e
- [ ] r
- [ ] y
- [ ]  
- [ ] C
- [ ] H
- [ ] A
- [ ] N
- [ ] G
- [ ] E
- [ ] D
- [ ]  
- [ ] f
- [ ] i
- [ ] l
- [ ] e
- [ ]  
- [ ] (
- [ ] o
- [ ] n
- [ ] l
- [ ] y
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ] /
- [ ] c
- [ ] o
- [ ] m
- [ ] p
- [ ] o
- [ ] n
- [ ] e
- [ ] n
- [ ] t
- [ ] s
- [ ] /
- [ ] r
- [ ] e
- [ ] v
- [ ] i
- [ ] e
- [ ] w
- [ ] /
- [ ] l
- [ ] a
- [ ] n
- [ ] e
- [ ] s
- [ ] /
- [ ] u
- [ ] s
- [ ] e
- [ ] M
- [ ] e
- [ ] t
- [ ] a
- [ ] d
- [ ] a
- [ ] t
- [ ] a
- [ ] L
- [ ] a
- [ ] n
- [ ] e
- [ ] .
- [ ] t
- [ ] s
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] r
- [ ] u
- [ ] n
- [ ]  
- [ ] l
- [ ] i
- [ ] n
- [ ] t
- [ ]  
- [ ] &
- [ ] &
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] e
- [ ] x
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] 0
- [ ] .
- [ ] Anti-over-suppression test: `Test asserting a genuine 500 still shows the original 'Failed to X' message, not the session-expired one.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_165.md`.

## Commit message

```
refactor(web): Review the 17 apiFetch-callers' catch handlers for session-e (TODO L2486)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is the direct consequence of apiFetch's ApiAuthRedirectError throw already shipping — the fix 'is working', per the TODO text; this item is purely about auditing/upgrading 18 call-site messages. Good candidate for a parallel-refactor-sweep given the repeated pattern across files.
