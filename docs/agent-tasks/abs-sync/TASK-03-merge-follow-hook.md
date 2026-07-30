<!-- file: docs/agent-tasks/abs-sync/TASK-03-merge-follow-hook.md -->
<!-- version: 1.0.0 -->
<!-- guid: 107a1324-ae34-4087-8405-2290a6fe7bb8 -->
<!-- last-edited: 2026-07-30 -->

# TASK-03 — Merge-follow hook: sync identity + progress survive a dedup merge (ABS-SYNC-ID-3)

**Priority:** P0 · **Effort:** L · **Recommended subagent:** Opus-class go-backend subagent, concurrency-aware · **Why:** this is the highest-risk task in the identity layer — `MergeBooks` has a documented prior race-condition history in this exact repo, and a mistake here silently orphans a device's listening position on the app's single most common operation (a dedup merge) · **Depends on:** TASK-01 (`sync_item` keyspace)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI), but this PR **must not merge** until its `-race` test AND the post-merge invariant assertions (not just "no race detected" — see step 9) both pass and are pasted in the PR body. This hook runs inside `MergeBooks`' existing critical section on every production merge from the moment it merges — a silent bug here is not cosmetic.
**File-ownership:** owns `internal/merge/**` exclusively for this wave. **Do not touch `internal/dedup/book_dedup.go`'s package-level `MergeBooks`** (a different, still-live function used by `internal/reconcile/itunes_heal.go` — see Background) **or `CombineBooks`** in the same file — both are explicitly out of scope for this task (see "What this task does NOT cover"). Depends on TASK-01's `internal/database/pebble_store_syncid.go` — if it hasn't merged yet, rebase is blocked; check `git log --oneline origin/main -- internal/database/pebble_store_syncid.go` before starting.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-merge-follow-hook" -b agent/abs-sync-merge-follow-hook origin/main
cd "$REPO/.worktrees/abs-sync-merge-follow-hook"
git rebase origin/main
```

Confirm TASK-01 has merged before writing code:
```bash
grep -n "func (p \*PebbleStore) RecordSyncMerge" internal/database/pebble_store_syncid.go
# Expected: 1 hit. If this fails, TASK-01 hasn't merged yet — stop and wait for it,
# don't reimplement its store methods here.
```

Module path is `github.com/falkcorp/audiobook-organizer` (**falkcorp**, not jdfalk).

## Goal

Wire the sync-identity layer (TASK-01) into `merge.Service.MergeBooks` so that when
book B merges into surviving book A: (1) A's `syncID` is unaffected and stays live,
(2) B's `syncID` becomes a redirect to A's, so any client still holding B's ID
resolves correctly, and (3) each user's progress on B merges onto A, favoring
whichever is further along. Must be **exactly-once** under concurrent merges — no
double-redirect, no duplicate `MergedFrom` entries, no lost progress. Read
[`docs/specs/2026-07-29-abs-sync-api-design.md`](../../specs/2026-07-29-abs-sync-api-design.md)
§4.2 (the model) and §5 (progress conflict resolution — for context; this task
implements a narrower, self-contained rule, see below) before writing code.

## Background (verify before editing)

- **`MergeBooks` does NOT change the surviving book's ID.** Re-verify:
  ```bash
  grep -n "resolvedPrimaryID := books\[bestIdx\].ID" internal/merge/service.go
  # Expected: 1 hit, ~line 187
  ```
  Losers are only soft-deleted (`SoftDeleteBook`, ~line 247) — their `Book.ID` is
  **also unchanged**. This is the load-bearing simplification for this task: **A's
  `sync_item:book:<A>` reverse index never needs to move.** You do NOT need
  `RepointSyncItem` here (that's for the untagged-move case, a different scenario —
  see TASK-01's brief). All this task needs is: ensure A has a `syncID` (mint if
  absent), and record a redirect from B's `syncID` (if any) to A's.
- **The whole read-modify-write is already serialized by a single process-wide
  mutex — do not add a second lock.** Re-verify:
  ```bash
  grep -n "mergeSerializeMu.Lock()" internal/merge/service.go
  # Expected: 1 hit inside MergeBooks, ~line 134 (immediately after the <2-books
  # check, held for the rest of the function via defer)
  cat internal/merge/serialize.go
  # Confirm: "serializes EVERY merge-family read-modify-write across the whole
  # process" — this is NOT a per-book-pair lock, it is a single global one.
  ```
  **This is a deliberate divergence from the general CLAUDE.md concurrency
  guidance** ("partition by book ID so parallel workers can never touch the same
  row"): that guidance targets whole-library batch loops, not a single
  already-fully-serialized RMW. Anything this task adds inside `MergeBooks`, after
  the lock is already held and before the function returns, is automatically
  exactly-once with respect to every other concurrent merge in the process —
  including two different merges that don't even share a book. Do not add a
  per-book-pair partition scheme on top; it would be redundant with, and could
  subtly fight, the existing global lock. State this reasoning in a code comment at
  the hook site so a future reader doesn't "fix" it by adding partitioning.
- **`dedup.MergeBooks` is a separate, still-live, HARD-DELETE path** — re-verify:
  ```bash
  grep -n "F6 (2026-07-18)" internal/dedup/book_dedup.go
  # Expected: 1 hit, ~line 386 — the comment explaining POST /audiobooks/merge was
  # rerouted OFF this function onto merge.Service.MergeBooks in 2026-07-18, and
  # this function is retained only for internal/reconcile/itunes_heal.go.
  ```
  **This task does not hook `dedup.MergeBooks`.** It hard-deletes losers via
  `store.DeleteBook` rather than soft-deleting them, which is a materially
  different code path with its own orphan-risk history (documented in that same
  comment for external IDs/iTunes). Hooking it is out of scope here — flag it as a
  follow-up if you notice reconcile-triggered merges bypassing sync-identity
  tracking, but do not implement a fix for it in this PR.
- **What this task does NOT cover:** `CombineBooks` (same file, absorbs several
  books into one multi-file book, hard-deletes shells) is a different operation
  from `MergeBooks` (version-group linking) and is **not** hooked by this task —
  the spec (§4.2) only names `MergeBooks`. Do not add a hook to `CombineBooks`;
  note it as a gap for a future task if asked.
- **`SoftDeleteBook` signature:** `func SoftDeleteBook(store database.Store, bookID
  string) error` (re-verify: `grep -n "^func SoftDeleteBook" internal/merge/service.go`
  — expected: 1 hit, ~line 478). Called once per loser inside the existing
  per-loser loop (~lines 210-250). Your hook runs in the same loop, or immediately
  after it, still inside the function (still under the lock).
- **Progress storage today** (`internal/database/pebble_store_playback.go`):
  `UserPosition{UserID, BookID, SegmentID, PositionSeconds, UpdatedAt}` keyed
  `upos:<userID>:<bookID>:<segmentID>` (re-verify: `grep -n 'upos:' internal/database/pebble_store_playback.go`
  — expected: several hits, e.g. ~line 29) and `UserBookState{UserID, BookID,
  Status, ProgressPct, TotalListenedSeconds, LastActivityAt, ...}`. **Both are keyed
  user-first** — there is **no existing reverse index from a book to the set of
  users with progress on it.** `ListUserPositionsForBook(userID, bookID)` still
  requires the userID up front (re-verify:
  `grep -n "func (p \*PebbleStore) ListUserPositionsForBook" internal/database/pebble_store_playback.go`
  — expected: 1 hit, ~line 57). This task therefore iterates `ListUsers()`
  (re-verify: `grep -n "func (p \*PebbleStore) ListUsers" internal/database/pebble_store_auth.go`
  — expected: 1 hit, ~line 106) and checks each user's state on both books. **This
  is NOT a whole-library-scale loop** (it's bounded by the number of app users —
  a household/family, not "hundreds/thousands+ items" per the CLAUDE.md
  concurrency rule's own threshold) — a plain sequential `for` loop is correct here,
  do not add a worker pool for it. Contrast this explicitly with TASK-04's backfill,
  which DOES need a worker pool because it loops over books (library-scale).
- **"Furthest position" — concrete rule for THIS task** (deliberately narrower than
  the fuller §5 policy that TASK-08 builds for live client PATCH conflict
  resolution, a different call site): compare `UserBookState.ProgressPct` (an
  already-normalized 0-100 value, comparable across two different-length books,
  unlike raw `PositionSeconds` which is meaningless across two books with
  different segment schemes); ties broken by `LastActivityAt` (more recent wins).
  Whichever `UserBookState` wins is written onto the winner's `BookID`, and that
  user's `UserPosition` rows are copied from the losing book onto the winner's
  `BookID` (same `SegmentID`s — they are opaque per-user bookkeeping, not
  meaningfully cross-book comparable either way). The losing side's rows for that
  book are always cleared afterward regardless of which book won, so no orphaned
  per-book progress survives under the loser's now-soft-deleted `BookID`.
- **Injection, not type-assertion-through-a-test-probe** — the existing concurrency
  test (`internal/merge/service_concurrent_test.go`) wraps `database.Store` in a
  `serializeProbe` struct that **embeds** the interface (re-verify:
  ```bash
  grep -n "type serializeProbe struct" internal/merge/service_concurrent_test.go
  # Expected: 1 hit, ~line 29 — embeds database.Store, overrides only
  # GetBookByID/UpdateBook
  ```
  ). If your hook does `database.AsSyncIdentityStore(ms.db)` **inside** `MergeBooks`
  every time it runs, that's fine in production (`ms.db` is the real
  `*PebbleStore`), but if a future test wraps `ms.db` in a similar probe for a
  DIFFERENT reason, the type assertion could silently return `nil` through the
  probe and no-op the whole hook without failing anything. Avoid the trap by using
  the **same constructor-time injection idiom this file already uses for
  `writeBackBatcher`** (re-verify: `grep -n "SetWriteBackBatcher\|writeBackBatcher WriteBackEnqueuer" internal/merge/service.go`
  — expected: 3 hits): add a `syncFollower` field set once in `NewService` via the
  type assertion, plus a `SetSyncFollower` setter tests can override directly with
  a real store-backed value. See step 3.

## Step-by-step

1. In `internal/merge/service.go`, add near the top (alongside `WriteBackEnqueuer`):
   ```go
   // SyncFollower is satisfied by anything that can record a merge in the ABS
   // sync-identity layer (internal/database's SyncIdentityStore, TASK-01).
   // Optional: nil is a valid, no-op value — a store that doesn't implement it
   // (e.g. a test double) simply means merges don't touch sync identity.
   type SyncFollower interface {
   	MintOrGetSyncID(bookID string) (string, error)
   	RecordSyncMerge(loserBookID, winnerBookID string) error
   }
   ```
2. Add a `syncFollower SyncFollower` field to `Service`, alongside `writeBackBatcher`.
3. In `NewService`, default-wire it:
   ```go
   func NewService(db database.Store) *Service {
   	return &Service{db: db, syncFollower: database.AsSyncIdentityStore(db)}
   }
   ```
   Add `SetSyncFollower(f SyncFollower)` mirroring `SetWriteBackBatcher` exactly, for
   tests to inject a real store-backed value directly (see the trap explained
   above).
4. Inside `MergeBooks`, **after** the per-loser cleanup loop (after step (d)
   `SoftDeleteBook`, still before the final `return &Result{...}`, i.e. still under
   `mergeSerializeMu`), add the sync-follow + progress-merge step. Best-effort,
   matching this file's existing style for optional side-effects (`eidStore`,
   `writeBackBatcher` are both `slog.Warn`-and-continue on error, never fail the
   merge) — a sync-identity hiccup must never block a merge that would otherwise
   succeed:
   ```go
   if ms.syncFollower != nil {
   	if _, err := ms.syncFollower.MintOrGetSyncID(resolvedPrimaryID); err != nil {
   		slog.Warn("merge sync-identity: mint winner syncID", "book", resolvedPrimaryID, "err", err)
   	} else {
   		for _, book := range books {
   			if book.ID == resolvedPrimaryID {
   				continue
   			}
   			if err := ms.syncFollower.RecordSyncMerge(book.ID, resolvedPrimaryID); err != nil {
   				slog.Warn("merge sync-identity: record merge", "loser", book.ID, "winner", resolvedPrimaryID, "err", err)
   			}
   			if err := mergeUserProgress(ms.db, book.ID, resolvedPrimaryID); err != nil {
   				slog.Warn("merge sync-identity: merge progress", "loser", book.ID, "winner", resolvedPrimaryID, "err", err)
   			}
   		}
   	}
   }
   ```
   Loop over `books`, not just the current loser inside the earlier loop, to keep
   this step visually separable and independently testable — do not interleave it
   into the earlier per-loser loop's body.
5. Implement `mergeUserProgress(db database.Store, loserBookID, winnerBookID string)
   error` as a new unexported function in a new file `internal/merge/sync_progress.go`
   (keeps this concern out of `service.go`'s already-long body):
   - `users, err := db.ListUsers()`; propagate a real error (this is not
     best-effort at this level — the caller above already treats the whole call as
     best-effort).
   - For each `user`:
     - `loserState, _ := db.GetUserBookState(user.ID, loserBookID)`. If `nil`,
       `continue` (nothing to merge for this user).
     - `winnerState, _ := db.GetUserBookState(user.ID, winnerBookID)`.
     - Decide the winner by the rule in Background ("furthest position"): loser
       wins if `winnerState == nil`, or `loserState.ProgressPct >
       winnerState.ProgressPct`, or (`==` and `loserState.LastActivityAt.After(winnerState.LastActivityAt)`).
     - If the loser's state wins: copy it onto the winner's book (same struct,
       `state.BookID = winnerBookID`) via `SetUserBookState`; then
       `positions, _ := db.ListUserPositionsForBook(user.ID, loserBookID)` and
       `db.SetUserPosition(user.ID, winnerBookID, pos.SegmentID,
       pos.PositionSeconds)` for each.
     - Always finish with `db.ClearUserPositions(user.ID, loserBookID)` regardless
       of which side won — the loser's book is soft-deleted; no per-user progress
       should be left resolvable only under its now-defunct `BookID`.
6. Add the file header to `internal/merge/sync_progress.go` (fresh guid).
7. Bump `internal/merge/service.go`'s header (version + last-edited; keep its
   existing guid).
8. Add a `changelog.d/` fragment: `changelog.d/abs-sync-merge-follow-hook.md`
   (guid `ce7f5e86-2f40-4b5e-8a2e-566f92dcd462`), category `Added`.

## How to test (TDD — write these first, run red, then implement)

Add to `internal/merge/service_test.go` or a new `internal/merge/sync_progress_test.go`:

1. **Basic redirect:** merge B into A (B loses, A wins). Assert:
   `database.AsSyncIdentityStore(store).ResolveSyncItem(bSyncID).RedirectTo ==
   aSyncID`, and A's `SyncItem.MergedFrom` contains `bSyncID` exactly once.
2. **Progress-merge, loser further along:** give a user `ProgressPct: 80` on B,
   `ProgressPct: 20` on A. After merge, the user's `GetUserBookState(user, aBookID)`
   has `ProgressPct == 80`, and `GetUserBookState(user, bBookID)` is `nil` (cleared).
3. **Progress-merge, winner further along:** reverse the percentages — A's state is
   untouched, B's is cleared.
4. **No prior progress:** neither book has any `UserBookState` — merge succeeds,
   no panic, no created rows.
5. **Idempotent re-merge:** call `MergeBooks` a second time with the same
   `bookIDs`/`primaryID` (mirrors the existing multi-call patterns in
   `service_test.go`). `MergedFrom` length is unchanged (still exactly one entry
   for B), not doubled.
6. **Concurrency (`-race`, mandatory):** copy the shape of
   `TestMergeBooks_ConcurrentSamePair_Serializes` (re-verify:
   `grep -n "const goroutines = 16" internal/merge/service_concurrent_test.go` —
   expected: 1 hit) — N=16 goroutines call `ms.MergeBooks([]string{aID, bID}, aID)`
   concurrently on ONE shared `Service` backed by a **real** `PebbleStore`
   (`database.NewPebbleStore(t.TempDir())`, not a mock — inject the follower via
   `SetSyncFollower(database.AsSyncIdentityStore(realStore))` per the injection
   note in Background). Seed one user with `ProgressPct: 50` on B and no state on
   A before the goroutines start. After `wg.Wait()`, assert **all** of:
   - `-race` reports nothing (run with `go test -race`).
   - `ResolveSyncItem(bSyncID).RedirectTo == aSyncID` (exactly, not empty, not a
     multi-hop chain).
   - `len(winnerSyncItem.MergedFrom) == 1` — **this is the load-bearing
     assertion**; a naive re-implementation that forgets the idempotency check in
     `RecordSyncMerge` (TASK-01) would append 16 times here even though
     `mergeSerializeMu` prevents a literal data race on the bytes.
   - The user's final `GetUserBookState(user, aBookID).ProgressPct == 50` (the
     lone progress record survived the merge exactly once, not zeroed by a lost
     update).

## Acceptance criteria

- [ ] `SyncFollower` interface + `syncFollower` field + `SetSyncFollower` +
      `NewService` default-wiring all present in `internal/merge/service.go`
- [ ] `mergeUserProgress` implemented in new file `internal/merge/sync_progress.go`
- [ ] Hook runs strictly inside `MergeBooks`' existing `mergeSerializeMu` critical
      section (verify by reading the diff — no new lock added)
- [ ] All 6 tests in "How to test" pass, including the concurrency test under
      `-race` with all 3 post-condition assertions holding (paste full `-v` output)
- [ ] `dedup.MergeBooks` and `CombineBooks` have zero diff — confirm with
      `git diff --stat origin/main -- internal/dedup/book_dedup.go` (empty) and
      manual read of the `CombineBooks` function (untouched)
- [ ] `go build ./...`, `gofmt -l`, `go vet ./internal/merge/...` all clean
- [ ] File headers bumped/added
- [ ] `changelog.d/abs-sync-merge-follow-hook.md` added

## Commit message

```
feat(abs-sync): follow sync identity + progress through MergeBooks (ABS-SYNC-ID-3)

