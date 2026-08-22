<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-150-audit-apply-shaped-endpoints-for-missing-tag-fil.md -->
<!-- version: 1.0.0 -->
<!-- guid: 47366b63-62fa-4789-9bb3-6579c2b9eff8 -->
<!-- last-edited: 2026-08-21 -->

# TASK-150 — Audit apply-shaped endpoints for missing tag/file-I/O writeback (TODO.md L2481)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Why:** Multi-file investigation across handler packages requiring judgment about which paths mutate on-disk-relevant state; no novel design. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2481 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Consider the same file-I/O audit for the remaini" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-150-audit-apply-shaped-endpoints-for-missing-tag-fil" -b agent/server-handlers-150-audit-apply-shaped-endpoints-for-missing-tag-fil origin/main
cd "$REPO/.worktrees/server-handlers-150-audit-apply-shaped-endpoints-for-missing-tag-fil"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each apply-shaped endpoint in exact_files, read its handler body and determine whether it (a) never touches on-disk tags/cover art, (b) already schedules a tag/cover writeback, or (c) mutates book metadata in the DB with no file writeback — the exact defect BatchApplyFromCache had before fix/review-apply-writes-tags. Record one subsection per endpoint in a new docs/audits/2026-08-21-apply-endpoint-fileio-audit.md with verdict + grep citation. Only propose a shared 'apply + schedule file I/O' helper if a case-(c) endpoint is actually found.

## Background (verify before editing)

- BatchApplyFromCache updated the DB without writing tags/cover art; fixed in fix/review-apply-writes-tags.
- No shared apply+writeback helper exists: grep -rn 'WriteTags|writeTagsAndCover|scheduleFileWriteback' internal/server shows scattered call sites in batch_apply_one.go, batch_apply_op.go, movement_atom_cleanup.go, handlers/metadata_cache.go, handlers/metadata/handler.go.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (h \*OrganizeHandler) ApplyRename' internal/server/handlers/organize.go   # 1 hit at L157 — ApplyRename handler exists
  grep -n 'func (h \*AIHandler) Apply' internal/server/handlers/ai.go   # 2 hits at L484 and L697 — ApplyScanResults and ApplyAuthorReview handlers exist
  grep -n 'func (h \*DiagnosticsHandler) ApplySuggestions' internal/server/handlers/diagnostics.go   # 1 hit at L472 — ApplySuggestions handler exists
  grep -n 'RegisterApplyHandler' internal/server/handlers/review/handler.go   # 1 hit at L137 — review package has a pluggable apply-handler registry
  ```

### Reuse — don't invent

- Use `runBulkWriteBack (library.bulk-write-back op)` in `internal/server/metadata_ops.go` (verify: `grep -n 'runBulkWriteBack' internal/server/metadata_ops.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read organize.go:157 ApplyRename end-to-end; note whether it calls a tag/cover writeback function.
2. Read ai.go:484 ApplyScanResults and ai.go:697 ApplyAuthorReview; same check.
3. Read diagnostics.go:472 ApplySuggestions; same check.
4. Read review/handler.go:137 RegisterApplyHandler and grep -rn 'RegisterApplyHandler(' internal/server to find every registered ApplyFunc; check each.
5. Write one subsection per endpoint to docs/audits/2026-08-21-apply-endpoint-fileio-audit.md: name, file:line, verdict, one-sentence reasoning.
6. If a DEFECT FOUND case turns up, add a changelog.d fragment and a separate todo.d follow-up fragment scoped to fixing only that endpoint — do not fix it inline here.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_150.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- An endpoint that never touches book metadata should be marked 'not applicable', not skipped silently.
- review/handler.go's registry must be expanded to every registered ApplyFunc, not just the registry function itself.

## Tests

- N/A for the audit itself. Any follow-up fix must add a regression test mirroring the tag-write assertion pattern in internal/server/handlers/metadata_cache_test.go.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/metadata/... ./internal/server/handlers/review/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] docs/audits/2026-08-21-apply-endpoint-fileio-audit.md exists with >=5 '## ' subsections, one per audited endpoint.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/metadata/... ./internal/server/handlers/review/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_150.md`.

## Commit message

```
feat(server-handlers): Audit apply-shaped endpoints for missing tag/file-I/O writeb (TODO L2481)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`docs/audits/2026-08-21-apply-endpoint-fileio-audit.md exists with >=5 '## ' subsections, one per audited endpoint.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner phrasing is 'Consider the same file-I/O audit' — the audit document is the deliverable; a shared helper is speculative and out of scope unless the audit finds a live second defect.
