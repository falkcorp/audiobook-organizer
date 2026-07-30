<!-- file: docs/agent-tasks/abs-sync/TASK-05-id-survival-tests.md -->
<!-- version: 1.0.0 -->
<!-- guid: 813161ae-2cae-4b7a-9196-d409c126cb9d -->
<!-- last-edited: 2026-07-30 -->

# TASK-05 — ID-survival acceptance suite: rename / move / retag / merge / replace (ABS-SYNC-ID-5)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class test-writing subagent · **Why:** pure test authorship, no production code — the acceptance bar for the whole identity layer (spec §4.3), one file that proves or disproves the other four tasks in one read · **Depends on:** TASK-01, TASK-02, TASK-03

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI). Test-only PR — zero production-code risk. **If any scenario fails, that is a real finding: STOP and report which one, do not weaken the assertion to make it pass.**
**File-ownership:** `*_test.go` only. Owns one new file, `internal/merge/sync_identity_survival_test.go` — does not touch any file TASK-01/02/03/04 own. Depends on all three merging first (needs `RecordSyncMerge`, `RepointSyncItem`, `RepointSyncFile`, and the `MergeBooks` hook to exist).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-id-survival-tests" -b agent/abs-sync-id-survival-tests origin/main
cd "$REPO/.worktrees/abs-sync-id-survival-tests"
git rebase origin/main
```

Confirm TASK-01, TASK-02, and TASK-03 have all merged before writing tests:
```bash
grep -n "func (p \*PebbleStore) RepointSyncItem\|func (p \*PebbleStore) RecordSyncMerge" internal/database/pebble_store_syncid.go
grep -n "func (p \*PebbleStore) RepointSyncFile" internal/database/pebble_store_syncid.go
grep -n "syncFollower SyncFollower" internal/merge/service.go
# Expected: 1, 1, 1 hits respectively. If any is missing, stop — this task only
# asserts existing behavior, it implements nothing new.
```

Module path is `github.com/falkcorp/audiobook-organizer` (**falkcorp**, not jdfalk).

## Goal

Write the single test file that is the acceptance bar for this whole identity layer
(spec §4.3): for each of **rename, move (tagged and untagged), retag, merge, and file
replace**, prove the `syncID`/`syncFileID` **and** the associated user progress
survive the operation. Read
[`docs/specs/2026-07-29-abs-sync-api-design.md`](../../specs/2026-07-29-abs-sync-api-design.md)
§4.3 before writing — this brief expands it into concrete, runnable assertions per
scenario, including one **documented, deliberate gap** (see "Untagged move" below) —
report it if you disagree with the scoping, don't quietly patch around it.

## Background (verify before editing)

- **Why one new file in `internal/merge`, not `internal/database`:** the merge
  scenario needs `merge.Service.MergeBooks`, so the suite has to live in (or import)
  `internal/merge`. `internal/merge` already imports `internal/database` for
  everything else this suite needs (Book/BookFile/User CRUD, the sync-identity
  primitives), so one file in `internal/merge` covers every scenario without a new
  package. It reuses the existing `setupTestStore(t) database.Store` helper
  (re-verify: `grep -n "^func setupTestStore" internal/merge/service_test.go` —
  expected: 1 hit, ~line 20) already used by `TestService_MergeBooks*`.
- **Rename / retag / tagged-move all leave `Book.ID` unchanged.** These three
  scenarios are, at the store level, identical: some fields on an existing `Book`
  row change (`FilePath`, `Title`, tags) via `UpdateBook(id, book)` with the **same**
  `id`. Since `sync_item:book:<bookID>` is keyed on that unchanged ID, **nothing
  needs to happen for the syncID to survive** — the test is a straightforward
  "prove nothing broke" check, not a test of new repoint logic. Do not write
  bespoke store code for these three; call `UpdateBook` directly, same as
  production code paths do.
- **🔴 Untagged move is a real gap — read this before writing that scenario.**
  When an untagged file is re-matched by hash on a later scan, `internal/scanner/scanner.go`
  mints a **new** `Book.ID` via `CreateBook` and links it to the old row via
  `VersionGroupID` (re-verify: `grep -n "Auto-linked hash-duplicate as version group" internal/scanner/scanner.go`
  — expected: 1 hit, ~line 2097; note it calls `CreateBook`, not `MergeBooks` —
  different code path entirely). **No task in this wave (01-05) wires
  `RepointSyncItem` into that call site.** TASK-01 built the primitive; nothing
  calls it in production yet. This task therefore tests the **primitive in
  isolation** — call `database.AsSyncIdentityStore(store).RepointSyncItem(oldID,
  newID)` directly, as a stand-in for what the (not-yet-built) scanner hook would
  do — and the test's doc comment must say explicitly that wiring this into
  `scanner.go` is an **open follow-up**, not covered by this suite's green result.
  A reviewer reading only the acceptance criteria checkboxes below must not walk
  away thinking untagged-move survival is proven end-to-end; it is proven only at
  the primitive level. Flag this in your PR description too.
- **Merge scenario exercises TASK-03's hook** — call `ms.MergeBooks([]bookID_A,
  bookID_B}, primaryID)` through a real `*merge.Service` (not a mock), same as
  `internal/merge/service_test.go` does, and assert via
  `database.AsSyncIdentityStore(store).ResolveSyncItem(...)` that the loser's
  syncID redirects to the winner's.
- **File replace: today's mechanism is `UpdateBookFile(id, file)` with the SAME
  `id`** (re-verify: `grep -n "file.ID = id" internal/database/pebble_store_bookfiles.go`
  — expected: exactly 1 hit, inside `CreateBookFile` only — `UpdateBookFile` never
  reassigns `file.ID`). So the "replace" scenario in THIS suite is: create a
  `BookFile`, mint its `syncFileID`, then call `UpdateBookFile` with new
  `FileHash`/`FileSize`/`Duration` (simulating a remux) but the same `id` — and
  assert the `syncFileID` is unchanged (same reasoning as rename/retag above: no
  new logic needed because the identity key didn't change). **Additionally** write
  one test that exercises `RepointSyncFile` directly (same "primitive, not a wired
  production path" framing as the untagged-move gap) for the hypothetical future
  case where a replace path deletes the old `BookFile` row and creates a genuinely
  new one — document this the same way.
- **Progress survival, concretely:** for the merge scenario, reuse the exact
  "furthest position" rule TASK-03 implements (`UserBookState.ProgressPct`, ties by
  `LastActivityAt`) — do not invent a different comparison in this test; assert
  against that rule so this suite and TASK-03's own tests agree on what "survives"
  means. For rename/retag/move/replace, progress survival is trivial (same
  `BookID` throughout — `GetUserBookState`/`GetUserPosition` calls never even
  change their key), so a single before/after equality check per scenario is
  sufficient; do not over-build.

## Step-by-step

1. Create `internal/merge/sync_identity_survival_test.go` with the standard header
   (version `1.0.0`, fresh guid, today's date). Package `merge` (same package as
   `service_test.go` — reuses `setupTestStore` without an import).
2. Write a small local helper `seedBookWithProgress(t, store, userID, title,
   format string, progressPct int) (bookID string)` — creates a `Book`, a
   `UserBookState{ProgressPct: progressPct, LastActivityAt: time.Now()}`, and one
   `UserPosition`. Used by every scenario below to avoid repeating setup.
3. **`TestSyncIdentitySurvives_Rename`**: seed a book, mint its syncID
   (`database.AsSyncIdentityStore(store).MintOrGetSyncID(bookID)`), call
   `UpdateBook(bookID, &Book{..., Title: "New Title"})` (same ID, new title),
   assert `GetSyncIDForBook(bookID)` returns the identical syncID, and
   `GetUserBookState(userID, bookID)` is unchanged.
4. **`TestSyncIdentitySurvives_MoveTagged`**: same shape as rename, but change
   `FilePath` instead of `Title` (tagged move — embedded ID keeps `Book.ID`
   stable, so this is store-level identical to rename; keep the test separate
   anyway since it documents a distinct real-world scenario a reader of this
   suite should see named explicitly).
5. **`TestSyncIdentitySurvives_MoveUntagged_Primitive`**: seed a book (`oldID`),
   mint its syncID, seed progress. Create a second `Book` row (`newID`, a fresh
   ULID) representing the version-linked re-scan result, with the SAME
   `VersionGroupID` set on both (mirror the real scanner's `existing`/`dbBook`
   shape). Call `RepointSyncItem(oldID, newID)` directly. Assert:
   - `GetSyncIDForBook(newID)` returns the ORIGINAL syncID.
   - `GetSyncIDForBook(oldID)` now returns `(_, false, nil)` (reverse index moved,
     not copied).
   - A doc comment directly above this test states, verbatim-ish: "This calls the
     RepointSyncItem primitive directly because no production code path invokes it
     yet — internal/scanner/scanner.go's untagged-move/version-link path does not
     call it. Wiring that call site is an open follow-up, not proven by this test."
6. **`TestSyncIdentitySurvives_Retag`**: same shape as rename/move-tagged —
   change some tag-derived field (e.g. `Narrator`) via `UpdateBook`, same ID,
   assert syncID + progress unchanged.
7. **`TestSyncIdentitySurvives_Merge`**: seed two books A (winner, m4b, e.g.
   `ProgressPct: 30`) and B (loser, mp3, `ProgressPct: 70`) for the SAME user, real
   `merge.NewService(store)` with `SetSyncFollower(database.AsSyncIdentityStore(store))`
   (per TASK-03's injection idiom), call `ms.MergeBooks([]string{aID, bID}, aID)`.
   Assert:
   - `ResolveSyncItem(bSyncID).RedirectTo == aSyncID`.
   - Winner's `SyncItem.MergedFrom` contains `bSyncID`.
   - `GetUserBookState(userID, aID).ProgressPct == 70` (loser was further along —
     its state won and moved onto the winner's `BookID`, per TASK-03's rule).
   - `GetUserBookState(userID, bID)` is `nil` (cleared).
8. **`TestSyncIdentitySurvives_FileReplace_SameID`**: create a `BookFile`, mint its
   `syncFileID` via `AsSyncFileStore(store).MintOrGetSyncFileID(bookID, fileID)`,
   call `UpdateBookFile(fileID, &BookFile{..., FileHash: "newhash"})` (same `id` —
   simulates a remux via today's actual mechanism), assert
   `GetSyncFileID(bookID, fileID)` is unchanged.
9. **`TestSyncIdentitySurvives_FileReplace_Primitive`**: create `BookFile` A (old
   physical file), mint its `syncFileID`, call `RepointSyncFile(bookID, oldFileID,
   newFileID)` directly (a hypothetical delete-and-recreate replace path — no
   production caller exists). Assert `GetSyncFileID(bookID, newFileID)` returns the
   ORIGINAL `syncFileID` and `GetSyncFileID(bookID, oldFileID)` now returns
   `(_, false, nil)`. Same "primitive only" doc-comment framing as step 5.
10. Run every test, confirm green, then run the FULL package once more with
    `-race` to confirm this new file didn't introduce anything the existing
    `service_concurrent_test.go` would flag.
11. Add a `changelog.d/` fragment: `changelog.d/abs-sync-id-survival-tests.md` (guid
    `1018e16b-75c5-42a9-b135-2aa21de776c0`), category `Added`, and explicitly note
    the untagged-move / file-replace primitive-only scope in the fragment body (not
    just the PR description) — this is the kind of caveat that should survive into
    the permanent changelog, not just review discussion.

## How to test

```bash
go build ./...
gofmt -l internal/merge/sync_identity_survival_test.go
go vet ./internal/merge/...
go test ./internal/merge/... -run SyncIdentitySurvives -race -count=1 -v
go test ./internal/merge/... -race -count=1   # full package, confirm nothing else broke
```

Paste both outputs in the PR body. Every `TestSyncIdentitySurvives_*` must be green;
if one fails, that is a genuine defect in TASK-01/02/03 — **stop and report which
scenario and why**, do not adjust the assertion to match broken behavior.

## Acceptance criteria

- [ ] `internal/merge/sync_identity_survival_test.go` created, package `merge`
- [ ] All 7 scenarios present: rename, move-tagged, move-untagged (primitive,
      explicitly labeled as such), retag, merge, file-replace-same-id,
      file-replace-primitive (explicitly labeled)
- [ ] Merge scenario asserts BOTH the redirect AND the correct surviving
      `ProgressPct` per TASK-03's furthest-position rule
- [ ] Untagged-move and file-replace-primitive tests each carry an explicit doc
      comment stating they exercise the primitive only, not a wired production
      path
- [ ] Full `internal/merge` package passes under `-race`, no regressions
- [ ] Zero production code touched — `git diff --stat origin/main -- ':!internal/merge/sync_identity_survival_test.go' ':!changelog.d/**'`
      prints nothing
- [ ] File header present
- [ ] `changelog.d/abs-sync-id-survival-tests.md` added, and it names the
      untagged-move / file-replace scope gap explicitly

## Commit message

```
test(abs-sync): ID-survival acceptance suite for sync identity (ABS-SYNC-ID-5)

Spec section 4.3's acceptance bar: syncID and progress must survive
rename, move (tagged/untagged), retag, merge, and file replace. Adds
internal/merge/sync_identity_survival_test.go covering all 7 concrete
scenarios against real PebbleStore-backed stores and a real
merge.Service. Two scenarios (untagged move, file-replace-via-new-id)
exercise TASK-01/02's RepointSyncItem/RepointSyncFile primitives
directly rather than a live call site, because no production code path
invokes either yet -- internal/scanner/scanner.go's version-link path
mints a new Book.ID via CreateBook without repointing sync identity.
That wiring is an open follow-up and is called out explicitly in both
the test doc comments and this changelog entry so the gap doesn't read
as closed.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-id-survival-tests
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/merge/sync_identity_survival_test.go` already exists with all 7
`TestSyncIdentitySurvives_*` functions, the transform is already done — run the
acceptance checks instead of re-adding. Rollback = revert the single commit (delete
the test file); it is pure test code with zero production dependencies added, so
reverting cannot regress runtime behavior — it only removes coverage.
