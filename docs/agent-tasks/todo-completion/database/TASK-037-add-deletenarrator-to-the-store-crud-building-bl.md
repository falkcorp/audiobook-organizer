<!-- file: docs/agent-tasks/todo-completion/database/TASK-037-add-deletenarrator-to-the-store-crud-building-bl.md -->
<!-- version: 1.0.0 -->
<!-- guid: dcd79abf-90eb-49a6-b3bf-28f51acbac5c -->
<!-- last-edited: 2026-08-21 -->

# TASK-037 — Add DeleteNarrator to the store (CRUD building block only) (TODO.md L5271)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · database subagent · **Why:** Mechanical CRUD addition with a clear model (DeleteAuthor) to mirror, but touches the memdb dual-write path and the Store interface (multiple implementers to update), so needs care. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 5271 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Narrator equivalent of the empty-author purge.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-037-add-deletenarrator-to-the-store-crud-building-bl" -b agent/database-037-add-deletenarrator-to-the-store-crud-building-bl origin/main
cd "$REPO/.worktrees/database-037-add-deletenarrator-to-the-store-crud-building-bl"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `DeleteNarrator(id int) error` to the database.Store surface: declare it in internal/database/iface_catalog.go alongside CreateNarrator (L46), implement it on *PebbleStore in internal/database/pebble_store_authors.go modeled on DeleteAuthor (delete the narrator:%d record key and its narrator_name:%s index key in one batch, commit, then update the in-memory index), add a stub/passthrough on database.MockStore in mock_store.go, and add a DeleteNarratorFromMemDB helper in memdb_sync.go modeled on DeleteAuthorFromMemDB. This is infrastructure only — it does NOT wire up a maintenance.purge-empty-narrators operation (see part 2, which is blocked on the narrator-identity design question).

## Background (verify before editing)

