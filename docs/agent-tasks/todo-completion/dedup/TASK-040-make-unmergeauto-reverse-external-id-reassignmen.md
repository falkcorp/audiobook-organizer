<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-040-make-unmergeauto-reverse-external-id-reassignmen.md -->
<!-- version: 1.0.0 -->
<!-- guid: 194d3bdf-f505-4333-a288-3f7f802c0a93 -->
<!-- last-edited: 2026-08-21 -->

# TASK-040 — Make UnmergeAuto reverse external-ID reassignment and iTunes write-back removals, not just the book record (MERGE-UNDO)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Why:** Touches a prod-data correctness surface (external-ID mapping table, iTunes write-back queue) across three packages (database/merge/dedup) with a locking discipline (mergeSerializeMu) that must not be broken; needs a new interface method and careful reasoning about partial-failure ordering, not mechanical edits. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 17 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**MERGE-UNDO**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-040-make-unmergeauto-reverse-external-id-reassignmen" -b agent/dedup-040-make-unmergeauto-reverse-external-id-reassignmen origin/main
cd "$REPO/.worktrees/dedup-040-make-unmergeauto-reverse-external-id-reassignmen"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Capture, at merge time, exactly which external-ID mappings the loser owned before ReassignExternalIDs moved them to the winner, and which iTunes PIDs were enqueued for removal — then give UnmergeAuto a way to move those specific mappings back to the loser and re-enqueue those PIDs, so an undo restores the FULL pre-merge state (book record + external identity + iTunes write-back), not just the book record.

## Background (verify before editing)

