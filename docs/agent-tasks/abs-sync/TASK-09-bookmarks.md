<!-- file: docs/agent-tasks/abs-sync/TASK-09-bookmarks.md -->
<!-- version: 1.0.0 -->
<!-- guid: 395459c2-fef1-498c-8ad1-fea120312b6b -->
<!-- last-edited: 2026-07-30 -->

# TASK-09 — Bookmarks CRUD, server-persisted (ABS-SYNC, Phase 6 foundation)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · go-backend
subagent (new Pebble keyspace + pure canonicalization logic, no HTTP wiring) · **Why:**
no named "bookmark" feature exists in this codebase today, so this task both defines the
type and implements first-class server persistence for it, on a clean untouched
keyspace · **Depends on:** TASK-08 (this task's pure logic lives in the same package
`internal/syncapi/progress/` that TASK-08 creates; do not start this task until
TASK-08's PR is merged)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI). New keyspace, no existing
callers — nothing to coordinate.
**File-ownership:** creates `internal/syncapi/progress/bookmarks.go` (+ `_test.go`) —
a **new file** in a directory TASK-08 also uses, but TASK-08 owns only
`policy.go`/`policy_test.go` there and merges first (wave 1 vs. this task's wave 2), so
there is no same-file collision as long as this task starts from a fetched
`origin/main` that already contains TASK-08's merged PR. Also creates
`internal/database/pebble_store_bookmarks.go` (+ `_test.go`) — a **brand-new file**.
This task does **NOT** edit `internal/database/store.go` (the composed `Store`
interface) or `internal/database/pebble_store_playback.go` — both are explicitly
off-limits per the workstream's file-ownership rule ("nobody edits store.go; every new
type goes in its own new file"). The new `BookmarkStore` interface this task defines
lives entirely inside the new `pebble_store_bookmarks.go` file and is **not** embedded
into the top-level `Store` interface — wiring it into an HTTP handler (which would need
that) is explicitly out of scope here and deferred to whichever later task builds the
ABS bookmark endpoints (Phase 6 in the spec's phase table).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-bookmarks" -b agent/abs-sync-bookmarks origin/main
cd "$REPO/.worktrees/abs-sync-bookmarks"
git rebase origin/main
```

**Before writing any code, confirm TASK-08 is merged into the `origin/main` you just
rebased onto:**
```bash
ls internal/syncapi/progress/policy.go internal/syncapi/progress/policy_test.go
# Expected: both files exist. If they do NOT exist, STOP — TASK-08 has not merged yet.
# Do not create internal/syncapi/progress/ yourself; wait and re-fetch/rebase later.
```

## Goal

Add a genuine, server-persisted, named **bookmark** feature: per (user, item) named
timestamps a listener can jump back to, keyed by `time` (seconds) and a `title`,
surviving a client reinstall. Two new files, split deliberately:

1. `internal/syncapi/progress/bookmarks.go` — **pure** type + canonicalization/
   validation logic (mirrors TASK-08's package: no I/O, no DB import). Defines the
   `Bookmark` type and the time-canonicalization rules that make "AudioBooth sends
   bookmark `time` as an Int, sometimes in a URL path segment, but decodes our
   responses as a Double" a non-issue.
2. `internal/database/pebble_store_bookmarks.go` — the actual PebbleDB CRUD
   implementation, using the pure package's canonicalization so the storage layer and
   any future HTTP layer agree on what counts as "the same bookmark."

**No HTTP handler is added in this task.** Wiring `/api/me/item/:id/bookmark` (create),
`PATCH .../bookmark` (update title), `DELETE .../bookmark/:time` (delete), and the
`bookmarks` field on `/api/me` is later Phase-6 scope and explicitly out of scope here.

## Background (verify before editing)

- **No bookmark list/CRUD feature exists anywhere in this codebase today.** The only
  existing "bookmark" concept is a single scalar field,
  `Book.ITunesBookmark *int64` (milliseconds, imported from iTunes) — **not a list**,
  one value per book, unrelated to the ABS named-bookmark concept this task builds.
  Verify:
  ```bash
  grep -n "ITunesBookmark" internal/database/store.go internal/database/bookcore.go
  # Expected: field declared as `ITunesBookmark *int64` (json:"itunes_bookmark,omitempty")
  # in both store.go (~:154) and bookcore.go (~:59) — a single pointer-to-int64, not a slice
  grep -rn "bookmarkSeconds\|ITunesBookmark) / 1000" internal/itunes/service/position_sync.go
  # Expected: confirms the unit is milliseconds (divided by 1000.0 to get seconds, ~:100)
  find internal -iname "*bookmark*" -not -path "*/itunes/*"
  # Expected: 0 hits before this task (no dedicated bookmark package/file exists yet)
  ```
- **The sibling per-user keyspace pattern to copy** is
  `internal/database/pebble_store_playback.go`'s `UserPosition` methods: key format
  `upos:<userID>:<bookID>:<segmentID>`, prefix-iterated with
  `pebble.IterOptions{LowerBound: prefix, UpperBound: prefix+"~"}` (the `"~"` trick
  exploits `~` (0x7E) sorting after all normal ID characters to bound a prefix scan —
  see `internal/database/pebble_store_playback.go:36-38` for `GetUserPosition`). This
  task's key format mirrors it: `bookmark:<userID>:<itemID>:<canonicalTimeKey>`.
  **This task reads that file for the pattern but does not edit it.**
  Verify:
  ```bash
  sed -n '17,55p' internal/database/pebble_store_playback.go
  # Expected: SetUserPosition/GetUserPosition using exactly the "upos:" prefix + "~"
  # upper-bound iteration pattern described above
  ```
- **ID minting:** `github.com/oklog/ulid/v2` is already an imported dependency
  (`internal/database/pebble_store.go:29`) — reuse it if you need a standalone
  bookmark identifier for anything internal (you should not need one; see Design
  below, bookmarks are keyed by time, not a separate ID, matching real ABS). **Do not
  add a new dependency** — only TASK-11 may touch `go.mod`/`go.sum` per this
  workstream's file-ownership rule.
- **Real ABS keys a bookmark by `(libraryItemId, time)`, not a separate bookmark ID** —
  `DELETE /api/me/item/:id/bookmark/:time` puts the time value itself in the URL path,
  and `PATCH .../bookmark` looks up the existing bookmark to update by matching `time`.
  This task's `Bookmark` type and store key therefore use `(userID, itemID, time)` as
  the natural key, deliberately with **no separate bookmark ID field** — this is a
  judgment call made explicit here so the future HTTP-wiring task does not have to
  guess: the URL path parameter IS the time value, parsed as a float.
- Module path is `github.com/falkcorp/audiobook-organizer`; repo Go version `go 1.26.0`.

## Design (this is the contract — implement exactly this API shape)

`internal/syncapi/progress/bookmarks.go` (pure — no `internal/database` import):

```go
package progress

// Bookmark is a named, server-persisted position within one (user, item)'s
// audio. "item" is opaque here, same as Progress.CurrentTime's item — a raw
// Book ULID today, an ABS sync_item syncID once TASK-01 ships. Deliberately
// has no separate ID field: real ABS keys a bookmark by (libraryItemId, time)
// -- the create/update/delete surface addresses it by time, not by an opaque
// ID -- so this type mirrors that rather than inventing a redundant key.
type Bookmark struct {
	UserID    string
	ItemID    string
	TimeSec   float64 // the natural key within (UserID, ItemID); see CanonicalTimeKey
	Title     string
	CreatedAt int64 // ms epoch
	UpdatedAt int64 // ms epoch
}

// ParseTimeSec parses a bookmark `time` value from either an HTTP request
// body's JSON number or a URL path segment string. Uses strconv.ParseFloat
// (never ParseInt) specifically because AudioBooth sends bookmark `time` as
// an Int in some paths (including the URL path segment on delete) while
// other call sites round-trip it as a Double -- ParseFloat accepts both
// "12" and "12.5" and "12.0" and returns the same float64 for "12" and
// "12.0". This is the single normalization point; callers must not add a
// second int-vs-float parsing path elsewhere.
func ParseTimeSec(raw string) (float64, error)

// CanonicalTimeKey converts a bookmark time in seconds into a sortable,
// collision-safe string suitable for use as (part of) a storage key: rounds
// to the nearest millisecond and zero-pads to a fixed width so lexicographic
// ordering matches numeric ordering. This guarantees "12" and "12.0" and
// "11.9996" (float rounding noise) all resolve to the identical stored
// bookmark, which is what "accept both Int and Double" actually requires at
// the storage layer -- the type-level distinction is erased here, not at
// the JSON boundary.
func CanonicalTimeKey(timeSec float64) string

// ValidateBookmark checks the invariants a Bookmark must satisfy before
// being persisted: UserID, ItemID non-empty, TimeSec >= 0 (a negative
// position is never valid), Title non-empty (real ABS requires a title;
// an untitled bookmark is not a supported case here). Returns a descriptive
// error, never panics.
func ValidateBookmark(b Bookmark) error
```

`internal/database/pebble_store_bookmarks.go` (new file, package `database`):

```go
package database

// BookmarkStore is a narrow interface satisfied structurally by *PebbleStore.
// Deliberately NOT embedded into the composed Store interface in store.go --
// wiring a bookmark HTTP handler (which would need that) is out of scope for
// this task; whichever later task adds the handler decides then whether to
// embed this into Store or accept it narrowly the way ReadingStore does in
// internal/server/handlers/reading.go.
type BookmarkStore interface {
	CreateBookmark(b progress.Bookmark) error
	ListBookmarks(userID, itemID string) ([]progress.Bookmark, error)
	UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error
	DeleteBookmark(userID, itemID string, timeSec float64) error
}

// Pebble key: "bookmark:" + userID + ":" + itemID + ":" + progress.CanonicalTimeKey(timeSec)
// Prefix-scanned per (userID, itemID) using the same LowerBound/UpperBound("~") pattern
// as GetUserPosition in pebble_store_playback.go, for ListBookmarks.

func (p *PebbleStore) CreateBookmark(b progress.Bookmark) error
func (p *PebbleStore) ListBookmarks(userID, itemID string) ([]progress.Bookmark, error)
func (p *PebbleStore) UpdateBookmarkTitle(userID, itemID string, timeSec float64, newTitle string) error
func (p *PebbleStore) DeleteBookmark(userID, itemID string, timeSec float64) error
```

`CreateBookmark` semantics: **upsert** (create-or-replace-title) at the same
`(userID, itemID, CanonicalTimeKey(timeSec))` key — calling it twice with the same time
and a different title updates the title rather than erroring, since real ABS's create
endpoint is also the natural "move the playhead and re-save at the same spot" path a
client might replay. Set `CreatedAt` only if no record exists yet at that key (preserve
original creation time across an upsert); always refresh `UpdatedAt`.

## Step-by-step

1. Confirm the START HERE pre-check (TASK-08 merged) passed.
2. Create `internal/syncapi/progress/bookmarks.go` with `Bookmark`, `ParseTimeSec`,
   `CanonicalTimeKey`, `ValidateBookmark` exactly as designed above. New file in an
   existing package — do not touch `policy.go`.
3. Write `internal/syncapi/progress/bookmarks_test.go`:
   - `TestParseTimeSec_AcceptsIntAndFloatStrings` — table: `"12"`, `"12.0"`, `"12.5"`,
     `"0"` all parse without error; `"12"` and `"12.0"` parse to the exact same
     `float64` value.
   - `TestParseTimeSec_RejectsGarbage` — `"abc"`, `""`, `"12.5.5"` all error.
   - `TestCanonicalTimeKey_IntAndFloatCollide` — `CanonicalTimeKey(12)` and
     `CanonicalTimeKey(12.0)` produce byte-identical strings (proves the Int-vs-Double
     acceptance requirement holds at the storage-key level, not just at parse time).
   - `TestCanonicalTimeKey_OrdersNumerically` — a table of increasing `TimeSec` values
     (e.g. `0, 1, 1.5, 10, 100.25, 9999`) whose `CanonicalTimeKey` outputs, when
     `sort.Strings`-sorted, come back in the SAME order as the numeric input (proves
     lexicographic == numeric ordering for the zero-padded scheme).
   - `TestValidateBookmark_RejectsNegativeTime`, `TestValidateBookmark_RejectsEmptyTitle`,
     `TestValidateBookmark_RejectsEmptyUserOrItem`, `TestValidateBookmark_AcceptsValid`.
4. Create `internal/database/pebble_store_bookmarks.go` implementing `BookmarkStore` on
   `*PebbleStore` exactly as designed above, importing `internal/syncapi/progress` for
   the `Bookmark` type and `CanonicalTimeKey`. Use `pebble.NoSync` for individual
   writes and the same iterator-with-"~"-upper-bound pattern as
   `GetUserPosition`/`ListUserPositionsForBook` for `ListBookmarks`.
5. Write `internal/database/pebble_store_bookmarks_test.go` against a real (temp-dir)
   `PebbleStore`. There is no shared test-store helper function in this package — every
   sibling `pebble_store_*_test.go` file calls the constructor directly. Verify and
   follow the same pattern:
   ```bash
   grep -n "NewPebbleStore(filepath.Join(t.TempDir()" internal/database/pebble_store_isbn_index_test.go
   # Expected: 1 hit, e.g. `store, err := NewPebbleStore(filepath.Join(t.TempDir(), "isbn-db"))`
   grep -n "^func NewPebbleStore" internal/database/pebble_store.go
   # Expected: 1 hit, `func NewPebbleStore(path string) (*PebbleStore, error)` (~:226)
   ```
   Use `store, err := NewPebbleStore(filepath.Join(t.TempDir(), "bookmarks-db"))` (fresh
   temp dir per test) — do not invent a shared helper; match the existing per-file style.
   - `TestCreateBookmark_ThenList` — create 2 bookmarks for the same (user, item),
     list returns both.
   - `TestCreateBookmark_UpsertSameTimeUpdatesTitle` — create at `time=30, title="A"`,
     create again at `time=30, title="B"` → list shows exactly ONE bookmark with
     `title="B"` (not two), and its `CreatedAt` is unchanged from the first call while
     `UpdatedAt` advances (assert `CreatedAt` is preserved -- this is the "preserve
     original creation time across an upsert" rule from the Design section).
   - `TestCreateBookmark_IntAndFloatTimeCollide` — create with `TimeSec: 12` then
     create again with `TimeSec: 12.0` (same Go literal value, but exercise it through
     `progress.ParseTimeSec("12")` and `progress.ParseTimeSec("12.0")` respectively to
     simulate two different client encodings) → list still shows exactly one bookmark.
   - `TestUpdateBookmarkTitle_NoSuchBookmarkErrors` — update against a time with no
     existing bookmark returns an error, does not silently create one.
   - `TestDeleteBookmark_RemovesOnlyThatOne` — create 2 bookmarks (different times),
     delete one by time, list shows exactly the other.
   - `TestListBookmarks_ScopedToUserAndItem` — create bookmarks for two different
     (user, item) pairs, assert `ListBookmarks` for one pair never returns the other
     pair's rows (proves the prefix-scan bound is correct, not over- or under-scoped).
   - Run with `-race`: `TestConcurrentCreateBookmark_DifferentTimesNoRace` — spin
     `registry.RunItems`-style bounded goroutines (or a simple bounded worker loop; a
     handful of bookmarks is not "whole-library scale" so a raw `sync.WaitGroup` over
     ~8 goroutines is acceptable here, unlike the CLAUDE.md whole-library rule) each
     creating a bookmark at a distinct time for the same (user, item), then assert all
     of them are present — this is the mandated `-race` proof, not a performance test.
6. Confirm no edits landed in `internal/database/store.go` or
   `internal/database/pebble_store_playback.go`:
   ```bash
   git diff --stat origin/main -- internal/database/store.go internal/database/pebble_store_playback.go
   # Expected: empty output (no changes to either file)
   ```
7. Run `gofmt -l internal/syncapi/progress/ internal/database/` and
   `go vet ./internal/syncapi/progress/... ./internal/database/...`.
8. Add a `changelog.d/` fragment:
   `changelog.d/20260730_abs_sync_bookmarks.md` (mint your own guid; explain the
   time-keyed-not-ID-keyed design decision and the Int/Double canonicalization, since
   both are the non-obvious parts a reviewer needs explained).
9. Bump file headers on every new file (fresh guids).

Anti-over-suppression: N/A — no error-suppression decisions in this task; every
rejection (`ValidateBookmark`, `UpdateBookmarkTitle` on a missing key) returns an
explicit error rather than swallowing one.

## How to test

```bash
cd "$REPO/.worktrees/abs-sync-bookmarks"
go build ./internal/syncapi/progress/... ./internal/database/...
go test ./internal/syncapi/progress/... -race -count=1 -v
go test ./internal/database/... -race -count=1 -run 'Bookmark'
# Expected: every TestXxx name from steps 3 and 5 present and PASS; -run 'Bookmark'
# scopes the (much larger) internal/database test suite to just this task's new tests
go vet ./internal/syncapi/progress/... ./internal/database/...
gofmt -l internal/syncapi/progress/ internal/database/
# Expected: empty (already formatted)
git diff --stat origin/main -- internal/database/store.go internal/database/pebble_store_playback.go
# Expected: empty (neither file touched)
go build ./...
# Expected: whole-repo build still succeeds
```

## Acceptance criteria

- [ ] `internal/syncapi/progress/bookmarks.go` exists with `Bookmark`, `ParseTimeSec`,
      `CanonicalTimeKey`, `ValidateBookmark` exactly as designed; zero
      `internal/database` import (`grep -n '"github.com/falkcorp/audiobook-organizer/internal/database"' internal/syncapi/progress/bookmarks.go` returns 0 hits)
- [ ] `internal/database/pebble_store_bookmarks.go` exists with `BookmarkStore` interface
      (not embedded in `Store`) and `CreateBookmark`/`ListBookmarks`/
      `UpdateBookmarkTitle`/`DeleteBookmark` on `*PebbleStore`
- [ ] `git diff --stat origin/main -- internal/database/store.go
      internal/database/pebble_store_playback.go` is empty — neither file touched
- [ ] All named tests in steps 3 and 5 exist and pass, including the upsert-preserves-
      `CreatedAt` test, the Int/Double-collide test (at both the pure-package and the
      store layer), and the `-race` concurrent-create test
- [ ] `go test ./internal/syncapi/progress/... ./internal/database/... -race -count=1`
      output pasted in the PR body, all green
- [ ] `gofmt -l` empty and `go vet` clean for both packages
- [ ] `go build ./...` still succeeds repo-wide
- [ ] `changelog.d/` fragment added with a unique filename
- [ ] File headers present and correct on every new file (fresh guids)
- [ ] Anti-over-suppression: N/A (documented above)

## Commit message

```
feat(abs-sync): add server-persisted bookmarks CRUD (#TASK-09)

No named-bookmark feature existed in this codebase -- only the single scalar
Book.ITunesBookmark, unrelated to ABS's per-(user,item) named-timestamp
concept. Adds internal/syncapi/progress/bookmarks.go (pure time
canonicalization: AudioBooth sends bookmark `time` as an Int, sometimes in
a URL path segment, but round-trips it as a Double elsewhere -- rounding to
milliseconds and zero-padding to a sortable key erases that distinction at
the storage layer) and internal/database/pebble_store_bookmarks.go (new
Pebble keyspace, upsert-by-(user,item,time) semantics, mirrors the existing
upos: prefix-scan pattern). Neither store.go nor pebble_store_playback.go
touched. No HTTP wiring yet -- that is later Phase-6 scope.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-bookmarks
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If both new files already exist with the functions/methods listed in the Design section
and `go test ./internal/syncapi/progress/... ./internal/database/... -race -count=1
-run 'Bookmark|ParseTimeSec|CanonicalTimeKey|ValidateBookmark'` passes, the work is
already done — run the acceptance checks instead of re-implementing. Rollback = revert
the single commit; nothing else in the repo imports `BookmarkStore` or
`progress.Bookmark` yet (verify with
`grep -rln 'BookmarkStore\|progress\.Bookmark' internal/ --include=*.go | grep -v
_test.go | grep -v pebble_store_bookmarks.go | grep -v bookmarks.go` — expect 0 hits
before this task's PR merges), so reverting is a clean no-op for every other package.
