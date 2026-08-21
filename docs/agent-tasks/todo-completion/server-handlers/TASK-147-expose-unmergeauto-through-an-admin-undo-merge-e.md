<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-147-expose-unmergeauto-through-an-admin-undo-merge-e.md -->
<!-- version: 1.0.0 -->
<!-- guid: 348a6c45-4227-441c-95e6-d6d50b97af2e -->
<!-- last-edited: 2026-08-21 -->

# TASK-147 — Expose UnmergeAuto through an admin undo-merge endpoint (list + invoke) (MERGE-UNDO)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Why:** Mostly mechanical handler + route wiring following the existing handler.go conventions (auth, error responses, event publishing), but touches a prod merge/undo surface so needs care on authz and idempotency, not pure boilerplate. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 17 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**MERGE-UNDO**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-147-expose-unmergeauto-through-an-admin-undo-merge-e" -b agent/server-handlers-147-expose-unmergeauto-through-an-admin-undo-merge-e origin/main
cd "$REPO/.worktrees/server-handlers-147-expose-unmergeauto-through-an-admin-undo-merge-e"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add two admin-only endpoints: GET /api/v1/dedup/merges (list recent AutoMergeJournalEntry rows via ListAutoMergeJournalEntries, newest first, capped) and POST /api/v1/dedup/merges/:key/undo (calls Engine.UnmergeAuto(key) — the fully-reversing version from part 2 once that lands — and returns 200 on success, 404 if the journal key does not exist, 409 if already reverted).

## Background (verify before editing)

- handler.go:1414's MergeJournaled call logs journalKey via slog.Info but never returns it in the HTTP response body (result set at handler.go:1478 has no journal_key field) — the undo flow currently has no way for an operator to discover a journal key except reading server logs.
- DedupEngine interface (internal/server/handlers/dedup/interfaces.go:76-83) already exports MergeJournaled for mocking; UnmergeAuto and a listing method need the same treatment to be reachable/mockable from the handler package.
- This repo's dedup routes are registered in internal/server/wire_dedup_routes.go (confirm exact filename via `grep -rln "RegisterDedupRoutes\|dedup.*router.GET" internal/server/*.go` before editing, since the file may be named differently).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn "UnmergeAuto" internal/server internal/plugins   # only a comment hit at handler.go:1405, no functional call site — UnmergeAuto has no production caller
  grep -n "ListAutoMergeJournalEntries" internal/database/dedup_automerge_journal.go   # 1 hit ~L105, doc comment says 'Intended for a follow-on admin undo merge listing' — ListAutoMergeJournalEntries exists and is documented as unused, built for this follow-on
  grep -n "MergeJournaled" internal/server/handlers/dedup/interfaces.go   # 1 hit in the DedupEngine interface — MergeJournaled's interface is already exported to the dedup handler package for mocking
  ```

### Reuse — don't invent

- Use `DedupEngine interface (h.dedupEngine)` in `internal/server/handlers/dedup/interfaces.go` (verify: `grep -n "type DedupEngine interface" internal/server/handlers/dedup/interfaces.go`) — do NOT write a parallel helper.
- Use `httputil.RespondWithOK / RespondWithBadRequest / RespondWithNotFound` in `internal/server/handlers/dedup/handler.go` (verify: `grep -n "httputil.RespondWith" internal/server/handlers/dedup/handler.go`) — do NOT write a parallel helper.

## Step-by-step

1. Confirm the actual dedup route-registration file: run `grep -rln "DismissDedupCandidate\|MergeDedupCandidate" internal/server/*.go` to find where dedup handler methods are wired to gin routes (do not assume the filename).
2. internal/server/handlers/dedup/interfaces.go: add `UnmergeAuto(journalKey string) error` and `ListAutoMergeJournalEntries(limit int) ([]database.AutoMergeJournalEntry, error)` to the DedupEngine interface (or a narrower sub-interface if the file follows a narrowing convention — check for an existing pattern before widening DedupEngine).
3. internal/server/handlers/dedup/handler.go: add `ListMergeJournal(c *gin.Context)` (GET) parsing an optional `?limit=` query param, calling h.dedupEngine.ListAutoMergeJournalEntries, and `UndoMerge(c *gin.Context)` (POST) taking `:key` as a URL param (note: journal keys contain a colon, e.g. `dedup:automerge:...` — confirm the key is safely embeddable as a single gin URL param or needs to be a body field instead; PREFER a JSON body `{"journal_key": "..."}` on the POST to avoid colon-in-path routing issues) that calls h.dedupEngine.UnmergeAuto(key) and returns 200 `{"status": "unmerged"}`, or 404 if the entry doesn't exist (UnmergeAuto currently returns a plain fmt.Errorf for that case — check whether a sentinel/typed error is needed to distinguish 404 from 500; if not, add one as part of this task rather than string-matching, unlike the pre-existing MAYDEPLOY-B2 workaround at handler.go:1426).
4. Register both routes in the dedup route-registration file found in step 1, gated behind whatever auth middleware the other dedup mutation routes (merge/dismiss) already use.
5. In handler.go's existing MergeDedupCandidate response (~line 1483, `httputil.RespondWithOK(c, gin.H{"status": "merged", "result": result, "keep_id": keepID})`), add `"journal_key": journalKey` to the response body so a client/operator can discover the undo key without reading logs.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_147.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty journal (no merges yet): ListMergeJournal returns an empty array, not null/error.
- limit=0 or missing: default to a sane cap (e.g. 50) — ListAutoMergeJournalEntries(0) currently means UNLIMITED per its doc comment, so the handler must supply a real default rather than passing through a raw 0.
- Undo called on a Tier-1 auto-merge journal entry vs a review-lane one: both share the same AutoMergeJournalEntry shape, so no special-casing should be needed — verify with a test using both origins.

## Tests

- internal/server/handlers/dedup/handler_test.go: TestUndoMerge_Success — mock DedupEngine.UnmergeAuto to succeed, POST the endpoint, assert 200.
- internal/server/handlers/dedup/handler_test.go: TestUndoMerge_UnknownKey_404 — mock UnmergeAuto to return the 'no journal entry' error, assert 404 not 500.
- internal/server/handlers/dedup/handler_test.go: TestListMergeJournal_ReturnsEntries — mock ListAutoMergeJournalEntries, assert response shape.
- internal/server/handlers/dedup/handler_test.go: TestMergeDedupCandidate_ResponseIncludesJournalKey — assert the existing merge endpoint's JSON body now has a non-empty journal_key field.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/dedup/... -run Undo -v` passes.
- [ ] `grep -n "UndoMerge\|ListMergeJournal" internal/server/handlers/dedup/handler.go` returns both new handler funcs.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_147.md`.

## Commit message

```
feat(server-handlers): Expose UnmergeAuto through an admin undo-merge endpoint (lis (MERGE-UNDO)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/server/handlers/dedup/... -run Undo -v` passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Should land AFTER part 2 (full external-ID + write-back reversal) so the exposed endpoint does not advertise a bigger guarantee ('undo this merge') than UnmergeAuto currently delivers (book-record-only). If shipped before part 2, the endpoint doc/response must say explicitly that external-ID and iTunes state are NOT reverted yet.