A dedup merge is this app's core loop, and until now it silently orphaned
any ABS client's stored libraryItemId/progress for the loser book. Hooks
MergeBooks (only -- not dedup.MergeBooks or CombineBooks, both separate
hard-delete paths, out of scope here) inside its EXISTING mergeSerializeMu
critical section: the winner's syncID is minted if absent, the loser's
syncID becomes a redirect record (TASK-01's RecordSyncMerge), and each
user's progress is merged onto the winner favoring the higher ProgressPct
(ties by most-recent LastActivityAt). Exactly-once by construction --
MergeBooks already serializes every merge process-wide, so no additional
per-book-pair partitioning was added; a 16-goroutine -race test against a
real PebbleStore asserts a single redirect, a single MergedFrom entry, and
the correct surviving progress value after concurrent identical merges.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-merge-follow-hook
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/merge/service.go` already has a `syncFollower` field, the transform is
already done — run the acceptance checks instead of re-adding. Rollback = revert the
single commit (`git revert`) covering `service.go`'s additive diff and the new
`sync_progress.go` file; `MergeBooks`' pre-existing behavior (version-group linking,
external-ID reassignment, iTunes ITL cleanup, soft-delete) is completely unchanged by
this hook (it runs strictly after that logic, best-effort, never returns an error
from it) — reverting drops sync-identity tracking but cannot regress merge
correctness itself.
