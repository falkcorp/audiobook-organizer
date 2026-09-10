<!-- file: docs/agent-tasks/todo-completion/database/TASK-030-add-a-compare-and-swap-on-collection-version-to-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5880334b-e2d7-4bc6-8244-a6fa7c43d886 -->
<!-- last-edited: 2026-09-02 -->

# TASK-030 — Add a compare-and-swap on Collection.Version to PebbleStore.UpdateCollection (TODO.md L4501)

> **Status 2026-09-02:** ✅ DONE — PR #2760 merged 2026-08-23 (102f2e6a0).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** A concurrency/CAS fix in the storage layer with parity needed across PebbleStore and MockStore, plus multiple call sites needing conflict-error translation — mechanical but touches enough files to warrant care. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4501 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`AddBookToCollection` is read-modify-write with " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-08.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-030-add-a-compare-and-swap-on-collection-version-to-" -b agent/database-030-add-a-compare-and-swap-on-collection-version-to- origin/main
cd "$REPO/.worktrees/database-030-add-a-compare-and-swap-on-collection-version-to-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a compare-and-swap check to PebbleStore.UpdateCollection: reject the write with a distinguishable conflict error when the caller's col.Version does not match the currently-stored prev.Version, so two concurrent read-modify-write cycles (e.g. two AddBookToCollection calls) cannot silently lose one's change.

## Background (verify before editing)

- internal/server/handlers/abs/collections.go:288-330 (AddBookToCollection) and :332-371 (RemoveBookFromCollection) both read the collection via h.lookupCollection(c), mutate col.BookIDs in memory, then call h.collections.UpdateCollection(col) — classic read-modify-write with no protection.
- internal/server/handlers/collections.go:280-345 (native PUT UpdateCollection) has the identical shape via h.load(c).
- internal/database/pebble_store_collections.go:195-217 already loads `prev` via p.GetCollection(col.ID) inside UpdateCollection itself — the CAS check slots in right after that load, before the unconditional Version bump.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'col.Version = prev.Version' internal/database/pebble_store_collections.go   # 1 hit at L217 — UpdateCollection increments Version unconditionally with no CAS check
  grep -n 'UpdateCollection(col)' internal/server/handlers/collections.go internal/server/handlers/abs/collections.go   # 6 hits total across both files — all 6 call sites read-then-write, so col.Version is always populated with the just-read value before this call
  grep -n 'Version         int' internal/database/store.go   # 1 hit inside the Collection struct — Collection already carries a Version field
  ```

### Reuse — don't invent

- Use `existing 'already in use' string-matched error pattern for translating a store error to an HTTP conflict` in `internal/server/handlers/collections.go` (verify: `grep -n "already in use" internal/server/handlers/collections.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/pebble_store_collections.go's UpdateCollection, immediately after `prev, err := p.GetCollection(col.ID)` and the nil-check (around line 195-201), add: `if col.Version != prev.Version { return fmt.Errorf("collection %s version conflict: expected %d, got %d", col.ID, prev.Version, col.Version) }`.
2. Update MockStore.UpdateCollection (internal/database/mock_store.go:1659) with the same CAS semantics for parity between the two Store implementations (dual-implementation divergence is exactly the class of bug #2406/#2410/#2411 fixed previously — do not let this be a third instance).
3. In internal/server/handlers/abs/collections.go's AddBookToCollection (line 324) and RemoveBookFromCollection (line 365), and in internal/server/handlers/collections.go's UpdateCollection (line 341), extend the existing `strings.Contains(err.Error(), "already in use")` conflict-detection pattern to also match `"version conflict"`, translating it to httputil.RespondWithConflict(c, ...) / respondError(c, http.StatusConflict, ...) instead of a 500.
4. Decide whether callers should retry-once (re-read, re-apply the same mutation, re-save) or simply surface the 409 to the client for a manual retry — given these are low-contention paths (single-book adds/removes), a bare 409 without auto-retry is the simpler, honest choice; do not add retry logic unless asked.
5. Bump version headers on all touched files.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_030.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A freshly-created collection (Version==0, never updated) being updated for the first time: the CAS check `col.Version != prev.Version` must still pass since both are 0 immediately after CreateCollection — verify CreateCollection initializes Version to 0 explicitly.
- A caller that constructs a bare Collection struct without ever reading the current row first (Version left at its Go zero value) and that zero happens to not match a since-updated prev.Version should get a normal conflict, not a panic or silent success — this is actually the CORRECT behavior (it protects against exactly that kind of blind overwrite) and should be asserted, not special-cased away.

## Tests

- {'file': 'internal/database/pebble_store_collections_test.go', 'name': 'TestUpdateCollection_VersionConflict_Rejected (new)', 'asserts': "calling UpdateCollection twice concurrently-simulated (read once, save twice with the same stale Version) — the second save fails with a version-conflict error, and the first save's change is not silently overwritten"}
- {'file': 'internal/database/pebble_store_collections_test.go', 'name': 'TestUpdateCollection_CorrectVersion_Succeeds (anti-over-suppression, new)', 'asserts': 'a normal read-then-write with the correct current Version still succeeds — this CAS must not reject legitimate sequential updates'}
- {'file': 'internal/server/handlers/abs/collections_test.go', 'name': 'TestAddBookToCollection_ConcurrentAdds_SecondGets409 (new)', 'asserts': 'two AddBookToCollection calls built from the same stale read produce one 200 and one 409, not two 200s where one silently loses its book'}

Anti-over-suppression test: `TestUpdateCollection_CorrectVersion_Succeeds` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/database/... -run TestUpdateCollection` passes.
- [ ] `go test ./internal/server/handlers/... -run TestAddBookToCollection` passes.
- [ ] MockStore.UpdateCollection and PebbleStore.UpdateCollection agree on CAS behavior (both reject a stale-version write) — verify with `grep -n 'version conflict' internal/database/*.go` showing the message in both implementations.
- [ ] Anti-over-suppression test: `TestUpdateCollection_CorrectVersion_Succeeds` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_030.md`.

## Commit message

```
refactor(database): Add a compare-and-swap on Collection.Version to PebbleStore. (TODO L4501)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Not literally 'organize/rename/dedup/repair/schema/migration' per the review_critical rubric, so marked false, but this is still a real correctness fix on a shared, multi-writer row.