- Author deletion (DeleteAuthor, pebble_store_authors.go:157) deletes the author:%d record, the author:name:%s index, cascades alias deletion, and (buggily, see L5290 below) attempts junction cleanup — DeleteNarrator should mirror the record+index deletion and memdb cleanup shape but does NOT need the junction-cleanup step in this task, since no caller needs it yet (see part 2).
- Narrator's key scheme differs slightly from author's: narrator uses `narrator_name:%s` (underscore) as its index key (pebble_store_authors.go:722), not `narrator:name:%s` (colon) the way author's index reads `author:name:%s` — get this exact or GetNarratorByName will silently stop finding narrators after a delete leaves a stale or wrongly-keyed index entry.
- There is no existing DeleteNarratorFromMemDB — memdb_sync.go has UpsertNarratorToMemDB (L327) and ReplaceBookNarratorsInMemDB (L360) but no delete counterpart; one must be added alongside DeleteAuthorFromMemDB (memdb_sync.go:293) as the model.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'DeleteNarrator' internal/database/*.go   # 0 hits — DeleteNarrator does not exist anywhere in the store layer
  grep -n 'narrator_name:%s\|narrator:%d' internal/database/pebble_store_authors.go   # >=2 hits, ~L719-722 — CreateNarrator exists and writes narrator:%d plus narrator_name:%s index keys
  grep -n 'CreateNarrator(name string) (\*Narrator, error)' internal/database/iface_catalog.go   # 1 hit, L46 — CreateNarrator is declared on the Store interface (the file to add DeleteNarrator's declaration alongside)
  grep -n 'func (p \*PebbleStore) DeleteAuthor(id int) error' internal/database/pebble_store_authors.go   # 1 hit, L157 — DeleteAuthor is the sibling method to mirror the shape of (batch delete + memdb cleanup)
  ```

### Reuse — don't invent

- Use `DeleteAuthor (structural model: batch delete record+name-index, commit, then memdb cleanup)` in `internal/database/pebble_store_authors.go` (verify: `grep -n 'func (p \*PebbleStore) DeleteAuthor' internal/database/pebble_store_authors.go`) — do NOT write a parallel helper.
- Use `UpsertNarratorToMemDB (existing memdb helper; a DeleteNarratorFromMemDB counterpart does not yet exist and must be added)` in `internal/database/memdb_sync.go` (verify: `grep -n 'func (p \*PebbleStore) UpsertNarratorToMemDB' internal/database/memdb_sync.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/iface_catalog.go, add `DeleteNarrator(id int) error` immediately after the existing `CreateNarrator(name string) (*Narrator, error)` declaration at L46.
2. In internal/database/pebble_store_authors.go, add a new method `func (p *PebbleStore) DeleteNarrator(id int) error` near CreateNarrator/GetNarratorByID/GetNarratorByName (~L740-780). Implementation: call GetNarratorByID(id) first (mirroring DeleteAuthor's early return on not-found); if nil, return nil (idempotent, mirroring DeleteAuthor). Open a batch; Delete narrator:<id> and narrator_name:<NormalizeAuthor(narrator.Name)> (use the same util.NormalizeAuthor call CreateNarrator uses at L722); commit with pebble.Sync; then call the new DeleteNarratorFromMemDB(id) added below.
3. In internal/database/memdb_sync.go, add `func (p *PebbleStore) DeleteNarratorFromMemDB(id int) { ... }` modeled on DeleteAuthorFromMemDB (L293) — remove the narrator's entry from whatever in-memory narrator index/map DeleteAuthorFromMemDB's author counterpart clears (read DeleteAuthorFromMemDB's body first to find the exact map(s) touched, then find the parallel narrator-side map populated by UpsertNarratorToMemDB at L327).
4. In internal/database/mock_store.go, add a DeleteNarratorFunc field plus a `func (m *MockStore) DeleteNarrator(id int) error` passthrough, modeled on the existing DeleteAuthor mock at ~L572.
5. Run `go build ./internal/database/...` to confirm all Store implementers (including any other iface_catalog.go conformance assertions) compile.
6. Do NOT add a maintenance op or wire this into any purge job in this task — that is part 2, blocked on the narrator-identity design question.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_037.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A narrator referenced by one or more book_narrators:<bookID> junction rows is NOT protected against deletion by this task's scope — deleting a still-referenced narrator is the caller's responsibility to avoid (exactly analogous to L5290's DeleteAuthor junction-cleanup gap, which this task deliberately does not attempt to fix for narrators either, to keep this a pure CRUD addition).

## Tests

- internal/database/pebble_store_authors_test.go — TestDeleteNarrator_RemovesRecordAndIndex: create a narrator via CreateNarrator, call DeleteNarrator, assert GetNarratorByID returns (nil, nil) and GetNarratorByName(name) also returns (nil, nil) (proves the narrator_name: index was cleaned up, not just the record).
- internal/database/pebble_store_authors_test.go — TestDeleteNarrator_UnknownID_Idempotent: call DeleteNarrator on a nonexistent id, assert it returns nil error (not an error) — mirrors DeleteAuthor's not-found-is-a-no-op contract.
- internal/database/pebble_store_authors_test.go — TestDeleteNarrator_MemDBSync: with UseMemDB enabled, delete a narrator and assert it no longer appears via the memdb-backed read path (whichever ListNarrators/GetNarratorByID variant reads through mem() when UseMemDB is true) — this is the anti-over-suppression test: without it, a DeleteNarrator that forgets the memdb cleanup step would still pass every Pebble-only test while leaving a stale entry visible through the (much more commonly hit) memdb read path.

Anti-over-suppression test: `TestDeleteNarrator_MemDBSync` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go build ./internal/database/... succeeds
- [ ] go test ./internal/database/... -run TestDeleteNarrator passes
- [ ] grep -n 'DeleteNarrator' internal/database/iface_catalog.go internal/database/pebble_store_authors.go internal/database/mock_store.go internal/database/memdb_sync.go each return >=1 hit
- [ ] Anti-over-suppression test: `TestDeleteNarrator_MemDBSync` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_037.md`.

## Commit message

```
feat(database): Add DeleteNarrator to the store (CRUD building block only) (TODO L5271)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go build ./internal/database/... succeeds`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Split from the TODO item's single bullet because the store-CRUD half is unconditionally useful infrastructure, while the purge-OP half (part 2) is explicitly gated by the item's own text on 'whatever decides the narrator identity question' (L5281's author-narrator swap repair).