- internal/merge/service.go:236-268 is the loser-cleanup loop inside Service.MergeBooks: for each loser it (a) collects iTunes PIDs via GetExternalIDsForBook, (b) calls eidStore.ReassignExternalIDs(loserID, winnerID) which moves ALL the loser's mappings (not just iTunes) to the winner, (c) enqueues EnqueueRemove(pid) for the iTunes PIDs, (d) soft-deletes the loser.
- ReassignExternalIDs (internal/database/pebble_store_externalids.go:148) is a blunt wholesale move — calling it a second time with (winnerID, loserID) after undo would move BACK every mapping the winner now holds, including ones the winner legitimately had before the merge, which is wrong. A correct undo needs a NEW targeted primitive that moves back only the mappings captured before the merge.
- merge.Result (internal/merge/service.go:74-78) currently only carries PrimaryID/VersionGroupID/MergedCount — it has no field for per-loser reversal data, so MergeJournaled (internal/dedup/merge_journaled.go) cannot currently persist what it would need to reverse.
- The iTunes write-back removal (WriteBackEnqueuer.EnqueueRemove) is fire-and-forget into an async batcher; by the time an undo runs the removal may already be applied to the ITL on disk. Full reversal of that side needs a new batcher capability (e.g. EnqueueUpsert/EnqueueAdd) that can reconstruct the track entry — this is the part of the task most likely to need a follow-up design call if the batcher's internals make an 'add back' non-trivial; flag rather than silently drop this half if it proves out of proportion to the rest.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "ReassignExternalIDs(book.ID, resolvedPrimaryID)" internal/merge/service.go   # 1 hit ~L254 — MergeBooks reassigns ALL of a loser's external IDs to the winner, irreversibly, with no record kept of the pre-reassignment owner
  grep -n "func (p \*PebbleStore) ReassignExternalIDs" internal/database/pebble_store_externalids.go   # 1 hit ~L148, body iterates GetExternalIDsForBook(oldBookID) unconditionally — ReassignExternalIDs moves ALL mappings for oldBookID to newBookID, it is not scoped to a subset
  grep -n "type AutoMergeJournalEntry struct" -A 25 internal/database/dedup_automerge_journal.go   # struct fields are Key/CandidateID/WinnerID/LoserID/WinnerPreMergeTS/LoserPreMergeTS/Tag/MergedAt only, no ext-id or PID field — AutoMergeJournalEntry has no field to record the loser's pre-merge external IDs or removed iTunes PIDs
  grep -n "RevertBookToVersion" internal/dedup/auto_resolve.go   # 2 hits inside UnmergeAuto ~L403,L410 — UnmergeAuto only calls RevertBookToVersion on both books, nothing else
  grep -n "type WriteBackEnqueuer interface" -A 4 internal/merge/service.go   # interface has exactly one method, EnqueueRemove(pid string) — WriteBackEnqueuer has no reverse (add-back) method, only EnqueueRemove
  ```

### Reuse — don't invent

- Use `GetExternalIDsForBook` in `internal/database/pebble_store_externalids.go` (verify: `grep -n "func (p \*PebbleStore) GetExternalIDsForBook" internal/database/pebble_store_externalids.go`) — do NOT write a parallel helper.
- Use `AsExternalIDReassigner` in `internal/merge/service.go` (verify: `grep -n "func AsExternalIDReassigner" internal/merge/service.go`) — do NOT write a parallel helper.

## Step-by-step

1. internal/database/dedup_automerge_journal.go: add two fields to AutoMergeJournalEntry: `LoserExternalIDs []ExternalIDMapping \`json:"loser_external_ids,omitempty\`` (the loser's full mapping set captured BEFORE ReassignExternalIDs ran) and `RemovedITunesPIDs []string \`json:"removed_itunes_pids,omitempty\`` (the PIDs enqueued for removal). Bump the file's version header and last-edited date.
2. internal/merge/service.go: extend `Result` (line 74) with `LoserExternalIDs map[string][]database.ExternalIDMapping` and `RemovedPIDs map[string][]string`, both keyed by loser book ID. Populate them inside the loop at lines 236-268: before calling eidStore.ReassignExternalIDs, snapshot `mappings` (already fetched at step (a), line 244) into `result.LoserExternalIDs[book.ID]`; snapshot `dupPIDs` into `result.RemovedPIDs[book.ID]` at step (c).
3. internal/database/pebble_store_externalids.go: add a new method `func (p *PebbleStore) ReassignSpecificExternalIDs(mappings []ExternalIDMapping, newBookID string) error` that, for each given mapping, deletes its current reverse key (`ext_id:book:<currentOwner>:<source>:<id>`) and rewrites it under newBookID — mirroring ReassignExternalIDs' batch logic (lines 154-180) but iterating the PASSED-IN mapping list instead of re-fetching by old book ID (the old book ID may have already been re-merged into something else by the time undo runs, so re-deriving 'current owner' from the mapping's own persisted BookID field, not from a caller-supplied oldBookID, is the correct semantics). Add it to the ExternalIDReassigner-shaped interface set as needed and to internal/database/mock_store.go's MockStore with a matching Func field.
4. internal/dedup/merge_journaled.go: after `de.mergeService.MergeBooks(...)` returns `result`, thread `result.LoserExternalIDs[loserID]` and `result.RemovedPIDs[loserID]` into the second `PutAutoMergeJournalEntry` call (the patch at line 110) as the two new journal fields.
5. internal/dedup/auto_resolve.go: in UnmergeAuto (line 389), after both RevertBookToVersion calls succeed, call the new store method to move `entry.LoserExternalIDs` back onto `entry.LoserID` (via a store-capability check analogous to merge.AsExternalIDReassigner — Engine will need its own accessor since it does not import internal/merge). For RemovedITunesPIDs: if a write-back batcher accessor exists on Engine, re-enqueue an add/upsert for each PID; if no such capability exists yet, log a warn per PID naming them explicitly (`slog.Warn("unmerge-auto: iTunes write-back removal not reversed, manual re-sync required", "pid", pid)`) rather than silently dropping the information — do not claim full reversal in the doc comment until the write-back half is actually implemented.
6. Update the SCOPE LIMIT doc comment at auto_resolve.go:383-388 to reflect what is now reversed vs. what still requires manual iTunes re-sync (if step 5's batcher capability is deferred).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_040.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A loser with zero external-ID mappings: LoserExternalIDs is nil/empty, ReassignSpecificExternalIDs must be a no-op, not an error.
- A mapping whose current owner is neither winner nor loser (a THIRD merge already moved it again since): ReassignSpecificExternalIDs must skip it rather than steal it back — check the mapping's live BookID before moving, per step 3's note.
- UnmergeAuto called twice on the same journal key (double-undo): the second call's RevertBookToVersion will target a book_ver snapshot timestamp that no longer represents 'pre-merge' since the first undo already restored it — document this as a known no-op/idempotent-ish case but not guaranteed safe; consider marking a journal entry 'reverted' after first successful undo and refusing a second.

## Tests

- internal/database/pebble_store_externalids_test.go: TestReassignSpecificExternalIDs_MovesOnlyGivenMappings — merge two books' external IDs via ReassignExternalIDs, then reverse a SUBSET with ReassignSpecificExternalIDs and assert only that subset moved back, the rest stayed on the winner.
- internal/dedup/auto_resolve_test.go: TestUnmergeAuto_RestoresExternalIDOwnership — merge two fixture books with distinct external-ID mappings via MergeJournaled, call UnmergeAuto with the returned journal key, assert GetExternalIDsForBook(loserID) returns the original mappings and GetExternalIDsForBook(winnerID) no longer includes them.
- internal/merge/service_test.go: TestService_MergeBooks_ResultCarriesLoserExternalIDsAndPIDs — assert Result.LoserExternalIDs and Result.RemovedPIDs are populated correctly for a merge with a stubbed eidStore/writeBackBatcher.

Anti-over-suppression test: `N/A — this is additive reversal logic, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/database/... ./internal/merge/... ./internal/dedup/... -run 'ExternalID|UnmergeAuto|ResultCarries' -v` all pass.
- [ ] `grep -n "LoserExternalIDs\|RemovedITunesPIDs" internal/database/dedup_automerge_journal.go` returns 2+ hits.
- [ ] Anti-over-suppression test: `N/A — this is additive reversal logic, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_040.md`.

## Commit message

```
feat(dedup): Make UnmergeAuto reverse external-ID reassignment and iTunes (MERGE-UNDO)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/database/... ./internal/merge/... ./internal/dedup/... -run 'ExternalID|UnmergeAuto|ResultCarries' -v` all pass.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Part 3 below (endpoint wiring) depends on this part existing, since a real undo endpoint should call the FULLY reversing UnmergeAuto, not the book-record-only version, to avoid shipping a fake-safe 'undo' button. Owner should decide whether the iTunes write-back reversal (step 5's batcher gap) is in-scope now or gets its own follow-up TODO if it turns out to need real design work in the write-back batcher.
