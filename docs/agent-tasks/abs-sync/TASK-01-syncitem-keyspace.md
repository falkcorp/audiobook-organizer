<!-- file: docs/agent-tasks/abs-sync/TASK-01-syncitem-keyspace.md -->
<!-- version: 1.0.0 -->
<!-- guid: 91f5eddf-74aa-4681-b720-5da23f785e02 -->
<!-- last-edited: 2026-07-30 -->

# TASK-01 — `sync_item` keyspace: durable 36-char UUID identity for `libraryItemId` (ABS-SYNC-ID-1)

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet-class go-backend subagent · **Why:** new keyspace + store methods, no existing call site to wire up yet — pure additive Pebble work with a clear test surface · **Depends on:** none (Wave 1)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI). This is additive-only (new file, new keys); nothing reads or writes these keys yet, so there is no way for this PR to change existing behavior.
**File-ownership:** owns `internal/database/pebble_store_syncid.go` (+ `_test.go`) exclusively — this is a Wave-1 file no other task touches. **Do NOT edit `internal/database/store.go`** (repo-wide rule — every new type/interface goes in its own file). TASK-02 (`TASK-02-syncfile-keyspace.md`) also targets `pebble_store_syncid.go` in the same wave — if it lands first, this task's methods are additive to the bottom of the file; do not reorder or reformat what TASK-02 added.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-syncitem-keyspace" -b agent/abs-sync-syncitem-keyspace origin/main
cd "$REPO/.worktrees/abs-sync-syncitem-keyspace"
git rebase origin/main
```

Module path is `github.com/falkcorp/audiobook-organizer` (**falkcorp**, not jdfalk) — every import in the code you write uses that path.

## Goal

Build the identity layer that lets an ABS client's `libraryItemId` survive everything
this app does to a Book except a hard delete: in-place retag, tagged move, untagged
move (which mints a new Book ULID today), and dedup merge. Read
[`docs/specs/2026-07-29-abs-sync-api-design.md`](../../specs/2026-07-29-abs-sync-api-design.md)
§1 (ground truth), §1.7.1 (why it must be a 36-char UUID, not our 26-char ULID), and
§4 (the full model, incl. §4.2b) before writing any code — this brief restates the
load-bearing parts but the spec has the full reasoning.

Deliverable: a new `internal/database/pebble_store_syncid.go` exposing a `SyncItem`
record type and store methods to mint, look up, resolve (following merge redirects),
and repoint sync identities — used later by TASK-03 (merge-follow), TASK-04
(backfill), and TASK-05 (survival tests). **This task does not wire anything up to a
live HTTP path** — there is no ABS handler yet (that's TASK-11, Wave 4). You are
building the foundation those consumers need; get the primitive set complete and
correct, because TASK-03/04/05 cannot add methods to your file.

## Background (verify before editing)

- **Why a 36-char UUID, not a ULID.** Absorb (a target client, GPL-3.0, source-audited
  2026-07-29) splits compound podcast keys by **fixed string offset**:
  `substring(0, 36)` / `substring(37)`, repeated at 4+ call sites. A 26-char ULID
  breaks episode splitting; anything longer than 36 chars gets mis-truncated into a
  wrong `/api/me/progress/...` path. The ID this task mints is exposed (by a later
  task) as `libraryItemId` — it MUST be exactly 36 characters, canonical hyphenated
  form (`8-4-4-4-12` hex groups, lowercase, e.g.
  `550e8400-e29b-41d4-a716-446655440000`).
- **No `google/uuid` import.** `go.mod` currently lists `github.com/google/uuid
  v1.6.0 // indirect` with **zero direct importers anywhere in the tree** — re-verify:
  ```bash
  grep -rn "google/uuid" go.mod
  # Expected: one hit, `github.com/google/uuid v1.6.0 // indirect`
  grep -rln "google/uuid" --include="*.go" . | grep -v _test
  # Expected: no output (nothing imports it directly)
  ```
  Moving it from indirect to direct requires a `go.mod`/`go.sum` diff, and **only
  TASK-11 may touch `go.mod`** (workstream rule). Mint the UUID by hand with
  `crypto/rand` instead — 16 lines of stdlib, no dependency change. Exact function to
  write (do not deviate from the bit-twiddling — it is what makes the string a valid
  UUIDv4 that satisfies Absorb's length check):
  ```go
  func newSyncID() (string, error) {
  	var b [16]byte
  	if _, err := rand.Read(b[:]); err != nil {
  		return "", err
  	}
  	b[6] = (b[6] & 0x0f) | 0x40 // version 4
  	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
  	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
  }
  ```
  Import `"crypto/rand"` (plain, no alias — matches the existing convention in
  `internal/database/pebble_store.go`'s `newULID()`, re-verify:
  `grep -n '"crypto/rand"' internal/database/pebble_store.go` — expected: 1 hit, line
  ~10).
- **Pattern for adding a capability without touching `store.go`.** This codebase's
  established idiom (re-verify: `grep -n "AsExternalIDReassigner" internal/merge/service.go`
  — expected: 2 hits, the func def and its call site in `MergeBooks`) is: define a
  small interface + a runtime type-assertion helper, and do NOT add the interface to
  the big `Store` interface in `store.go`. Follow it exactly:
  ```go
  // SyncIdentityStore is implemented by PebbleStore. Consumers that only have a
  // database.Store value obtain it via AsSyncIdentityStore's type assertion —
  // this keeps store.go untouched (repo rule: nobody edits it; every new
  // capability lives in its own file) and means MemStore/mocks are not required
  // to implement it.
  type SyncIdentityStore interface {
  	MintOrGetSyncID(bookID string) (string, error)
  	GetSyncIDForBook(bookID string) (string, bool, error)
  	ResolveSyncItem(syncID string) (*SyncItem, error)
  	RepointSyncItem(oldBookID, newBookID string) error
  	RecordSyncMerge(loserBookID, winnerBookID string) error
  }

  // AsSyncIdentityStore returns s as a SyncIdentityStore if it implements the
  // interface (true for *PebbleStore), or nil otherwise.
  func AsSyncIdentityStore(s any) SyncIdentityStore {
  	if s == nil {
  		return nil
  	}
  	if ss, ok := s.(SyncIdentityStore); ok {
  		return ss
  	}
  	return nil
  }
  ```
  Callers (TASK-03/04/05) MUST nil-check the result exactly like
  `merge/service.go`'s `eidStore != nil` checks — re-verify:
  `grep -n "eidStore != nil" internal/merge/service.go` (expected: 1 hit, ~line 227).
- **Key-builder convention.** Every existing keyspace uses `fmt.Sprintf("prefix:%s",
  id)` for point keys (re-verify: `grep -n 'fmt.Sprintf("blocked:hash:%s"' internal/database/pebble_store_blocklist.go`
  — expected: 2 hits). Follow it:
  - `sync_item:<syncID>` → the `SyncItem` record (JSON).
  - `sync_item:book:<bookID>` → raw bytes of `syncID` (the reverse index — same
    "value is the ID as raw bytes" convention as `book:path:<path>` → bookID; re-verify:
    `grep -n 'pathKey :=' internal/database/pebble_store.go` — expected: 1 hit,
    `pathKey := []byte(fmt.Sprintf("book:path:%s", book.FilePath))`, ~line 1680).
- **`GetBookByID` returns `(nil, nil)` on not-found**, never an error (re-verify:
  `grep -n "pebble.ErrNotFound" internal/database/pebble_store.go | head -3` — expected:
  ≥3 hits including one at `GetBookByID`, ~line 754). This store is a pure key-value
  layer — it does **not** validate that `bookID` refers to a real Book. Validation is
  the caller's job; do not add a `GetBookByID` round-trip inside these methods.
- **Concurrency inside `MintOrGetSyncID`.** Two ABS requests for the same
  never-before-seen book can race the classic check-then-write. Guard it with a
  package-level `sync.Mutex` scoped ONLY to the check-then-mint-then-write, mirroring
  the existing `mergeSerializeMu` idiom (re-verify:
  `grep -n "var mergeSerializeMu sync.Mutex" internal/merge/serialize.go` — expected: 1
  hit). Name it `syncIDMintMu`. Do not hold it across any Pebble iteration or I/O
  beyond the two point-reads/writes this method needs.

## Step-by-step

1. Create `internal/database/pebble_store_syncid.go` with the standard header
   (version `1.0.0`, fresh guid via `uuidgen | tr '[:upper:]' '[:lower:]'`,
   today's date).
2. Define:
   ```go
   type SyncItem struct {
   	SyncID        string   `json:"sync_id"`
   	CurrentBookID string   `json:"current_book_id,omitempty"`
   	CreatedAt     time.Time `json:"created_at"`
   	MergedFrom    []string `json:"merged_from,omitempty"`
   	RedirectTo    string   `json:"redirect_to,omitempty"`
   }
   ```
   `RedirectTo` empty = a live record (client-visible item); non-empty = this
   `syncID` was a merge loser and now redirects to the winner's `syncID`.
   `CurrentBookID` is meaningless on a redirect record — leave it as whatever it was
   at merge time; readers must follow `RedirectTo` first (see step 5).
3. Implement `MintOrGetSyncID(bookID string) (string, error)`:
   - Reject `bookID == ""` with an error (`fmt.Errorf("bookID required")`).
   - `syncIDMintMu.Lock(); defer syncIDMintMu.Unlock()`.
   - Point-get `sync_item:book:<bookID>`. If found, return the existing syncID
     (string of the raw value), `nil`.
   - If `pebble.ErrNotFound`: mint via `newSyncID()`, build a `SyncItem{SyncID: id,
     CurrentBookID: bookID, CreatedAt: time.Now()}`, marshal, and write BOTH keys
     in one `p.db.NewBatch()` (`sync_item:<id>` → the JSON, `sync_item:book:<bookID>`
     → raw bytes of `id`), `batch.Commit(pebble.Sync)`. Return the new id.
   - Any other error: propagate.
4. Implement `GetSyncIDForBook(bookID string) (string, bool, error)` — a read-only
   point-get of `sync_item:book:<bookID>`; returns `("", false, nil)` on
   `pebble.ErrNotFound` (NOT an error — "no sync item yet" is a normal state).
5. Implement `ResolveSyncItem(syncID string) (*SyncItem, error)` — reads
   `sync_item:<syncID>`, and if `RedirectTo != ""`, follows it to the next record,
   repeating. **Cap at 10 hops and track visited IDs in a `map[string]bool`**; if the
   cap is hit or a cycle is detected, return an error
   (`fmt.Errorf("sync item redirect chain too long or cyclic starting at %s", syncID)`)
   rather than looping forever or returning stale data. Return `(nil, nil)` if the
   starting `syncID` itself does not exist (mirrors `GetBookByID`'s not-found
   convention).
6. Implement `RepointSyncItem(oldBookID, newBookID string) error` — for the
   untagged-move case (§4.2: a move that mints a new Book ULID via version-linking).
   Look up `syncID` for `oldBookID` via the reverse index; if none exists, return
   `nil` (nothing to repoint — a book with no sync item yet has nothing to carry
   forward). Otherwise, in one batch: delete `sync_item:book:<oldBookID>`, write
   `sync_item:book:<newBookID>` → same syncID, and update the `SyncItem` record's
   `CurrentBookID` to `newBookID`. **This method has no caller yet** — the scanner's
   untagged-move/version-link path (`internal/scanner/scanner.go`, re-verify:
   `grep -n "Auto-linked hash-duplicate as version group" internal/scanner/scanner.go`
   — expected: 1 hit, ~line 2097) does not call it today, and wiring that call site up
   is explicitly **out of scope for this task** (see TASK-05's brief for why, and the
   follow-up this gap implies). Build and unit-test the primitive; do not touch
   `scanner.go`.
7. Implement `RecordSyncMerge(loserBookID, winnerBookID string) error` — the
   primitive TASK-03 calls from inside the merge hook:
   - `winnerSyncID, err := p.MintOrGetSyncID(winnerBookID)` (ensures the winner has
     one; merges can involve two books that never had a sync item minted yet).
   - `loserSyncID, has, err := p.GetSyncIDForBook(loserBookID)`. If `!has`, return
     `nil` — the loser never had a client-visible identity, nothing to redirect.
   - **Idempotency**: read the loser's current `SyncItem`; if `RedirectTo ==
     winnerSyncID` already, return `nil` (already recorded — safe to re-run, e.g. a
     retried merge or a backfill sweep that re-touches the same pair).
   - Otherwise, in one batch: set the loser's `SyncItem.RedirectTo = winnerSyncID`
     (write `sync_item:<loserSyncID>`), and append `loserSyncID` to the winner's
     `SyncItem.MergedFrom` **only if not already present** (dedup — a merge can be
     retried), then write `sync_item:<winnerSyncID>`. Commit both in one
     `NewBatch()`. **Do not delete or modify `sync_item:book:<loserBookID>`** — leave
     it pointing at `loserSyncID`; a future lookup of the loser's (still-soft-deleted)
     book resolves to `loserSyncID`, and `ResolveSyncItem` follows the redirect to the
     winner. Deleting it would make a stale client request 404 instead of correctly
     resolving.
8. Bump the file's own header if you touch it again after TASK-02 lands first in the
   same file (re-check `git log --oneline -- internal/database/pebble_store_syncid.go`
   before assuming you're creating it fresh).
9. Write `internal/database/pebble_store_syncid_test.go` (TDD: write these first, run
   them red, then implement):
   - Mint-on-first-encounter: `MintOrGetSyncID(bookA)` twice returns the same ID both
     times.
   - Two different books get two different IDs.
   - Minted ID is exactly 36 chars, matches
     `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
     (version-4/variant-1 pattern — assert this exactly, not just `len() == 36`).
   - `GetSyncIDForBook` on an unminted book returns `("", false, nil)`.
   - `RepointSyncItem` moves the reverse index and updates `CurrentBookID`; a
     `MintOrGetSyncID` call on the OLD bookID after repoint now returns a **brand
     new, different** ID (proving the reverse index really moved, not just got
     copied).
   - `RecordSyncMerge`: loser's `ResolveSyncItem` follows the redirect to the
     winner's live record; winner's `MergedFrom` contains the loser's ID; calling
     `RecordSyncMerge` a second time with the same pair is a no-op (same
     `MergedFrom` length, same `RedirectTo`).
   - Three-way chain: merge B into A, then merge A's *book* into a third book C
     (i.e. `RecordSyncMerge(A's bookID, C's bookID)`) — `ResolveSyncItem` on B's
     original syncID must resolve all the way to C's live record (proves the
     redirect-chain-following in step 5, not just one hop).
   - Concurrent mint race: `t.Run` with ~16 goroutines all calling
     `MintOrGetSyncID(sameBookID)` simultaneously (mirror
     `internal/merge/service_concurrent_test.go`'s goroutine-fan-out shape,
     re-verify: `grep -n "const goroutines" internal/merge/service_concurrent_test.go`
     — expected: 1 hit, `const goroutines = 16`); assert all 16 results are the
     identical string, and `sync_item:book:<bookID>` has exactly one live record
     (no orphaned second `sync_item:<X>` record from a lost race).
   - Use the `database.NewPebbleStore(filepath.Join(t.TempDir(), "..."))` +
     `t.Cleanup(store.Close)` pattern (re-verify:
     `grep -n "func newPebbleStoreForISBN" internal/database/pebble_store_isbn_index_test.go`
     — expected: 1 hit — copy that helper's shape, new name).
10. Add a `changelog.d/` fragment: `changelog.d/abs-sync-syncitem-keyspace.md`
    (guid `791ba524-e776-4bee-861d-7df49c26b297`), category `Added`, describing the
    new durable identity keyspace and why (36-char UUID, not ULID — Absorb's
    fixed-offset split).

## How to test

```bash
go build ./...
gofmt -l internal/database/pebble_store_syncid.go internal/database/pebble_store_syncid_test.go
go vet ./internal/database/...
go test ./internal/database/... -run SyncID -race -count=1 -v
```

Expected: `gofmt -l` prints nothing (already formatted); `go vet` clean; every
`TestSyncID*`/`TestSyncItem*` test passes, and the concurrent-mint test passes under
`-race` with **no** data race reported (the mutex is what prevents one, not Pebble's
own internal locking — the batch write itself is not what's racing, the
check-then-mint decision is).

Paste the full `-v` test output in the PR body.

## Acceptance criteria

- [ ] `internal/database/pebble_store_syncid.go` exists with `SyncItem`,
      `SyncIdentityStore`, `AsSyncIdentityStore`, `newSyncID`,
      `MintOrGetSyncID`, `GetSyncIDForBook`, `ResolveSyncItem`, `RepointSyncItem`,
      `RecordSyncMerge` — all on `*PebbleStore`, none of them added to
      `internal/database/store.go`
- [ ] `grep -c "func (p \*PebbleStore)" internal/database/pebble_store_syncid.go`
      returns at least 5 (the 5 public methods above)
- [ ] Minted IDs match the UUIDv4 regex above, verified by an explicit test assertion
- [ ] Redirect-chain test (3-way: B→A→C) passes
- [ ] Concurrent mint-race test passes under `-race` with no race detected and no
      duplicate live record
- [ ] `go build ./...`, `gofmt -l`, `go vet ./internal/database/...` all clean
- [ ] `internal/database/store.go` has **zero** diff (`git diff --stat origin/main -- internal/database/store.go` prints nothing)
- [ ] File headers present and bumped on every created/modified file
- [ ] `changelog.d/abs-sync-syncitem-keyspace.md` added

## Commit message

```
feat(abs-sync): add sync_item keyspace for durable ABS identity (ABS-SYNC-ID-1)

libraryItemId is the key every ABS client stores progress/bookmarks against.
Book ULIDs churn under untagged moves (version-link mints a new one) and
dedup merges (surviving ULID changes), so the ID exposed to clients must be
a separate, never-reused identity. Mints a 36-char UUIDv4 (not our 26-char
ULID -- Absorb splits ids by fixed offset substring(0,36)/substring(37) and
breaks on the wrong length) via crypto/rand, avoiding a google/uuid go.mod
change reserved for TASK-11. sync_item:<id> + reverse index
sync_item:book:<bookID>; RepointSyncItem and RecordSyncMerge are the
primitives TASK-03/04/05 build on next.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-syncitem-keyspace
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/database/pebble_store_syncid.go` already has `MintOrGetSyncID` defined,
the transform is already done — run the acceptance checks instead of re-adding.
Rollback = revert the single commit; nothing else in the tree references this file
yet (verify: `git grep -l "AsSyncIdentityStore\|MintOrGetSyncID" -- ':!internal/database/pebble_store_syncid*'`
should be empty until TASK-03/04/05 land), so reverting is a clean, blast-radius-zero
delete.
